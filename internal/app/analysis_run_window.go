package app

import (
	"context"
	"errors"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) launchAnalysisRunLocked(preview *AnalysisRunPreview, confirmations AnalysisRunConfirmations) error {
	c := s.analysisRun
	c.windowBudget = time.Duration(c.run.Plan.Limits.BudgetSeconds) * time.Second
	ctx, cancel, err := s.analysisAgentContext(context.Background(), preview)
	if err != nil {
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis stopped before the stage completed; review and resume."
		return errors.Join(err, s.saveAnalysisRunLocked(false))
	}
	c.cancel = cancel
	c.done = make(chan struct{})
	c.admission = preview
	c.confirmations = AnalysisRunConfirmations{SecurityReview: confirmations.SecurityReview, ProviderIDs: append([]string(nil), confirmations.ProviderIDs...)}
	c.windowStart = time.Now()
	c.elapsedBase = c.run.ElapsedSeconds
	c.fileStopReason = ""
	go s.runAnalysisWindow(ctx, c.run.Identity, c.done)
	return nil
}

func (s *Service) runAnalysisWindow(ctx context.Context, identity AnalysisRunIdentity, done chan struct{}) {
	c := s.analysisRun
	defer s.closeAnalysisWindow(done)
	defer s.analysisRun.cancel()
	c.mu.Lock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		c.mu.Unlock()
		return
	}
	if c.run.Status != AnalysisRunQueued {
		s.finishAnalysisWindowLocked(ctx)
		c.mu.Unlock()
		return
	}
	c.run.Status = AnalysisRunRunning
	budget := c.windowBudget
	c.mu.Unlock()
	fileContext, cancelFiles := context.WithTimeout(ctx, budget)
	s.runAnalysisFiles(fileContext, identity)
	cancelFiles()
	s.runChecksAfterRun(ctx, identity)
	s.refreshDocsAfterRun(ctx, identity)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		return
	}
	if c.run.Status != AnalysisRunRunning || ctx.Err() != nil {
		s.finishAnalysisWindowLocked(ctx)
		return
	}
	if c.fileStopReason != "" {
		c.run.Status, c.run.Reason = AnalysisRunPaused, c.fileStopReason
		_ = s.saveAnalysisRunLocked(false)
		return
	}
	s.finishCompletedAnalysisRunLocked(ctx)
}

func (s *Service) finishAnalysisWindowLocked(ctx context.Context) bool {
	c := s.analysisRun
	switch c.run.Status {
	case AnalysisRunStale, AnalysisRunCanceled:
		return true
	case AnalysisRunCanceling:
		c.run.Status = AnalysisRunCanceled
		interruptAnalysisStages(c.run, AnalysisStageCanceled)
	case AnalysisRunPausing:
		c.run.Status = AnalysisRunPaused
	case AnalysisRunRunning:
		if ctx.Err() == nil {
			return false
		}
		if errors.Is(context.Cause(ctx), context.Canceled) {
			c.run.Status = AnalysisRunCanceled
			c.run.Reason = "Analysis canceled by the user."
			interruptAnalysisStages(c.run, AnalysisStageCanceled)
		} else {
			c.run.Status = AnalysisRunPaused
			c.run.Reason = "The dispatch allowance ended; preview and resume the remaining work."
			interruptAnalysisStages(c.run, AnalysisStageInterrupted)
		}
	default:
		return true
	}
	_ = s.saveAnalysisRunLocked(false)
	return true
}

func (s *Service) updateAnalysisElapsedLocked() {
	c := s.analysisRun
	if c.run == nil || c.windowStart.IsZero() {
		return
	}
	active := time.Since(c.windowStart)
	active = min(active, c.windowBudget)
	elapsed := int64(active / time.Second)
	c.run.WindowElapsedSeconds = elapsed
	c.run.ElapsedSeconds = c.elapsedBase + elapsed
}

const (
	// analysisRunStaleReason says why a run's results no longer match: the
	// files, the context policy or the models changed.
	analysisRunStaleReason = "Files, settings or models changed since this run. Analyze changes to bring the results up to date."
	// legacyAnalysisRunStaleReason is how saved runs said it before; it is
	// read as analysisRunStaleReason.
	legacyAnalysisRunStaleReason = "Project source, policy or provider identity changed; start a new analysis."
)

func (s *Service) staleAnalysisRunLocked() {
	c := s.analysisRun
	c.run.Status = AnalysisRunStale
	c.run.Reason = analysisRunStaleReason
	interruptAnalysisStages(c.run, AnalysisStageStale)
	if c.cancel != nil {
		c.cancel()
	}
}

func (s *Service) closeAnalysisWindow(done chan struct{}) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == done {
		if c.cancel != nil {
			c.cancel()
		}
		c.cancel = nil
		c.done = nil
		c.admission = nil
		c.confirmations = AnalysisRunConfirmations{}
		c.windowStart = time.Time{}
	}
	close(done)
}

func (s *Service) finishCompletedAnalysisRunLocked(ctx context.Context) {
	c := s.analysisRun
	if err := s.validateAnalysisCompletedRun(ctx, c.root, &c.run.Plan); err != nil {
		s.staleAnalysisRunLocked()
	} else {
		refreshAnalysisSections(c.run)
		c.run.Status = analysisFinishedStatus(c.run)
	}
	_ = s.saveAnalysisRunLocked(false) // Failure is retained and stops this worker.
}

func (s *Service) validateAnalysisCompletedRun(ctx context.Context, root string, plan *AnalysisRunPreview) error {
	if err := s.validateAnalysisQueue(ctx, root, plan, false); err != nil {
		return err
	}
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	return project.VerifyProjectSources(ctx, root, index)
}
