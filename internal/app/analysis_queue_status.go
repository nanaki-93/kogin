package app

import (
	"context"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// These are current cache/run observations, not a new analysis or dispatch plan.
type AnalysisFileStageStatus struct {
	Stage  AnalysisStage `json:"stage"`
	Status string        `json:"status"`
	Reason string        `json:"reason"`
}

type analysisSelectionEvidence struct {
	files    map[string]AnalysisRunFile
	excluded map[string]string
	run      *AnalysisRun
	// carried marks retained stages that kept an earlier outcome; a carried
	// stale stage defers to the saved report, which still says stale.
	carried map[string][]AnalysisStageStatus
}

func selectionEvidence(run *AnalysisRun, analysis project.Analysis) analysisSelectionEvidence {
	evidence := analysisSelectionEvidence{files: map[string]AnalysisRunFile{}, excluded: map[string]string{}}
	if run == nil || run.Identity.ProjectID != analysis.ProjectID {
		return evidence
	}
	for _, file := range run.Plan.Excluded {
		evidence.excluded[file.Path] = file.Reason
	}
	evidence.run = run
	evidence.carried = map[string][]AnalysisStageStatus{}
	for i, file := range run.Files {
		evidence.files[file.Path] = file
		if planned := run.Plan.Files[i]; planned.Retained {
			for _, stage := range planned.Stages {
				evidence.carried[file.Path] = append(evidence.carried[file.Path], stage.Carried)
			}
		}
	}
	return evidence
}

// The boolean reports whether the file is readable and still matches its index entry.
func (s *Service) analysisSelectionStages(ctx context.Context, analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, evidence analysisSelectionEvidence) ([]AnalysisFileStageStatus, bool, error) {
	result := make([]AnalysisFileStageStatus, 0, len(analysisStages))
	info, readErr := project.GetFileInfo(s.manager.Root(), file.Path)
	for _, stage := range analysisStages {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		switch {
		case readErr != nil:
			result = append(result, AnalysisFileStageStatus{Stage: stage, Status: "unavailable", Reason: "The file could not be read. Refresh project facts and retry."})
		case info.ContentHash != file.ContentHash:
			result = append(result, AnalysisFileStageStatus{Stage: stage, Status: "stale", Reason: "The file changed since indexing. Refresh project facts, then analyze again."})
		default:
			result = append(result, s.analysisSelectionStage(analysis, file, policy, stage, evidence))
		}
	}
	return result, readErr == nil && info.ContentHash == file.ContentHash, nil
}

// models are the evidence run's captured choices, or nil (defaults) without a matching run.
func (e analysisSelectionEvidence) models() *AnalysisModels {
	if e.run == nil {
		return nil
	}
	return e.run.Plan.Models
}

func (s *Service) analysisSelectionStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, evidence analysisSelectionEvidence) AnalysisFileStageStatus {
	return s.analysisSelectionStageForModels(analysis, file, policy, stage, evidence, evidence.models())
}

func (s *Service) analysisSelectionStageForModels(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, evidence analysisSelectionEvidence, models *AnalysisModels) AnalysisFileStageStatus {
	result := AnalysisFileStageStatus{Stage: stage}
	if reason := analysisStageExclusion(file, stage); reason != "" {
		result.Status, result.Reason = "skipped", reason
		return result
	}
	cached, status, generatedAt, err := s.analysisStageCacheState(analysis, file, policy, stage, models)
	if item, ok := evidence.stage(file, stage); ok && s.analysisModelRuntime(stage, models).effective == s.analysisModelRuntime(stage, evidence.models()).effective {
		if runStatus, reason := selectionRunStage(item, evidence.run); runStatus != "" && selectionRunOverridesCache(item, evidence.run, cached, status, generatedAt) {
			result.Status, result.Reason = runStatus, reason
			return result
		}
	}

	switch {
	case err != nil:
		result.Status, result.Reason = "unavailable", "Saved analysis could not be read. Refresh files and retry analysis."
	case cached && status != "partial":
		result.Status, result.Reason = "fresh", "Saved analysis is up to date."
	default:
		result.Status, result.Reason = selectionCacheStatus(status)
		if result.Status == "missing" && s.analysisModelRuntime(stage, models).client == nil && stage != AnalysisStageSecurityRules {
			result.Status, result.Reason = "unavailable", "The model for this stage is not configured."
		}
		if result.Status == "missing" && evidence.excluded[file.Path] != "" {
			result.Reason = "Excluded from the previous run: " + evidence.excluded[file.Path]
		}
	}
	return result
}

func (e analysisSelectionEvidence) stage(file project.IndexFile, stage AnalysisStage) (AnalysisStageProgress, bool) {
	progress, ok := e.files[file.Path]
	if !ok || progress.ContentHash != file.ContentHash {
		return AnalysisStageProgress{}, false
	}
	for i, item := range progress.Stages {
		if item.Stage == stage {
			if carried := e.carried[file.Path]; i < len(carried) && carried[i] == AnalysisStageStale {
				return AnalysisStageProgress{}, false
			}
			return item, true
		}
	}
	return AnalysisStageProgress{}, false
}

func selectionCacheStatus(status string) (string, string) {
	switch status {
	case "stale":
		return "stale", "Saved analysis no longer matches this file, its instructions or analysis configuration."
	case "failed":
		return "failed", "The previous analysis failed. Retry analysis for this file."
	case "partial":
		return "partial", "The saved analysis is incomplete. Retry analysis to complete coverage."
	case "unavailable":
		return "unavailable", "No usable analysis is available for this stage."
	case "", "missing":
		return "missing", "No saved analysis exists for this stage."
	default:
		return "unavailable", "The saved analysis is not usable for the current project."
	}
}

var selectionStageReasons = map[AnalysisStageStatus]string{
	AnalysisStageCanceled:    "The run was canceled before this stage completed.",
	AnalysisStageInterrupted: "The run was interrupted during this stage. Resume the run to continue.",
	AnalysisStageFailed:      "This stage failed before analysis could complete.",
	AnalysisStagePartial:     "This stage returned incomplete analysis.",
	AnalysisStageSkipped:     "This stage was skipped in the previous run.",
	AnalysisStageUnavailable: "The analyzer or model was unavailable for this stage.",
}

func selectionRunStage(stage AnalysisStageProgress, run *AnalysisRun) (string, string) {
	switch stage.Status {
	case AnalysisStageCompleted, AnalysisStageCompletedEmpty:
		return "", "" // Current cache identity establishes freshness, rather than historic completion.
	case AnalysisStagePending:
		return selectionWaitingReason(run)
	case AnalysisStageRunning:
		return "running", "Analysis is currently processing this stage."
	case AnalysisStageStale:
		return "interrupted", "The run stopped after its inputs changed. Current file evidence determines freshness."
	default:
		reason, ok := selectionStageReasons[stage.Status]
		if !ok {
			return "", ""
		}
		if stage.Reason != "" {
			reason = stage.Reason
		}
		status := string(stage.Status)
		if stage.Status == AnalysisStageSkipped {
			status = "missing"
		}
		return status, reason
	}
}

func selectionWaitingReason(run *AnalysisRun) (string, string) {
	switch run.Status {
	case AnalysisRunPaused, AnalysisRunPausing:
		return "paused", "The run is paused before this stage. Resume the run to continue."
	case AnalysisRunInterrupted:
		return "interrupted", "The run was interrupted before this stage. Resume the run to continue."
	case AnalysisRunCanceled:
		return "canceled", "The run was canceled before this stage completed."
	case AnalysisRunStale:
		return "interrupted", "The run became outdated before this stage completed. Start a new analysis."
	default:
		return "pending", "This stage is waiting in the analysis queue."
	}
}

// A failed refresh must not be hidden by a report from before that run. A later
// explicit review may replace the failure without rerunning the whole project.
func selectionRunOverridesCache(stage AnalysisStageProgress, run *AnalysisRun, cached bool, cacheStatus string, generatedAt time.Time) bool {
	if run != nil && run.Status == AnalysisRunStale && (stage.Status == AnalysisStagePending || stage.Status == AnalysisStageStale) && (cached || cacheStatus == "stale") {
		return false
	}
	if stage.Status == AnalysisStagePending || stage.Status == AnalysisStageRunning {
		return true
	}
	if run != nil && !generatedAt.IsZero() && !generatedAt.Before(run.CreatedAt) {
		return false
	}
	switch stage.Status {
	case AnalysisStageFailed, AnalysisStageStale, AnalysisStagePartial:
		return true
	default:
		return !cached
	}
}
