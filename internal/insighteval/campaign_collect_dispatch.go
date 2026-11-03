package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func executeRunnerAttempt(parent context.Context, cfg EngineeringInsightRunnerConfig, item EngineeringInsightRunnerCase) (EngineeringInsightEvaluationAttempt, string, app.FileAnalysisDiagnostics, error) {
	root, prompt, target, err := buildRunnerPrompt(cfg, item)
	if err != nil {
		return EngineeringInsightEvaluationAttempt{}, "", app.FileAnalysisDiagnostics{}, err
	}
	defer os.RemoveAll(root)
	return dispatchRunnerPrompt(parent, cfg, item, prompt, target)
}

func buildRunnerPrompt(cfg EngineeringInsightRunnerConfig, item EngineeringInsightRunnerCase) (string, string, *project.IndexFile, error) {
	root, err := os.MkdirTemp("", "kogin-insight-fixture-")
	if err != nil {
		return "", "", nil, fmt.Errorf("create private evaluation fixture")
	}
	fail := func(err error) (string, string, *project.IndexFile, error) {
		_ = os.RemoveAll(root)
		return "", "", nil, err
	}
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(item.Source), 0600); err != nil {
		return fail(fmt.Errorf("write private evaluation fixture"))
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module evaluationfixture\n\ngo 1.22\n"), 0600); err != nil {
		return fail(fmt.Errorf("write private evaluation fixture"))
	}
	if err := validateRunnerFixtureCompilation(root); err != nil {
		return fail(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		return fail(err)
	}
	analysis := project.Analysis{Name: "evaluation", Type: "go", Path: root}
	if err := manager.Set(root, &analysis); err != nil {
		return fail(err)
	}
	index, err := manager.Index()
	if err != nil {
		return fail(err)
	}
	target, err := manager.IndexedFile("sample.go")
	if err != nil {
		return fail(err)
	}
	prompt, err := app.PrepareFileAnalysisEvaluation(item.Source, analysis, index, *target, cfg.Profile)
	if err != nil {
		return fail(err)
	}
	return root, prompt, target, nil
}

func dispatchRunnerPrompt(parent context.Context, cfg EngineeringInsightRunnerConfig, item EngineeringInsightRunnerCase, prompt string, target *project.IndexFile) (EngineeringInsightEvaluationAttempt, string, app.FileAnalysisDiagnostics, error) {
	timed, cancel := context.WithTimeout(parent, qualificationAttemptTimeoutSeconds*time.Second)
	defer cancel()
	started := time.Now()
	response, err := cfg.Client.ChatWithJSONSchema(timed, []llm.ChatMessage{{Role: "user", Content: prompt}}, app.FileAnalysisResponseSchema())
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		attempt := buildFailedRunnerAttemptWithElapsed(item.Expected, cfg.CandidateID, err, elapsed)
		if errors.Is(err, llm.ErrStructuredRequestRejected) {
			return attempt, "", app.FileAnalysisDiagnostics{}, err
		}
		return attempt, "", app.FileAnalysisDiagnostics{}, nil
	}
	message := response.Choices[0].Message
	content := message.Content
	emitted := buildEvaluationResponseMaterial(message)
	attempt := EngineeringInsightEvaluationAttempt{CaseName: item.Expected.CaseName, Partition: item.Expected.Partition, Intent: item.Expected.Intent, Repetition: item.Expected.Repetition, Attempt: item.Expected.Attempt, CandidateID: cfg.CandidateID, OutputTokens: response.Usage.CompletionTokens, ElapsedMilliseconds: float64(elapsed), OptionalInsight: "not_evaluated"}
	if emitted != "" {
		digest := sha256.Sum256([]byte(emitted))
		attempt.EmittedResponse = true
		attempt.ResponseDigest = hex.EncodeToString(digest[:])
	}
	assessment := app.AssessFileAnalysisEvaluation(content, *target, item.Source)
	if attempt.OutputTokens < 0 || attempt.OutputTokens > qualificationOutputTokenCap || response.Model != "" && response.Model != cfg.Model {
		attempt.Outcome, attempt.FinishReason = "failed", "error"
		attempt.InvalidProviderMetadata = true
		return attempt, emitted, assessment.Diagnostics, nil
	}
	if response.Choices[0].FinishReason == "length" {
		attempt.Outcome, attempt.FinishReason = "truncated", "length"
		return attempt, emitted, assessment.Diagnostics, nil
	}
	if response.Choices[0].FinishReason != "" && response.Choices[0].FinishReason != "stop" {
		attempt.Outcome, attempt.FinishReason = "failed", "error"
		return attempt, emitted, assessment.Diagnostics, nil
	}
	if strings.TrimSpace(content) == "" {
		attempt.Outcome, attempt.FinishReason = "malformed", "stop"
		return attempt, emitted, app.FileAnalysisDiagnostics{}, nil
	}
	if !assessment.UsableSummary {
		attempt.Outcome, attempt.FinishReason = "malformed", "stop"
		return attempt, emitted, assessment.Diagnostics, nil
	}
	attempt.Outcome, attempt.FinishReason, attempt.UsableSummary = "completed", "stop", true
	attempt.OptionalInsight = assessment.Diagnostics.OptionalInsight()
	attempt.OptionalSectionDegraded = assessment.Diagnostics.Degraded()
	attempt.CompleteSummary = !attempt.OptionalSectionDegraded
	return attempt, emitted, assessment.Diagnostics, nil
}

// buildEvaluationResponseMaterial retains every non-empty provider response field
// for private scoring. The semantic parser receives only final content, so
// separately returned reasoning is never interpreted as a JSON prefix.
func buildEvaluationResponseMaterial(message llm.ChatMessage) string {
	parts := make([]string, 0, 3)
	for _, value := range []string{message.ReasoningContent, message.Reasoning, message.Content} {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "\n\n")
}

func validateRunnerFixtureCompilation(root string) error {
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	if err := command.Run(); err != nil {
		return fmt.Errorf("compile private evaluation fixture")
	}
	return nil
}

func buildFailedRunnerAttempt(expected EngineeringInsightExpectedAttempt, candidate string, cause error) EngineeringInsightEvaluationAttempt {
	return buildFailedRunnerAttemptWithElapsed(expected, candidate, cause, 0)
}

func buildFailedRunnerAttemptWithElapsed(expected EngineeringInsightExpectedAttempt, candidate string, cause error, elapsed int64) EngineeringInsightEvaluationAttempt {
	attempt := buildUnknownReservedAttempt(expected, candidate)
	attempt.ElapsedMilliseconds = float64(elapsed)
	if errors.Is(cause, context.DeadlineExceeded) {
		attempt.Outcome, attempt.FinishReason = "timeout", "timeout"
	} else if errors.Is(cause, context.Canceled) {
		attempt.Outcome, attempt.FinishReason = "failed", "canceled"
	} else {
		attempt.Outcome, attempt.FinishReason = "failed", "error"
	}
	return attempt
}
