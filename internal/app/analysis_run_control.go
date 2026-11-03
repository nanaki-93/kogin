package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func (s *Service) ControlAnalysisRun(ctx context.Context, request AnalysisRunControlRequest) (*AnalysisRun, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Action == AnalysisRunResume {
		if err := s.validateAnalysisModelSelections(ctx, nil, &request.Identity); err != nil {
			return nil, err
		}
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	// Recoverable write failures retain the in-memory state; only explicit controls
	// may attempt another save. Restore errors without a retained run remain fatal.
	if err := s.restoreAnalysisRunLocked(); err != nil && (c.run == nil || c.fault == nil) {
		return nil, err
	}
	if c.run == nil || c.run.Identity != request.Identity {
		return nil, project.ErrRevisionConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch request.Action {
	case AnalysisRunCancel:
		return s.cancelAnalysisRunLocked()
	case AnalysisRunPause:
		return s.pauseAnalysisRunLocked()
	case AnalysisRunResume:
		return s.resumeAnalysisRunLocked(ctx, request)
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) cancelAnalysisRunLocked() (*AnalysisRun, error) {
	c := s.analysisRun
	if c.run.Status == AnalysisRunStale {
		if c.fault == nil {
			return cloneAnalysisRun(c.run), project.ErrRevisionConflict
		}
		// Discarding stale work must remain possible after a failed save.
		// Recovery preserves its stale identity and cannot restart requests.
		c.run.Reason = analysisRunStaleReason
		err := s.saveAnalysisRunLocked(true)
		return cloneAnalysisRun(c.run), err
	}
	c.run.Status = AnalysisRunCanceled
	if c.done != nil {
		c.run.Status = AnalysisRunCanceling
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.run.Reason = "Analysis canceled by the user."
	interruptAnalysisStages(c.run, AnalysisStageCanceled)
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) pauseAnalysisRunLocked() (*AnalysisRun, error) {
	c := s.analysisRun
	if c.run.Status != AnalysisRunRunning && c.run.Status != AnalysisRunQueued && c.run.Status != AnalysisRunPausing && c.run.Status != AnalysisRunPaused && c.fault == nil {
		return nil, fmt.Errorf("%w: analysis cannot be paused in its current state", project.ErrRevisionConflict)
	}
	c.run.Status = AnalysisRunPaused
	if c.done != nil && c.fault == nil {
		c.run.Status = AnalysisRunPausing
	}
	c.run.Reason = "Analysis paused by the user."
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return cloneAnalysisRun(c.run), nil
}

func (s *Service) resumeAnalysisRunLocked(ctx context.Context, request AnalysisRunControlRequest) (*AnalysisRun, error) {
	c := s.analysisRun
	if c.done != nil {
		return cloneAnalysisRun(c.run), errAnalysisRunBusy
	}
	if c.run.Status != AnalysisRunPaused && c.run.Status != AnalysisRunInterrupted {
		return nil, fmt.Errorf("%w: analysis requires paused or interrupted progress to resume", project.ErrRevisionConflict)
	}
	preview, err := s.analysisPreviewLocked(ctx, AnalysisPreviewRequest{ProjectID: c.run.Identity.ProjectID, ProjectRevision: c.run.Identity.ProjectRevision, Scope: AnalysisRunScopeProject, RetryStaleFailed: c.run.Plan.RetryStaleFailed, StaleOnly: c.run.Plan.StaleOnly, StalePath: c.run.Plan.StalePath, RecoverIncomplete: c.run.Plan.RecoverIncomplete, Refresh: c.run.Plan.Refresh, Limits: c.run.Plan.Limits, ResumeRun: &request.Identity})
	if err != nil {
		return nil, err
	}
	if request.PreviewID != preview.PreviewID {
		return nil, project.ErrRevisionConflict
	}
	if err := validateAnalysisConfirmations(preview, *request.Confirmations); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisQueue(ctx, c.root, preview, true); err != nil {
		return nil, err
	}
	c.run.Identity.Generation = newOpaqueID("generation")
	c.run.Status = AnalysisRunQueued
	c.run.Reason = ""
	c.run.WindowElapsedSeconds = 0
	c.run.WindowFilesCompleted = 0
	if err := s.saveAnalysisRunLocked(true); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	result := cloneAnalysisRun(c.run)
	if err := s.launchAnalysisRunLocked(preview, *request.Confirmations); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return result, nil
}

func (s *Service) interruptAnalysisRunForProjectChange() {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return
	}
	// Leaving or restoring a project revokes dispatch authority, not the
	// freshness of its evidence. Reads validate the captured source identity.
	switch c.run.Status {
	case AnalysisRunQueued, AnalysisRunRunning, AnalysisRunPausing:
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis was interrupted; preview and resume explicitly."
		interruptAnalysisStages(c.run, AnalysisStageInterrupted)
	case AnalysisRunCanceling:
		c.run.Status = AnalysisRunCanceled
		interruptAnalysisStages(c.run, AnalysisStageCanceled)
	default:
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	_ = s.saveAnalysisRunLocked(false) // Retained fault blocks any further dispatch.
}
