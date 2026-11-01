package app

import (
	"context"
	"errors"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func captureAnalysisGraph(run *AnalysisRun) agentruntime.AnalysisGraphInput {
	id := run.Identity
	input := agentruntime.AnalysisGraphInput{Snapshot: agentruntime.AnalysisSnapshot{ProjectID: id.ProjectID, ProjectRevision: id.ProjectRevision, RunID: id.ID, Generation: id.Generation, QueueID: id.QueueID, PolicyFingerprint: id.PolicyFingerprint, ProviderFingerprint: id.ProviderFingerprint}}
	remaining := run.Plan.Limits.BatchFiles - run.WindowFilesCompleted
	for i, file := range run.Files {
		if remaining <= 0 {
			break
		}
		if analysisFileFinished(file) {
			continue
		}
		remaining--
		for j, stage := range file.Stages {
			if analysisStageNeedsWork(stage.Status) {
				input.Work = append(input.Work, agentruntime.AnalysisWork{Ordinal: len(input.Work), FileIndex: i, StageIndex: j, Stage: string(stage.Stage), FileHash: file.ContentHash})
			}
		}
	}
	return input
}

func (s *Service) runAnalysisFiles(ctx context.Context, identity AnalysisRunIdentity) {
	c := s.analysisRun
	c.mu.Lock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil || c.run.Status != AnalysisRunRunning {
		c.mu.Unlock()
		return
	}
	input := captureAnalysisGraph(c.run)
	c.mu.Unlock()
	if len(input.Work) == 0 {
		s.finishAnalysisGraph(ctx, identity, nil)
		return
	}
	_, err := agentruntime.RunAnalysisGraph(ctx, input, func(ctx context.Context, work agentruntime.AnalysisWork) (agentruntime.AnalysisOutcome, error) {
		return s.runAnalysisGraphNode(ctx, identity, work)
	})
	s.finishAnalysisGraph(ctx, identity, err)
}

func (s *Service) runAnalysisGraphNode(ctx context.Context, identity AnalysisRunIdentity, work agentruntime.AnalysisWork) (agentruntime.AnalysisOutcome, error) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.Identity != identity {
		return agentruntime.AnalysisOutcome{}, project.ErrRevisionConflict
	}
	if c.fault != nil {
		return agentruntime.AnalysisOutcome{}, errAnalysisRunPersistence
	}
	if c.run.Status != AnalysisRunRunning {
		return agentruntime.AnalysisOutcome{Status: "stopped"}, nil
	}
	if err := context.Cause(ctx); err != nil {
		return agentruntime.AnalysisOutcome{}, err
	}
	i, j, found := nextAnalysisStage(c.run)
	if !found || i != work.FileIndex || j != work.StageIndex || c.run.Files[i].ContentHash != work.FileHash || string(c.run.Files[i].Stages[j].Stage) != work.Stage {
		return agentruntime.AnalysisOutcome{}, project.ErrRevisionConflict
	}
	return s.executeAnalysisGraphStageLocked(ctx, identity, i, j)
}

// The controller owns admission and publication. ADK receives the settled stage
// outcome only after the host has saved its counts and cumulative attempts.
func (s *Service) executeAnalysisGraphStageLocked(ctx context.Context, identity AnalysisRunIdentity, i, j int) (agentruntime.AnalysisOutcome, error) {
	c := s.analysisRun
	stage := &c.run.Files[i].Stages[j]
	if stage.Stage != AnalysisStageSecurityRules && stage.Attempts >= c.run.Plan.Limits.MaxAttemptsPerStage && !c.admission.Files[i].Stages[j].Cached {
		err := s.failExhaustedAnalysisStageLocked(i, j)
		return agentruntime.AnalysisOutcome{Status: string(stage.Status), Continue: err == nil}, err
	}
	stage.Status, stage.Reason = AnalysisStageRunning, ""
	if err := s.saveAnalysisRunLocked(false); err != nil {
		return agentruntime.AnalysisOutcome{}, err
	}
	request := c.stageRequest(i, j)
	authority := s.analysisRunAuthority(i, j)
	c.mu.Unlock()
	result, err := s.analyzeFileStage(ctx, request, authority)
	c.mu.Lock()
	keepRunning := s.recordAnalysisStageLocked(ctx, identity, i, j, result, err)
	if err != nil {
		return agentruntime.AnalysisOutcome{}, err
	}
	if c.fault != nil {
		return agentruntime.AnalysisOutcome{}, c.fault
	}
	if !keepRunning {
		return agentruntime.AnalysisOutcome{Status: "stopped"}, nil
	}
	return agentruntime.AnalysisOutcome{Status: string(c.run.Files[i].Stages[j].Status), Continue: c.run.Status == AnalysisRunRunning}, nil
}

func (s *Service) finishAnalysisGraph(ctx context.Context, identity AnalysisRunIdentity, err error) {
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil || c.run.Status != AnalysisRunRunning {
		return
	}
	if err != nil {
		s.failAnalysisGraphLocked(ctx, err)
		return
	}
	if _, _, pending := nextAnalysisStage(c.run); pending && c.run.WindowFilesCompleted >= c.run.Plan.Limits.BatchFiles {
		c.fileStopReason = "The batch limit was reached; resume to continue pending files."
		c.run.Reason = c.fileStopReason
		_ = s.saveAnalysisRunLocked(false) // A failure is retained by the controller.
	}
}

func (s *Service) failAnalysisGraphLocked(ctx context.Context, err error) {
	c := s.analysisRun
	switch {
	case errors.Is(err, project.ErrRevisionConflict):
		s.staleAnalysisRunLocked()
	case ctx.Err() != nil && !errors.Is(err, agentstate.ErrPersistence) && !errors.Is(err, agentruntime.ErrPolicyDenied):
		c.fileStopReason = "The file dispatch allowance ended; preview and resume the remaining work."
		c.run.Reason = "File analysis reached its allowance. Feature discovery continues independently."
	default:
		c.run.Status = AnalysisRunInterrupted
		c.run.Reason = "Analysis stopped before the stage completed; review and resume."
		interruptAnalysisStages(c.run, AnalysisStageInterrupted)
		if c.cancel != nil {
			c.cancel()
		}
	}
	_ = s.saveAnalysisRunLocked(false)
}
