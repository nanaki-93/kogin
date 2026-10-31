package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) CancelChangeWorkflow(ctx context.Context, id string, request ChangeWorkflowControl) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if request.ProjectID != index.ProjectID || request.ProjectRevision != index.ProjectRevision {
		return nil, project.ErrRevisionConflict
	}
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, err
	}
	if session.ProjectID != request.ProjectID || session.Workflow == nil || session.Workflow.ID != request.WorkflowID {
		return nil, project.ErrRevisionConflict
	}
	if !isChangePipelineActive(session.Workflow) {
		return s.cancelWaitingInput(ctx, root, session)
	}
	s.changeWorkflow.mu.Lock()
	job := s.changeWorkflow.getJob(id)
	active := job != nil && !job.finished && job.root == root && job.id == request.WorkflowID
	if active {
		job.cancel()
	}
	s.changeWorkflow.mu.Unlock()
	if !active {
		return nil, fmt.Errorf("restore this interrupted workflow before running again")
	}
	session.Workflow.Status = "canceling"
	session.Workflow.UpdatedAt = time.Now().UTC()
	session.UpdatedAt = session.Workflow.UpdatedAt
	return session, storeChangeSession(root, session)
}

// CancelChangeWorkflows revokes dispatch on project replacement, reindex and
// daemon shutdown. The worker owns cleanup and saves a terminal result.
// CancelChangeWorkflows stops every running workflow because the daemon is
// shutting down; each stopped task records that reason.
func (s *Service) CancelChangeWorkflows() {
	s.cancelChangeWorkflowsBecause(stopReasonShutdown)
}

// cancelChangeWorkflowsBecause stops running workflows and records why, so a
// task the daemon stopped does not read like one the user canceled.
func (s *Service) cancelChangeWorkflowsBecause(reason string) {
	s.changeWorkflow.mu.Lock()
	defer s.changeWorkflow.mu.Unlock()
	for _, job := range s.changeWorkflow.allJobs() {
		if !job.finished {
			job.stop(reason)
		}
	}
}

// stopAffectedChangeWorkflows stops the running workflows whose tasks a source
// change made stale, naming what changed, and lets the others continue. A
// path-scoped task is affected only when its captured sources, freshness
// scope or branch changed; an older task by any change.
func (s *Service) stopAffectedChangeWorkflows(reason string) {
	s.changeWorkflow.mu.Lock()
	var running []*changeWorkflowJob
	for _, job := range s.changeWorkflow.allJobs() {
		if !job.finished {
			running = append(running, job)
		}
	}
	s.changeWorkflow.mu.Unlock()
	for _, job := range running {
		affected, detail := s.changeWorkflowAffected(job)
		if !affected {
			continue
		}
		cause := reason
		if detail != "" {
			cause += " (" + detail + ")"
		}
		s.changeWorkflow.mu.Lock()
		if !job.finished {
			job.stop(cause)
		}
		s.changeWorkflow.mu.Unlock()
	}
}

func (s *Service) changeWorkflowAffected(job *changeWorkflowJob) (bool, string) {
	session, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return true, ""
	}
	if err := s.verifyChangeCurrent(context.Background(), job.root, session); err != nil {
		return true, staleReason(err)
	}
	return false, ""
}

// ShutdownChangeWorkflows waits for process/workspace cleanup after revoking
// active change workflows, analysis runs, and project scans. Lifecycle mutations
// use cancellation alone to avoid lock waits.
func (s *Service) ShutdownChangeWorkflows(ctx context.Context) error {
	children := s.worktreeServices()
	for _, child := range children {
		child.invalidateJobsForProjectChange(stopReasonShutdown)
	}
	for _, child := range children {
		if err := child.ShutdownChangeWorkflows(ctx); err != nil {
			return err
		}
	}
	s.invalidateJobsForProjectChange(stopReasonShutdown)
	if err := s.waitChangeWorkflowDone(ctx); err != nil {
		return err
	}
	if err := s.waitAnalysisRunDone(ctx); err != nil {
		return err
	}
	return s.waitDocsRefreshDone(ctx)
}

func (s *Service) waitChangeWorkflowDone(ctx context.Context) error {
	s.changeWorkflow.mu.Lock()
	jobs := s.changeWorkflow.allJobs()
	for _, job := range jobs {
		job.cancel()
	}
	s.changeWorkflow.mu.Unlock()
	for _, job := range jobs {
		select {
		case <-job.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) waitAnalysisRunDone(ctx context.Context) error {
	if s.analysisRun == nil {
		return nil
	}
	s.analysisRun.mu.Lock()
	done := s.analysisRun.done
	if s.analysisRun.cancel != nil {
		s.analysisRun.cancel()
	}
	s.analysisRun.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) restoreChangeWorkflow(session *ChangeSession) {
	w := session.Workflow
	if w == nil {
		return
	}
	s.changeWorkflow.mu.Lock()
	job := s.changeWorkflow.getJob(session.ID)
	// The snapshot can precede a worker's final atomic save. A known finished
	// worker is not an orphan: preserve this observation until the next read.
	// Save failures still override it, and a restarted service has no owner.
	owned := job != nil && job.id == w.ID && job.root == s.manager.Root()
	failure := ""
	if owned {
		failure = job.failure
	}
	s.changeWorkflow.mu.Unlock()
	if failure != "" {
		w.Status, w.Reason = "failed", failure
	} else if isChangePipelineActive(w) && !owned {
		w.Status, w.Reason = "interrupted", "The workflow stopped before completion. Restore the conversation to run again."
	}
	if session.Freshness == "stale" {
		w.Status, w.Reason = "stale", staleWorkflowReason(session.StaleReason)
	}
	if w.Status == "interrupted" || w.Status == "failed" || w.Status == "stale" {
		stopChangeWorkflowStages(w)
	}
	settleWorkflowPause(w)
}
