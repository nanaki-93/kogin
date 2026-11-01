package app

import (
	"context"
	"errors"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) shouldAutoRepairWorkflow(ctx context.Context, job *changeWorkflowJob, runErr error) (bool, error) {
	if ctx.Err() != nil {
		return false, context.Cause(ctx)
	}
	if runErr != nil && !errors.Is(runErr, errWorkflowChecksFailed) {
		return false, nil
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return false, err
	}
	if session.Workflow == nil || session.Workflow.ID != job.id {
		return false, project.ErrRevisionConflict
	}
	if errors.Is(runErr, errWorkflowChecksFailed) {
		return workflowChecksRepairable(session.Checks), nil
	}
	return session.Workflow.Review != nil && session.Workflow.Review.ProposalHash == session.Hash && session.Workflow.Review.Verdict == "changes_requested" && specialistsComplete(session.Workflow, session.Hash), nil
}

// existingTestsFail reports a failure that predates the proposal and that
// Kasuri could not separate from new ones: repairing the proposal cannot
// fix it, so automatic repair would only spend budget. Failures it could
// separate never block (outcome known_failures).
func existingTestsFail(checks []CheckResult) bool {
	for _, check := range checks {
		if check.Outcome == "also_failed_before" && check.State == CheckFailed {
			return true
		}
	}
	return false
}

// A missing or interrupted check is not evidence that a model should change
// code, and neither is a test that already failed before the change.
func workflowChecksRepairable(checks []CheckResult) bool {
	if existingTestsFail(checks) {
		return false
	}
	failed := false
	for _, check := range checks {
		if !check.Required || check.State == CheckPassed {
			continue
		}
		if check.State != CheckFailed {
			return false
		}
		failed = true
	}
	return failed
}

func (s *Service) prepareWorkflowAutoRepair(ctx context.Context, job *changeWorkflowJob, baseMessage string, attempt int) (string, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return "", err
	}
	if session.Workflow == nil || session.Workflow.ID != job.id {
		return "", project.ErrRevisionConflict
	}
	if _, err := s.loadChangeGraphSnapshot(ctx, job, changeGraphSnapshot(session)); err != nil {
		return "", err
	}
	retryMsg := workflowRetryMessage(baseMessage, session.Workflow, session.Checks)
	session.Messages = stripWorkflowTestMessages(session.Messages)
	session.Workflow.AutoRepairAttempts = attempt
	chargeWorkflowStage(ctx, job, session.Workflow)
	session.Workflow.Review, session.Workflow.Override = nil, nil
	resetWorkflowSpecialists(session.Workflow)
	for i := 0; i < 3 && i < len(session.Workflow.Stages); i++ {
		session.Workflow.Stages[i].Status = "pending"
		session.Workflow.Stages[i].Reason = ""
		session.Workflow.Stages[i].StartedAt, session.Workflow.Stages[i].FinishedAt = time.Time{}, time.Time{}
	}
	session.PinnedTests = nil
	revokeApproval(session)
	session.Workflow.UpdatedAt, session.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := storeChangeSession(job.root, session); err != nil {
		return "", err
	}
	s.notifyWorkflow(ctx, session, "create")
	return retryMsg, nil
}
