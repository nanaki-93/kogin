package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const analysisStaleExclusion = "No stale analysis for this file."
const analysisStalePathExclusion = "Outside the selected stale file."
const analysisRetryExclusion = "No stale or failed analysis for this file."
const analysisRecoveryExclusion = "No incomplete analysis for this file."

// analysisScopedSelection narrows a new admission to files with earlier work to finish.
type analysisScopedSelection uint8

const (
	analysisSelectAll analysisScopedSelection = iota
	analysisSelectStale
	// analysisSelectStaleFailed retries stale and failed stages, and stages a
	// canceled or stopped run left unfinished: canceling a retry keeps them due.
	analysisSelectStaleFailed
	// analysisSelectIncomplete selects every recoverable stage for recover_incomplete.
	analysisSelectIncomplete
)

func analysisSelectionMode(retryStaleFailed, recoverIncomplete, staleOnly bool) analysisScopedSelection {
	switch {
	case staleOnly:
		return analysisSelectStale
	case recoverIncomplete:
		return analysisSelectIncomplete
	case retryStaleFailed:
		return analysisSelectStaleFailed
	default:
		return analysisSelectAll
	}
}

// exclusion is the reason recorded for inventory files that the selection omits.
func (mode analysisScopedSelection) exclusion() string {
	switch mode {
	case analysisSelectStale:
		return analysisStaleExclusion
	case analysisSelectStaleFailed:
		return analysisRetryExclusion
	case analysisSelectIncomplete:
		return analysisRecoveryExclusion
	default:
		return ""
	}
}

func (mode analysisScopedSelection) selects(status string, configured bool) bool {
	switch mode {
	case analysisSelectStale:
		return status == "stale"
	case analysisSelectStaleFailed:
		return status == "stale" || status == "failed" || status == "canceled" || status == "interrupted"
	case analysisSelectIncomplete:
		return analysisStageRecoveryFor(status, configured) == analysisStageRecoverable
	default:
		return false
	}
}

// analysisStageRecovery classifies an eligible stage observation for recovery.
// Source changes and unreadable files block the whole file before classification.
type analysisStageRecovery uint8

const (
	// analysisStageSettled is current evidence or work owned by an active/continuable run.
	analysisStageSettled analysisStageRecovery = iota
	analysisStageRecoverable
	// analysisStageNeedsModel is unfinished work that a retry cannot dispatch.
	analysisStageNeedsModel
)

// analysisStageRecoveryFor is the single recovery rule shared by admission and the
// selection summary. Pending, running and paused stages belong to their run.
func analysisStageRecoveryFor(status string, configured bool) analysisStageRecovery {
	switch status {
	case "stale", "failed", "partial", "missing", "canceled", "interrupted", "unavailable":
		if !configured {
			return analysisStageNeedsModel
		}
		return analysisStageRecoverable
	default:
		return analysisStageSettled
	}
}

// Passive Security rules need no model; every other stage needs a configured client.
func (s *Service) analysisStageConfigured(stage AnalysisStage, models *AnalysisModels) bool {
	return stage == AnalysisStageSecurityRules || s.analysisModelRuntime(stage, models).client != nil
}

// A scoped preview keeps every eligible file: files the scope selects get their
// selected stages redispatched, and every other file is retained with its saved
// results, so a retry never drops the rest of the project's analysis. Resume
// keeps the admitted plan even as this run replaces stale reports; new starts
// recompute the selection, so changed evidence invalidates an old preview.
func (s *Service) scopeAnalysisRetryPreview(ctx context.Context, mode analysisScopedSelection, preview *AnalysisRunPreview, resume *AnalysisRunIdentity) error {
	if preview.StalePath != "" && !slices.ContainsFunc(preview.Files, func(file AnalysisPlannedFile) bool { return file.Path == preview.StalePath }) {
		return fmt.Errorf("%w: stale file is not in the eligible saved selection", project.ErrRevisionConflict)
	}
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		return err
	}
	indexed := make(map[string]project.IndexFile, len(index.Files))
	for _, file := range index.Files {
		indexed[file.Path] = file
	}
	admitted := make(map[string]AnalysisPlannedFile)
	if run := s.analysisRun.run; run != nil && run.Identity.ProjectID == preview.Identity.ProjectID {
		for _, file := range run.Plan.Files {
			admitted[file.Path] = file
		}
	}
	evidence := selectionEvidence(s.analysisRun.run, *analysis)
	kept := []AnalysisPlannedFile{}
	for _, file := range preview.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if resume != nil {
			previous, captured := admitted[file.Path]
			if !captured {
				// Runs admitted before retention excluded the files outside their scope.
				preview.Excluded = append(preview.Excluded, AnalysisExcludedFile{Path: file.Path, Reason: legacyAnalysisScopeExclusion(mode, preview.StalePath, file.Path)})
				continue
			}
			s.resumeAnalysisScopedFile(&file, previous, preview.Limits, preview.Models)
		} else if preview.StalePath != "" && file.Path != preview.StalePath || !s.planAnalysisRetryFile(mode, *analysis, indexed[file.Path], policy, &file, evidence, preview.Limits, preview.Models) {
			s.retainAnalysisFile(*analysis, indexed[file.Path], policy, &file, evidence, preview.Models)
		}
		kept = append(kept, file)
	}
	preview.Files = kept
	return nil
}

func legacyAnalysisScopeExclusion(mode analysisScopedSelection, stalePath, path string) string {
	if stalePath != "" && path != stalePath {
		return analysisStalePathExclusion
	}
	return mode.exclusion()
}

// resumeAnalysisScopedFile repeats the admitted plan for one file.
func (s *Service) resumeAnalysisScopedFile(file *AnalysisPlannedFile, previous AnalysisPlannedFile, limits AnalysisRunLimits, models *AnalysisModels) {
	file.Retained = previous.Retained
	for j := range file.Stages {
		stage, admitted := &file.Stages[j], previous.Stages[j]
		switch {
		case previous.Retained:
			stage.Carried, stage.Reason, stage.MaxModelRequests = admitted.Carried, admitted.Reason, 0
			if admitted.Carried != "" {
				stage.Cached = false
			}
		case !admitted.Cached:
			s.planAnalysisRetryStage(stage, limits, models)
		}
	}
}

// retainAnalysisFile keeps a file outside the scope: stages with a current saved
// report are read back from it, and the others carry their earlier outcome.
func (s *Service) retainAnalysisFile(analysis project.Analysis, indexed project.IndexFile, policy *project.ContextPolicy, file *AnalysisPlannedFile, evidence analysisSelectionEvidence, models *AnalysisModels) {
	file.Retained = true
	for i := range file.Stages {
		stage := &file.Stages[i]
		stage.MaxModelRequests = 0
		if !stage.Eligible || stage.Cached {
			continue
		}
		stage.Carried, stage.Reason = carriedAnalysisStage(s.analysisSelectionStageForModels(analysis, indexed, policy, stage.Stage, evidence, models))
	}
}

// Reasons for retained stages without a current saved report.
const (
	carriedFailedReason      = "This stage failed before analysis could complete."
	carriedStaleReason       = "Saved analysis no longer matches this file, its instructions or analysis configuration."
	carriedMissingReason     = "No saved analysis exists for this stage."
	carriedIncompleteReason  = "The saved analysis is incomplete. Retry analysis to complete coverage."
	carriedCanceledReason    = "The run was canceled before this stage completed."
	carriedStoppedReason     = "The earlier run stopped before this stage completed."
	carriedUnavailableReason = "No usable analysis is available for this stage."
)

var carriedAnalysisReasons = []string{carriedFailedReason, carriedStaleReason, carriedMissingReason, carriedIncompleteReason, carriedCanceledReason, carriedStoppedReason, carriedUnavailableReason}

// carriedAnalysisStage turns a stage's current observation into the outcome a
// retained file keeps. Saved reasons stay when they are already a stored reason
// (such as why a model request failed); others get a fixed one.
func carriedAnalysisStage(observed AnalysisFileStageStatus) (AnalysisStageStatus, string) {
	status, reason := AnalysisStageSkipped, carriedMissingReason
	switch observed.Status {
	case "failed":
		status, reason = AnalysisStageFailed, carriedFailedReason
	case "stale":
		status, reason = AnalysisStageStale, carriedStaleReason
	case "partial":
		reason = carriedIncompleteReason
	case "canceled":
		status, reason = AnalysisStageCanceled, carriedCanceledReason
	case "interrupted", "paused", "pending", "running":
		status, reason = AnalysisStageCanceled, carriedStoppedReason
	case "unavailable":
		status, reason = AnalysisStageUnavailable, carriedUnavailableReason
	}
	if observed.Reason != "" && observed.Status != "missing" && observed.Status != "partial" && analysisMetadataReason(observed.Reason) {
		reason = observed.Reason
	}
	return status, reason
}

func validCarriedAnalysisStatus(status AnalysisStageStatus) bool {
	switch status {
	case AnalysisStageFailed, AnalysisStageStale, AnalysisStageSkipped, AnalysisStageCanceled, AnalysisStageUnavailable:
		return true
	}
	return false
}

func (s *Service) planAnalysisRetryFile(mode analysisScopedSelection, analysis project.Analysis, indexed project.IndexFile, policy *project.ContextPolicy, file *AnalysisPlannedFile, evidence analysisSelectionEvidence, limits AnalysisRunLimits, models *AnalysisModels) bool {
	include := false
	for i := range file.Stages {
		stage := &file.Stages[i]
		if !stage.Eligible {
			continue
		}
		status := s.analysisSelectionStageForModels(analysis, indexed, policy, stage.Stage, evidence, models).Status
		if mode.selects(status, s.analysisStageConfigured(stage.Stage, models)) {
			include = true
			s.planAnalysisRetryStage(stage, limits, models)
		}
	}
	return include
}

func (s *Service) planAnalysisRetryStage(stage *AnalysisStagePlan, limits AnalysisRunLimits, models *AnalysisModels) {
	// Retain this cache bypass in the admitted plan, including across resume.
	stage.Cached = false
	if !stage.Eligible || stage.Stage == AnalysisStageSecurityRules || s.analysisModelRuntime(stage.Stage, models).client == nil {
		return
	}
	runtime := s.analysisModelRuntime(stage.Stage, models)
	stage.MaxModelRequests = min(limits.MaxAttemptsPerStage, runtime.effective.MaxRetries+1)
}
