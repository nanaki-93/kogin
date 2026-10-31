package app

import (
	"context"
	"errors"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (c *analysisRunController) stageRequest(fileIndex, stageIndex int) analysisFileStageRequest {
	file := c.run.Files[fileIndex]
	stage := file.Stages[stageIndex]
	plan := c.admission.Files[fileIndex].Stages[stageIndex]
	return analysisFileStageRequest{
		Run: c.run.Identity, File: file.AnalysisFileIdentity, Stage: stage.Stage, Models: cloneAnalysisModels(c.run.Plan.Models),
		Refresh:               c.run.Plan.Refresh || ((c.run.Plan.RetryStaleFailed || c.run.Plan.RecoverIncomplete || c.run.Plan.StaleOnly) && !plan.Cached),
		RemainingAttempts:     min(plan.MaxModelRequests, c.run.Plan.Limits.MaxAttemptsPerStage-stage.Attempts),
		ConfirmRemoteProvider: analysisProviderConfirmed(c.confirmations, plan.ProviderID),
		SecurityReview:        c.confirmations.SecurityReview,
	}
}

func (s *Service) analysisRunAuthority(fileIndex, stageIndex int) analysisFileStageAuthority {
	c := s.analysisRun

	return analysisFileStageAuthority{
		Check: func(ctx context.Context, identity AnalysisRunIdentity) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.run == nil || c.run.Identity != identity {
				return project.ErrRevisionConflict
			}
			return s.validateAnalysisStageAuthorityLocked(ctx, fileIndex, stageIndex, identity, c.run.Files[fileIndex].AnalysisFileIdentity, c.run.Files[fileIndex].Stages[stageIndex].Stage)
		},
		BeforeAttempt: func(ctx context.Context, identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if err := s.validateAnalysisStageAuthorityLocked(ctx, fileIndex, stageIndex, identity, file, stage); err != nil {
				return err
			}
			progress := &c.run.Files[fileIndex].Stages[stageIndex]
			if progress.Attempts >= c.run.Plan.Limits.MaxAttemptsPerStage {
				return errAnalysisAttemptBudget
			}
			progress.Attempts++
			return s.saveAnalysisRunLocked(false)
		},
		Publish: func(identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if err := s.validateAnalysisStageAuthorityLocked(context.Background(), fileIndex, stageIndex, identity, file, stage); err != nil {
				return err
			}
			if err := write(); err != nil {
				if errors.Is(err, project.ErrRevisionConflict) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				s.failAnalysisPersistenceLocked()
				return errAnalysisRunPersistence
			}
			return nil
		},
	}
}

func (s *Service) recordAnalysisStageLocked(ctx context.Context, identity AnalysisRunIdentity, fileIndex, stageIndex int, result analysisFileStageResult, err error) bool {
	c := s.analysisRun
	if c.run == nil || c.run.Identity != identity || c.fault != nil {
		return false
	}
	if c.run.Status == AnalysisRunStale || c.run.Status == AnalysisRunCanceled || c.run.Status == AnalysisRunCanceling || c.run.Status == AnalysisRunInterrupted {
		s.finishAnalysisWindowLocked(ctx)
		return false
	}
	if err != nil {
		s.interruptAnalysisStageLocked(ctx, fileIndex, stageIndex, err)
		return false
	}

	recordAnalysisResult(c.run, fileIndex, stageIndex, result)
	if analysisFileFinished(c.run.Files[fileIndex]) {
		c.run.WindowFilesCompleted++
	}
	if err := s.saveAnalysisRunLocked(false); err != nil {
		return false
	}
	return true
}

func (s *Service) interruptAnalysisStageLocked(ctx context.Context, fileIndex, stageIndex int, err error) {
	c := s.analysisRun
	current := &c.run.Files[fileIndex].Stages[stageIndex]
	current.Status = AnalysisStageInterrupted
	current.Reason = "Analysis stage could not complete."
	if errors.Is(context.Cause(ctx), context.Canceled) {
		current.Status = AnalysisStageCanceled
		current.Reason = "Analysis canceled by the user."
	}
	if errors.Is(err, project.ErrRevisionConflict) {
		s.staleAnalysisRunLocked()
	} else if c.run.Status == AnalysisRunPausing || ctx.Err() != nil || errors.Is(err, errAnalysisAttemptBudget) {
		c.fileStopReason = "The file dispatch allowance ended; preview and resume the remaining work."
		if c.run.Status == AnalysisRunRunning {
			c.run.Reason = "File analysis reached its allowance. Feature discovery continues independently."
		}
	} else {
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis stopped before the stage completed; review and resume."
		if c.cancel != nil {
			c.cancel()
		}
	}
	_ = s.saveAnalysisRunLocked(false)
}

func analysisProviderConfirmed(confirmations AnalysisRunConfirmations, providerID string) bool {
	for _, id := range confirmations.ProviderIDs {
		if id == providerID {
			return true
		}
	}
	return false
}

func (s *Service) validateAnalysisStageAuthorityLocked(ctx context.Context, fileIndex, stageIndex int, identity AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
	c := s.analysisRun
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.run == nil || c.run.Identity != identity || c.run.Files[fileIndex].AnalysisFileIdentity != file || c.run.Files[fileIndex].Stages[stageIndex].Stage != stage {
		return project.ErrRevisionConflict
	}
	if c.fault != nil {
		return errAnalysisRunPersistence
	}
	if c.run.Status != AnalysisRunRunning && c.run.Status != AnalysisRunPausing {
		return project.ErrRevisionConflict
	}
	return s.validateAnalysisQueue(ctx, c.root, &c.run.Plan, false)
}

func (s *Service) failExhaustedAnalysisStageLocked(fileIndex, stageIndex int) error {
	c := s.analysisRun
	stage := &c.run.Files[fileIndex].Stages[stageIndex]
	stage.Status = AnalysisStageFailed
	stage.Reason = "The stage exhausted its total attempt allowance."
	if analysisFileFinished(c.run.Files[fileIndex]) {
		c.run.WindowFilesCompleted++
	}
	return s.saveAnalysisRunLocked(false)
}
