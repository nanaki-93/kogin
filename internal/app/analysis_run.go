package app

import (
	"context"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

type analysisRunRecovery struct {
	run   *AnalysisRun
	fault error
}

type analysisRunController struct {
	detached       map[string]analysisRunRecovery
	mu             sync.Mutex
	run            *AnalysisRun
	root           string
	fault          error
	done           chan struct{}
	cancel         context.CancelFunc
	admission      *AnalysisRunPreview
	confirmations  AnalysisRunConfirmations
	windowStart    time.Time
	elapsedBase    int64
	windowBudget   time.Duration
	fileStopReason string
}

func (s *Service) StartAnalysisRun(ctx context.Context, request AnalysisRunStartRequest) (*AnalysisRun, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisModelSelections(ctx, request.Models, nil); err != nil {
		return nil, err
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	if request.ClearResults {
		s.changesMu.Lock()
		defer s.changesMu.Unlock()
	}
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.startAnalysisRunLocked(ctx, request)
}

func canReplaceAnalysisRun(run *AnalysisRun, clearResults bool) bool {
	if run == nil {
		return true
	}
	switch run.Status {
	case AnalysisRunRunning, AnalysisRunQueued, AnalysisRunPausing, AnalysisRunCanceling:
		return false
	case AnalysisRunPaused, AnalysisRunInterrupted:
		return clearResults
	default:
		return true
	}
}

// Caller holds both lifecycle and controller locks.
func (s *Service) startAnalysisRunLocked(ctx context.Context, request AnalysisRunStartRequest) (*AnalysisRun, error) {
	c := s.analysisRun
	if err := s.restoreAnalysisRunLocked(); err != nil {
		return nil, err
	}
	if c.done != nil {
		return nil, errAnalysisRunBusy
	}
	if c.fault != nil {
		return cloneAnalysisRun(c.run), errAnalysisRunPersistence
	}
	if !canReplaceAnalysisRun(c.run, request.ClearResults) {
		return cloneAnalysisRun(c.run), errAnalysisRunBusy
	}
	preview, err := s.analysisPreviewLocked(ctx, AnalysisPreviewRequest{Models: request.Models, ProjectID: request.Identity.ProjectID, ProjectRevision: request.Identity.ProjectRevision, Scope: AnalysisRunScopeProject, RetryStaleFailed: request.RetryStaleFailed, StaleOnly: request.StaleOnly, StalePath: request.StalePath, RecoverIncomplete: request.RecoverIncomplete, Refresh: request.Refresh, ClearResults: request.ClearResults, Limits: request.Limits})
	if err != nil {
		return nil, err
	}
	if preview.Identity != request.Identity || preview.PreviewID != request.PreviewID {
		return nil, project.ErrRevisionConflict
	}
	if err := validateAnalysisConfirmations(preview, request.Confirmations); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisQueue(ctx, s.manager.Root(), preview, true); err != nil {
		return nil, err
	}
	if request.ClearResults {
		if err := s.clearAnalysisResultsLocked(ctx, s.manager.Root(), preview); err != nil {
			return nil, err
		}
	}
	run := newAnalysisRun(*preview)
	if err := s.recordSavedAnalysisStages(run); err != nil {
		return nil, err
	}
	c.root = s.manager.Root()
	c.run = run
	if err := s.saveAnalysisRunLocked(false); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	result := cloneAnalysisRun(c.run)
	if err := s.launchAnalysisRunLocked(preview, request.Confirmations); err != nil {
		return cloneAnalysisRun(c.run), err
	}
	return result, nil
}

// The caller holds jobLifecycleMu through its complete project read.
func (s *Service) currentAnalysisRunLocked(ctx context.Context) (*AnalysisRun, error) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.restoreAnalysisRunLocked(); err != nil && (c.run == nil || c.fault == nil) {
		return cloneAnalysisRun(c.run), err
	}
	if c.run == nil {
		return nil, nil
	}
	if err := s.refreshAnalysisRunFreshnessLocked(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return cloneAnalysisRun(c.run), err
	}
	s.updateAnalysisElapsedLocked()
	return cloneAnalysisRun(c.run), c.fault
}
