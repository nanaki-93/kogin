package app

import (
	"context"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// Reindex owns jobLifecycleMu. A progress write fault remains available through
// run reads and controls without hiding the successfully refreshed index.
func (s *Service) refreshAnalysisRunAfterReindex() {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil && c.root == s.manager.Root() {
		if err := s.validateAnalysisQueue(context.Background(), c.root, &c.run.Plan, true); err != nil {
			if c.run.Status != AnalysisRunStale {
				s.staleAnalysisRunLocked()
				_ = s.saveAnalysisRunLocked(false)
			}
			return
		}
		_ = s.refreshAnalysisRunFreshnessLocked(context.Background())
	}
}

// The caller holds both lifecycle and controller locks.
func (s *Service) refreshAnalysisRunFreshnessLocked(ctx context.Context) error {
	c := s.analysisRun
	stale := c.run.Status == AnalysisRunStale
	if stale && (c.fault != nil || !analysisRunHasTerminalEvidence(c.run)) {
		return nil
	}
	if err := s.validateAnalysisRunFreshness(ctx, c.root, c.run); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !stale {
			s.staleAnalysisRunLocked()
			return s.saveAnalysisRunLocked(false)
		}
		return nil
	}
	if stale {
		// Older versions marked finished runs stale on restore/reindex even
		// without source changes. Recover only terminal evidence whose entire
		// captured queue still matches; incomplete work keeps its lifecycle.
		c.run.Status = analysisFinishedStatus(c.run)
		c.run.Reason = ""
		refreshAnalysisSections(c.run)
	}
	return nil
}

func (s *Service) validateAnalysisRunFreshness(ctx context.Context, root string, run *AnalysisRun) error {
	switch run.Status {
	case AnalysisRunCompleted, AnalysisRunCompletedEmpty, AnalysisRunPartial, AnalysisRunFailed, AnalysisRunUnavailable, AnalysisRunStale:
		// Finished evidence depends on analyzed source, policy and providers.
		// The admission inventory and execution workspace also contain ignored
		// runtime files; changes there must not invalidate saved results.
		if err := s.validateAnalysisQueue(ctx, root, &run.Plan, false); err != nil {
			return err
		}
		index, err := s.manager.Index()
		if err != nil {
			return err
		}
		return project.VerifyProjectSources(ctx, root, index)
	default:
		if s.analysisRun.done != nil {
			return s.validateAnalysisQueue(ctx, root, &run.Plan, false)
		}
		return s.validateAnalysisQueue(ctx, root, &run.Plan, true)
	}
}

func analysisRunHasTerminalEvidence(run *AnalysisRun) bool {
	evidence := false
	if run.Features != nil {
		switch run.Features.Status {
		case AnalysisStageCompleted, AnalysisStageCompletedEmpty:
			evidence = true
		case AnalysisStageFailed, AnalysisStageUnavailable:
		default:
			return false
		}
	}
	for i, file := range run.Files {
		for j, stage := range file.Stages {
			switch stage.Status {
			case AnalysisStageCompleted, AnalysisStageCompletedEmpty, AnalysisStagePartial:
				evidence = true
			case AnalysisStageFailed, AnalysisStageUnavailable:
			case AnalysisStageSkipped:
				if plan := run.Plan.Files[i].Stages[j]; plan.Eligible && plan.Carried == "" {
					return false
				}
			case AnalysisStageCanceled, AnalysisStageStale:
				if run.Plan.Files[i].Stages[j].Carried == "" {
					return false
				}
			default:
				return false
			}
		}
	}
	return evidence
}
