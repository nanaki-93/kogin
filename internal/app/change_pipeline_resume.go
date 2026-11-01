package app

import (
	"fmt"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
)

// workflowStart is where an admitted run enters the change graph and how many
// leading model stages (create, then test) it keeps from the previous run.
type workflowStart struct {
	node agentruntime.ChangeNode
	kept int
}

var (
	startCreate = workflowStart{node: agentruntime.ChangeCreate}
	startTests  = workflowStart{node: agentruntime.ChangeTests, kept: 1}
	// startChecks keeps the current proposal, implementation and tests alike:
	// its test stage runs the checks without a model, then the reviewers run.
	startChecks = workflowStart{node: agentruntime.ChangeVerify, kept: 1}
	startReview = workflowStart{node: agentruntime.ChangeReview, kept: 2}
)

// firstModelStage is the first model stage the run may call: 0 create, 1 test,
// 2 review. A checks start calls no model before the review.
func (start workflowStart) firstModelStage() int {
	switch start.node {
	case agentruntime.ChangeCreate:
		return 0
	case agentruntime.ChangeTests:
		return 1
	default:
		return 2
	}
}

// Resuming skips only stages whose output still belongs to this exact proposal.
func (s *Service) workflowStartFor(session *ChangeSession, from string) (workflowStart, error) {
	switch from {
	case "", "create":
		return startCreate, nil
	case "test":
		if len(session.Changes) > 0 {
			return startTests, nil
		}
	case "checks":
		if len(session.Changes) > 0 {
			return startChecks, nil
		}
	case "review":
		if s.hasLivePassingChecks(session) {
			return startReview, nil
		}
	default:
		return workflowStart{}, fmt.Errorf("resume_from must be create, test, checks or review")
	}
	return workflowStart{}, fmt.Errorf("cannot resume %s without current proposal evidence; run the preceding stage", from)
}

func completeWorkflowStage(stage *ChangeWorkflowStage, now time.Time) {
	stage.Status, stage.Reason = "completed", ""
	if stage.FinishedAt.IsZero() {
		stage.FinishedAt = now
	}
}

func finishWorkflowStageTiming(w *ChangeWorkflow, now time.Time) {
	for i := range w.Stages {
		st := &w.Stages[i]
		if !st.StartedAt.IsZero() && st.FinishedAt.IsZero() {
			st.FinishedAt = now
		}
	}
}

func (s *Service) hasLivePassingChecks(session *ChangeSession) bool {
	authority := s.changeAuthority[session.ProjectID+"/"+session.ID]
	return len(session.Checks) > 0 && requiredChecksPassed(session.Checks) && (authority == "checks:"+session.Hash || authority == "review:"+session.Hash)
}

func (s *Service) prepareWorkflowRun(session *ChangeSession, request ChangeWorkflowRequest, start workflowStart, cleanedMessages []ChangeMessage) *ChangeWorkflow {
	now := time.Now().UTC()
	session.Revision++ // Invalidates operations admitted before this run.
	attempt := 1
	if session.Workflow != nil {
		attempt += session.Workflow.Attempt + session.Workflow.AutoRepairAttempts
	}
	w := &ChangeWorkflow{Attempt: attempt, ResumeFrom: request.ResumeFrom, ID: fmt.Sprintf("%s-%d", session.ID, session.Revision), Status: "running", Models: request.Models, AutoRepair: request.AutoRepair, StartedAt: now, UpdatedAt: now}
	w.StreamOutput = request.StreamOutput
	for i, profile := range request.Models.selections() {
		runtime := s.workflowStageRuntime(i, profile)
		w.Stages = append(w.Stages, ChangeWorkflowStage{Name: []string{"create", "test", "review"}[i], Status: "pending", Model: &runtime.effective})
	}
	w.Stages = append(w.Stages, ChangeWorkflowStage{Name: "human_review", Status: "blocked"})
	w.Specialists = s.prepareWorkflowSpecialists(session, request.Models.Review)
	for i := 0; i < start.kept; i++ {
		if previous, ok := savedWorkflowStage(session.Workflow, w.Stages[i].Name); ok {
			w.Stages[i] = previous
		}
		w.Stages[i].Status, w.Stages[i].Reason = "completed", ""
	}
	// A review start reuses the passing checks; create and test starts keep a
	// failure as evidence. A checks start runs them again straight away.
	retainedChecks := []CheckResult{}
	if len(session.Changes) > 0 && (start == startReview || start != startChecks && !requiredChecksPassed(session.Checks)) {
		retainedChecks = session.Checks
	}
	session.Workflow, session.Checks, session.PinnedTests = w, retainedChecks, nil
	revokeApproval(session)
	session.Messages = cleanedMessages
	session.UpdatedAt = now
	return w
}

func validateResumedModels(session *ChangeSession, models ChangeWorkflowModels, kept int) error {
	if kept < 0 || kept > 2 {
		return fmt.Errorf("invalid checked-change start stage")
	}
	if session.Workflow == nil {
		return nil
	}
	profiles := models.selections()
	for i := 0; i < kept; i++ {
		name := []string{"create", "test"}[i]
		stage, ok := savedWorkflowStage(session.Workflow, name)
		if !ok || stage.Model == nil || stage.Model.Profile != profiles[i] {
			return fmt.Errorf("retry from %s to change its model, or retain its previously selected model", name)
		}
	}
	return nil
}

func savedWorkflowStage(workflow *ChangeWorkflow, name string) (ChangeWorkflowStage, bool) {
	var result ChangeWorkflowStage
	found := false
	if workflow == nil {
		return result, false
	}
	for _, stage := range workflow.Stages {
		if stage.Name != name {
			continue
		}
		if found {
			return ChangeWorkflowStage{}, false
		}
		result, found = stage, true
	}
	return result, found
}
