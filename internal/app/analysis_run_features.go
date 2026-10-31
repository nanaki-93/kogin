package app

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// Feature discovery was retired. Saved runs that included it stay readable:
// their plan and progress keep these shapes, are validated as before and
// count in the run's coverage, but nothing runs them and they cannot resume.

// AnalysisFeaturePlan is the plan of a saved run's feature-discovery step.
type AnalysisFeaturePlan struct {
	ExpectedHash     string   `json:"expected_hash"`
	GoalsHash        string   `json:"goals_hash"`
	WorkspaceHash    string   `json:"workspace_hash"`
	ExcludedPaths    []string `json:"excluded_paths"`
	ProviderID       string   `json:"provider_id"`
	MaxModelRequests int      `json:"max_model_requests"`
	Reason           string   `json:"reason,omitempty"`
}

// AnalysisFeatureProgress is the outcome of a saved run's feature-discovery step.
type AnalysisFeatureProgress struct {
	Status          AnalysisStageStatus `json:"status"`
	Attempts        int                 `json:"attempts"`
	SuggestionCount *int                `json:"suggestion_count"`
	ReportHash      string              `json:"report_hash,omitempty"`
	Reason          string              `json:"reason,omitempty"`
}

func validateStoredAnalysisFeatures(run *AnalysisRun) error {
	plan, progress := run.Plan.Features, run.Features
	if plan == nil && progress == nil {
		return nil
	}
	if plan == nil || progress == nil || !validAnalysisFeaturePlan(plan) {
		return errAnalysisRunCorrupt
	}
	if validateAnalysisExcludedPaths(plan.ExcludedPaths) != nil {
		return errAnalysisRunCorrupt
	}
	stage := AnalysisStageProgress{Stage: AnalysisStageFeatures, Status: progress.Status, Attempts: progress.Attempts,
		FindingCount: progress.SuggestionCount, ReportID: strings.TrimPrefix(progress.ReportHash, "sha256:"), Reason: progress.Reason}
	stagePlan := AnalysisStagePlan{Stage: AnalysisStageFeatures, Eligible: true, ProviderID: plan.ProviderID, MaxModelRequests: plan.MaxModelRequests, Reason: plan.Reason}
	count, err := validateStoredAnalysisStage(stage, stagePlan, AnalysisStageFeatures, run.Plan.Limits.MaxAttemptsPerStage)
	if err != nil || count > 5 || progress.Status == AnalysisStagePartial || progress.Status == AnalysisStageSkipped || progress.ReportHash != "" && !analysisFeatureHash(progress.ReportHash) {
		return errAnalysisRunCorrupt
	}
	return nil
}

func analysisFeatureHash(hash string) bool {
	return strings.HasPrefix(hash, "sha256:") && len(hash) == 71
}

func validAnalysisFeaturePlan(plan *AnalysisFeaturePlan) bool {
	return analysisFeatureHash(plan.GoalsHash) && analysisFeatureHash(plan.WorkspaceHash) && len(plan.ProviderID) == 64 &&
		(plan.ExpectedHash == "empty" || analysisFeatureHash(plan.ExpectedHash)) && len(plan.ExcludedPaths) <= maxAnalysisInventoryFiles
}

// errAnalysisFeaturesRetired refuses to resume a saved run that included
// feature discovery, which Mini-Orca no longer has.
var errAnalysisFeaturesRetired = fmt.Errorf("%w: this analysis included feature discovery, which Mini-Orca no longer has; cancel it and start a new analysis", project.ErrRevisionConflict)

// featureSuggestionsPromptVersion is in the provider fingerprint of saved
// runs that included feature discovery, so their freshness reads as before.
const featureSuggestionsPromptVersion = "feature-suggestions-v4"

// Reasons saved runs of the retired feature-discovery step may carry.
const analysisFeatureHistoryFullReason = "Feature history is full; existing ideas were kept."
