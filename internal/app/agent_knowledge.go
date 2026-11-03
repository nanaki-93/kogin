package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func runKnowledgeHost[T any](ctx context.Context, kind agentruntime.KnowledgeKind, host func(context.Context) (T, string, error)) (value T, err error) {
	_, err = agentruntime.RunKnowledgeGraph(ctx, kind, func(ctx context.Context) (string, error) {
		var status string
		var hostErr error
		value, status, hostErr = host(ctx)
		return status, hostErr
	})
	return value, err
}

func knowledgeReportStatus(status string) string {
	switch status {
	case "fresh", "ready", "completed", "completed_empty":
		return "completed"
	case "failed", "partial":
		return status
	default:
		return "unavailable"
	}
}

// Project facts remain provider-neutral. This adapter makes the same single
// structured request through the native runner and preserves safe diagnostics.
type overviewCompletion struct{ runtime modelRuntime }

func (c overviewCompletion) ChatWithJSONSchema(ctx context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	result, err := c.runtime.execute(ctx, agentruntime.Request{Messages: messages, Schema: &schema, ContextMaxTokens: c.runtime.effective.ContextMaxTokens})
	if err != nil {
		if errors.Is(err, agentstate.ErrPersistence) || errors.Is(err, agentruntime.ErrPolicyDenied) {
			return nil, fmt.Errorf("%w: %w", project.ErrAnalysisPublicationBlocked, err)
		}
		return nil, err
	}
	return &llm.ChatResponse{Model: result.Model, Usage: result.Usage, Choices: []llm.ChatChoice{{FinishReason: result.FinishReason, Message: llm.ChatMessage{Role: "assistant", Content: result.Content}}}}, nil
}

func (s *Service) analyzeProjectKnowledge(ctx context.Context, root string, target *configuration) (*project.Analysis, error) {
	timed, cancel := context.WithTimeout(ctx, target.importTimeout)
	defer cancel()
	snapshot, err := project.ScanProject(timed, root)
	if err != nil {
		return nil, err
	}
	return s.writeKnowledgeOverview(timed, snapshot, target.runtimes.analyze, "", false)
}

// writeKnowledgeOverview has a model write the project overview for a
// snapshot; note, when set, is read before the project's context. A rewrite
// that fails leaves the saved overview as it was.
func (s *Service) writeKnowledgeOverview(ctx context.Context, snapshot *project.Analysis, runtime modelRuntime, note string, rewrite bool) (*project.Analysis, error) {
	admitted, release, err := s.agentRunContextForSnapshot(ctx, snapshot.Path, snapshot, runtime)
	if err != nil {
		return nil, err
	}
	defer release()
	analyzer := project.NewAnalyzerWithProvenance(overviewCompletion{runtime}, runtime.profile.Model, string(config.AnalyzeModelScope), runtime.effective.ProviderOrigin, runtime.effective.ReasoningEffort).WithContextNote(note)
	if rewrite {
		analyzer.KeepingSavedOnFailure()
	}
	analysis, err := runKnowledgeHost(admitted, agentruntime.KnowledgeOverview, func(ctx context.Context) (*project.Analysis, string, error) {
		result, err := analyzer.AnalyzeSnapshot(ctx, snapshot)
		if result == nil {
			return nil, "failed", err
		}
		return result, knowledgeReportStatus(result.AIStatus), err
	})
	if errors.Is(err, agentstate.ErrPersistence) || errors.Is(err, agentruntime.ErrPolicyDenied) {
		return nil, err
	}
	// Provider timeout/cancellation produces a failed interpretation, while the
	// rechecked factual inventory remains usable. Never suppress other failures.
	if analysis != nil && analysis.AIStatus == project.ProjectAnalysisStatusFailed && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return analysis, nil
	}
	return analysis, err
}
