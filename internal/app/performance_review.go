package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type performanceReviewSnapshot struct {
	runtime       modelRuntime
	modelProfile  string
	root          string
	analysis      project.Analysis
	file          project.IndexFile
	source        string
	policyVersion string
	instructions  project.EffectiveInstructions
}

func (s *Service) preparePerformanceReview(path string) (performanceReviewSnapshot, error) {
	file, err := s.manager.IndexedFile(path)
	if err != nil {
		return performanceReviewSnapshot{}, err
	}
	root := s.manager.Root()
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return performanceReviewSnapshot{}, err
	}
	if decision := policy.Decide(file.Path); !decision.Include {
		return performanceReviewSnapshot{}, fmt.Errorf("performance review path is not eligible: %s", decision.Reason)
	}
	info, err := project.GetFileInfo(root, file.Path)
	if err != nil {
		return performanceReviewSnapshot{}, err
	}
	if info.ContentHash != file.ContentHash {
		return performanceReviewSnapshot{}, project.ErrRevisionConflict
	}
	if len(info.Content) > project.PerformanceMaxSourceBytes {
		return performanceReviewSnapshot{}, fmt.Errorf("performance review skipped: source exceeds 64 KiB")
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		return performanceReviewSnapshot{}, err
	}
	instructions, err := project.ResolveInstructions(root, file.Path)
	if err != nil {
		return performanceReviewSnapshot{}, err
	}
	return performanceReviewSnapshot{runtime: s.conf().runtimes.analyze, root: root, analysis: *analysis, file: *file, source: info.Content, policyVersion: policy.Version(), instructions: instructions}, nil
}

func (s *Service) requestPerformanceReview(ctx context.Context, snapshot performanceReviewSnapshot, dispatch *analysisModelDispatch) (modelOutput, error) {
	runtime := snapshot.runtime
	if runtime.client != nil {
		runtime.client = runtime.client.WithOptionalFinalContent()
	}
	prompt, err := performancePrompt(snapshot.source, snapshot.analysis, snapshot.file)
	if err != nil {
		return modelOutput{}, err
	}
	schema := performanceReviewResponseSchema(snapshot)
	if snapshot.instructions.Text != "" {
		prompt += "\n\n" + project.InstructionPromptGuidance + snapshot.instructions.Text
	}
	timeout := s.conf().analysisTimeout
	if snapshot.modelProfile != "" {
		timeout = duration(runtime.effective.Timeout)
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := s.requestAnalysisModel(timed, runtime, []llm.ChatMessage{{Role: "user", Content: prompt}}, &schema, dispatch)
	if timed.Err() != nil {
		return modelOutput{}, timed.Err()
	}
	if err != nil {
		return modelOutput{}, err
	}
	return result, nil
}

func (s *Service) publishPerformanceReview(ctx context.Context, snapshot performanceReviewSnapshot, result modelOutput, findings []project.PerformanceFinding, warning string, authorizePublication func(func() error) error) (*project.PerformanceFileReport, error) {
	if err := s.validatePerformanceReviewSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	runtime := snapshot.runtime
	report := project.PerformanceFileReport{SchemaVersion: "1", ProjectID: snapshot.analysis.ProjectID, ProjectRevision: snapshot.analysis.ProjectRevision, Path: snapshot.file.Path, ContentHash: snapshot.file.ContentHash, Status: "completed", Findings: findings, Warning: warning, Model: runtime.profile.Model, ConfiguredModel: runtime.profile.Model, Profile: runtime.effective.Profile, Scope: runtime.effective.Scope, ProviderOrigin: runtime.effective.ProviderOrigin, ReasoningEffort: runtime.effective.ReasoningEffort, PromptVersion: project.PerformancePromptVersion, ContextPolicyVersion: snapshot.policyVersion, GeneratedAt: time.Now().UTC()}
	if result.Model != "" {
		report.Model = result.Model
	}
	report.InstructionsFingerprint = snapshot.instructions.Fingerprint
	store := func() error {
		if err := s.validatePerformanceReviewSnapshot(ctx, snapshot); err != nil {
			return err
		}
		return project.StorePerformanceFileReport(snapshot.root, report)
	}
	if err := publishAnalysisReport(store, authorizePublication); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *Service) validatePerformanceReviewSnapshot(ctx context.Context, snapshot performanceReviewSnapshot) error {
	if err := project.ValidateInstructions(snapshot.root, snapshot.file.Path, snapshot.instructions.Fingerprint); err != nil {
		return err
	}
	if s.runtimeForAnalysisScope(config.AnalyzeModelScope, snapshot.modelProfile).effective != snapshot.runtime.effective {
		return project.ErrRevisionConflict
	}
	return s.validateSourceFileSnapshot(ctx, snapshot.root, snapshot.analysis, snapshot.file, snapshot.policyVersion, false)
}

func performancePrompt(source string, analysis project.Analysis, file project.IndexFile) (string, error) {
	facts, err := json.Marshal(struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Path      string `json:"path"`
		Language  string `json:"language"`
		LineCount int    `json:"line_count"`
	}{analysis.Name, analysis.Type, file.Path, file.Language, file.LineCount})
	if err != nil {
		return "", err
	}
	return "Review exactly one supplied source file for plausible performance opportunities. This is source-based review, not measurement: never claim a bottleneck, speedup, metric, hot path, or that the project is fast. Return one JSON object only, with no Markdown fences or surrounding prose: findings array (maximum five), with category (cpu,memory,io,concurrency,caching,ui), potential_impact (high,medium,low,unknown), confidence (high,medium,low), title, observed_pattern, workload_conditions, recommendation, tradeoff, verification_plan, start_line, end_line, engineering_insight?. Require concrete source evidence and conditions; return {\"findings\":[]} if none. Do not invent issues to fill the array; an empty findings section is a successful review with no findings. " + analysisFindingTitleGuidance + project.EngineeringInsightPromptInstructions + performanceReviewAnchorGuidance + "\n\nFILE_FACTS:\n" + string(facts) + "\n\nSOURCE:\n```\n" + source + "\n```", nil
}

const performanceReviewAnchorGuidance = "Keep each finding explanation concise and under 1,000 characters; escape quotes and line breaks inside JSON strings. " +
	"Line numbers count every line of SOURCE, including blank lines, and must satisfy 1 <= start_line <= end_line <= FILE_FACTS.line_count. " +
	"Return start_line and end_line for the observed operation or pattern."
