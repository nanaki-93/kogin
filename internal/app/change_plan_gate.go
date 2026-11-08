package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
)

// The plan gate: before the agents write any code for a task, the creation
// model investigates the code read-only and proposes a plan, and the run
// stops at awaiting_plan_review for the human to approve or redirect it.
// Approving starts the run again with the plan's hash; a redirect runs the
// investigation again with the human's note.

// errPlanReview ends a run whose investigation proposed a plan: not a
// failure, the run waits for the human.
var errPlanReview = errors.New("the proposed plan waits for your review")

// Plan gate modes, from the project profile or the request.
const (
	PlanGateAlways  = "always"
	PlanGateGrouped = "grouped"
	PlanGateOff     = "off"
)

var planGateModes = []string{PlanGateAlways, PlanGateGrouped, PlanGateOff}

// mainCheckoutRoot is the main checkout a task's project metadata lives in:
// a task worktree under .worktrees/ belongs to the checkout above it.
func mainCheckoutRoot(root string) string {
	if filepath.Base(filepath.Dir(root)) == ".worktrees" {
		return filepath.Dir(filepath.Dir(root))
	}
	return root
}

// workflowPlanMode is the gate mode a run uses: the request's, else the
// project profile's.
func workflowPlanMode(root string, request ChangeWorkflowRequest) string {
	switch request.Plan {
	case "on":
		return PlanGateAlways
	case "off":
		return PlanGateOff
	}
	return profileFor(mainCheckoutRoot(root)).PlanGate
}

// validateWorkflowPlanRequest bounds the request's plan fields.
func validateWorkflowPlanRequest(request ChangeWorkflowRequest) error {
	if request.Plan != "" && request.Plan != "on" && request.Plan != "off" {
		return fmt.Errorf("plan must be on or off")
	}
	if redirect := request.PlanRedirect; redirect != "" && !validInvestigationText(redirect, maxPlanRedirectBytes) {
		return fmt.Errorf("a plan redirect is at most %d bytes of text", maxPlanRedirectBytes)
	}
	return nil
}

// investigationNeeded reports whether this run proposes a plan before any
// code: the gate is on for the task, the run starts from create on a fresh
// proposal, and the creation model can run as an agent. An approved
// investigator plan that still matches the task needs no new investigation
// unless the human redirected it.
func investigationNeeded(session *ChangeSession, mode, redirect string, runtime modelRuntime) bool {
	if !ownsWorktree(session) || len(session.Changes) > 0 || len(session.AcceptanceCriteria) == 0 || session.Kind == "instructions" {
		return false
	}
	if runtime.client == nil || !runtime.client.SupportsAgentRuns() {
		return false
	}
	if redirect != "" {
		return true
	}
	switch mode {
	case PlanGateOff:
		return false
	case PlanGateGrouped:
		if len(session.Issues) < 2 {
			return false
		}
	}
	plan := session.Plan
	return plan == nil || plan.Source != planSourceInvestigator || plan.ApprovedHash != plan.Hash || plan.Hash != changePlanHash(session)
}

// investigateChangePlan proposes a plan for the run when the gate asks for
// one: it returns errPlanReview once the plan is stored, nil when no
// investigation is needed, and any other error as the run's failure.
func (s *Service) investigateChangePlan(ctx context.Context, job *changeWorkflowJob, message string) error {
	runtime := s.workflowStageRuntime(0, job.models.Create)
	session, redirect, err := s.beginInvestigation(ctx, job, runtime)
	if err != nil || session == nil {
		return err
	}
	prompt := investigationMessage(session, message, redirect, s.docsBrief(changeTargetPaths(session)))
	plan, tools, runErr := s.runInvestigation(ctx, job.root, session, runtime, prompt)
	if storeErr := s.settleInvestigation(ctx, job, runtime, plan, tools, runErr); storeErr != nil {
		return storeErr
	}
	if runErr != nil {
		return runErr
	}
	return errPlanReview
}

// beginInvestigation decides whether the run investigates and marks the plan
// stage running. It returns a nil session when no investigation is needed.
// The session it returns is a read-only view for the agent's prompt.
func (s *Service) beginInvestigation(ctx context.Context, job *changeWorkflowJob, runtime modelRuntime) (*ChangeSession, string, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return nil, "", err
	}
	if session.Workflow == nil || session.Workflow.ID != job.id {
		return nil, "", nil
	}
	redirect := strings.TrimSpace(job.request.PlanRedirect)
	if !investigationNeeded(session, workflowPlanMode(job.root, job.request), redirect, runtime) {
		return nil, "", nil
	}
	now := time.Now().UTC()
	session.Workflow.PlanStage = &ChangeWorkflowStage{Name: investigationStageName, Status: "running", StartedAt: now, Model: &runtime.effective}
	session.Workflow.UpdatedAt, session.UpdatedAt = now, now
	if err := storeChangeSession(job.root, session); err != nil {
		return nil, "", err
	}
	s.notifyWorkflow(ctx, session, investigationStageName)
	return session, redirect, nil
}

// settleInvestigation stores what the investigation ended with: the proposed
// plan on the task, with the plan stage completed, or the stage's failure.
func (s *Service) settleInvestigation(ctx context.Context, job *changeWorkflowJob, runtime modelRuntime, plan *ChangePlanInput, tools *ChangeStageTools, runErr error) error {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return err
	}
	if session.Workflow == nil || session.Workflow.ID != job.id || session.Workflow.PlanStage == nil {
		return nil
	}
	now := time.Now().UTC()
	stage := session.Workflow.PlanStage
	stage.FinishedAt, stage.Tools = now, tools
	chargeWorkflowPlanStage(ctx, job, stage)
	if runErr != nil {
		stage.Status, stage.Reason = "failed", clip(runErr.Error(), maxIssueReasonBytes)
	} else if err := capturePlanFromInvestigation(job.root, session, plan, runtime); err != nil {
		stage.Status, stage.Reason = "failed", clip(err.Error(), maxIssueReasonBytes)
		runErr = err
	} else {
		stage.Status = "completed"
		session.Messages = append(session.Messages, ChangeMessage{Role: "assistant", Content: investigationReport(plan, tools), CreatedAt: now})
	}
	session.Workflow.UpdatedAt, session.UpdatedAt = now, now
	if err := storeChangeSession(job.root, session); err != nil {
		return err
	}
	s.notifyWorkflow(ctx, session, investigationStageName)
	return runErr
}

// capturePlanFromInvestigation makes the proposed plan the task's plan: its
// cited files outside the task become read context, so the citations stay
// verifiable, and the plan waits unapproved.
func capturePlanFromInvestigation(root string, session *ChangeSession, plan *ChangePlanInput, runtime modelRuntime) error {
	if err := captureChangePlan(root, session, plan); err != nil {
		return fmt.Errorf("capture the proposed plan: %w", err)
	}
	session.Plan.Source = planSourceInvestigator
	session.Plan.Model = runtime.effective.Model
	session.Plan.InvestigatedAt = time.Now().UTC()
	session.Plan.ApprovedHash = ""
	return nil
}

// chargeWorkflowPlanStage adds what the run spent since its last charge to
// the plan stage. Caller owns changesMu, which also guards job.spent.
func chargeWorkflowPlanStage(ctx context.Context, job *changeWorkflowJob, stage *ChangeWorkflowStage) {
	budget := agentruntime.BudgetFromContext(ctx)
	if budget == nil || stage == nil {
		return
	}
	now := budget.Snapshot()
	delta := now.Minus(job.spent)
	job.spent = now
	if delta.ModelCalls == 0 && delta.InputTokens == 0 && delta.OutputTokens == 0 {
		return
	}
	if prior := stage.Usage; prior != nil {
		delta = prior.Plus(delta)
	}
	stage.Usage = &delta
}

// planReviewReason is why a run stopped at the plan gate.
func planReviewReason() string {
	return "The investigation proposed a plan. Review it on the task page, then approve it to start the agents, or redirect it with a note."
}
