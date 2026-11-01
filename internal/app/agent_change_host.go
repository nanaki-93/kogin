package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func changeGraphSnapshot(session *ChangeSession) agentruntime.ChangeSnapshot {
	return agentruntime.ChangeSnapshot{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, SessionID: session.ID, WorkflowID: session.Workflow.ID, Revision: session.Revision, Hash: session.Hash}
}

func graphChangeIdentity(snapshot agentruntime.ChangeSnapshot) ChangeIdentity {
	return ChangeIdentity{ProjectID: snapshot.ProjectID, ProjectRevision: snapshot.ProjectRevision, Revision: snapshot.Revision, Hash: snapshot.Hash}
}

func (s *Service) executeChangeGraph(ctx context.Context, job *changeWorkflowJob, start agentruntime.ChangeNode, message string) error {
	if !agentruntime.ValidChangeStart(start) {
		return fmt.Errorf("invalid checked-change start stage")
	}
	s.changesMu.Lock()
	session, err := loadChangeSession(job.root, job.sessionID)
	if err == nil && (session.Workflow == nil || session.Workflow.ID != job.id) {
		err = project.ErrRevisionConflict
	}
	var input agentruntime.ChangeGraphInput
	if err == nil {
		input = agentruntime.ChangeGraphInput{Snapshot: changeGraphSnapshot(session), Start: start, Models: [3]string{job.models.Create, job.models.Test, job.models.Review}}
	}
	s.changesMu.Unlock()
	if err != nil {
		return err
	}
	_, err = agentruntime.RunChangeGraph(ctx, input, func(ctx context.Context, input agentruntime.ChangeNodeInput) (agentruntime.ChangeNodeResult, error) {
		return s.runChangeGraphNode(ctx, job, input, message)
	})
	return err
}

func (s *Service) runChangeGraphNode(ctx context.Context, job *changeWorkflowJob, input agentruntime.ChangeNodeInput, message string) (agentruntime.ChangeNodeResult, error) {
	if input.Snapshot.SessionID != job.sessionID || input.Snapshot.WorkflowID != job.id {
		return agentruntime.ChangeNodeResult{}, project.ErrRevisionConflict
	}
	switch input.Node {
	case agentruntime.ChangeCreate:
		return s.runChangeGraphModel(ctx, job, input, 0, message)
	case agentruntime.ChangeTests:
		return s.runChangeGraphModel(ctx, job, input, 1, message)
	case agentruntime.ChangeReview:
		return s.runChangeGraphModel(ctx, job, input, 2, message)
	case agentruntime.ChangeVerify:
		return s.runChangeGraphVerification(ctx, job, input)
	case agentruntime.ChangeHumanReview:
		return s.changeGraphHandoff(ctx, job, input)
	default:
		return agentruntime.ChangeNodeResult{}, fmt.Errorf("unknown checked-change node")
	}
}

func (s *Service) runChangeGraphModel(ctx context.Context, job *changeWorkflowJob, input agentruntime.ChangeNodeInput, stage int, message string) (agentruntime.ChangeNodeResult, error) {
	session, err := s.beginChangeWorkflowStage(ctx, job, stage, graphChangeIdentity(input.Snapshot))
	if err != nil {
		return agentruntime.ChangeNodeResult{}, err
	}
	runtime := s.workflowStageRuntime(stage, job.models.selections()[stage])
	session, err = s.runWorkflowModelStage(ctx, job, session, runtime, stage, message)
	if err != nil {
		return agentruntime.ChangeNodeResult{}, err
	}
	result := agentruntime.ChangeNodeResult{Node: input.Node, Snapshot: changeGraphSnapshot(session), Status: "completed"}
	if stage == 2 {
		s.changesMu.Lock()
		defer s.changesMu.Unlock()
		current, err := s.loadChangeGraphSnapshot(ctx, job, input.Snapshot)
		if err != nil {
			return agentruntime.ChangeNodeResult{}, err
		}
		if current.Workflow.Review == nil || current.Workflow.Review.ProposalHash != current.Hash {
			return agentruntime.ChangeNodeResult{}, fmt.Errorf("critic did not publish a review for this proposal")
		}
		result.Verdict = current.Workflow.Review.Verdict
	}
	return result, nil
}

// Caller owns changesMu. Every node checks both domain admission and the exact
// predecessor output; a restored graph event is never sufficient authority.
func (s *Service) loadChangeGraphSnapshot(ctx context.Context, job *changeWorkflowJob, snapshot agentruntime.ChangeSnapshot) (*ChangeSession, error) {
	session, root, err := s.loadChangeForAction(ctx, job.sessionID, graphChangeIdentity(snapshot))
	if err != nil {
		return nil, err
	}
	if root != job.root || session.Workflow == nil || session.Workflow.ID != job.id || session.Workflow.Models != job.models || !slices.Equal(workflowSpecialistIDs(session.Workflow), job.specialists) {
		return nil, project.ErrRevisionConflict
	}
	if err := s.requireChangeExecutionTrust(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *Service) runChangeGraphVerification(ctx context.Context, job *changeWorkflowJob, input agentruntime.ChangeNodeInput) (agentruntime.ChangeNodeResult, error) {
	s.changesMu.Lock()
	session, err := s.loadChangeGraphSnapshot(ctx, job, input.Snapshot)
	s.changesMu.Unlock()
	if err != nil {
		return agentruntime.ChangeNodeResult{}, err
	}
	if session.Workflow.Stages[1].Status == "pending" {
		// A checks start: the test stage runs the checks on the kept proposal.
		if _, err := s.beginChangeWorkflowStage(ctx, job, 1, graphChangeIdentity(input.Snapshot)); err != nil {
			return agentruntime.ChangeNodeResult{}, err
		}
	}
	session, err = s.CheckChange(ctx, job.sessionID, ChangeCheckRequest{ChangeIdentity: graphChangeIdentity(input.Snapshot), CheckOptions: CheckOptions{RunTests: true, RunLint: true}})
	if err != nil {
		return agentruntime.ChangeNodeResult{}, err
	}
	if !requiredChecksPassed(session.Checks) {
		return agentruntime.ChangeNodeResult{}, errWorkflowChecksFailed
	}
	return agentruntime.ChangeNodeResult{Node: input.Node, Snapshot: changeGraphSnapshot(session), Status: "completed"}, nil
}

func (s *Service) changeGraphHandoff(ctx context.Context, job *changeWorkflowJob, input agentruntime.ChangeNodeInput) (agentruntime.ChangeNodeResult, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, err := s.loadChangeGraphSnapshot(ctx, job, input.Snapshot)
	if err != nil {
		return agentruntime.ChangeNodeResult{}, err
	}
	review := session.Workflow.Review
	if !s.hasLivePassingChecks(session) || review == nil || review.ProposalHash != session.Hash || review.Verdict != input.PriorVerdict || !specialistsComplete(session.Workflow, session.Hash) {
		return agentruntime.ChangeNodeResult{}, fmt.Errorf("human review requires current checked proposal and critic evidence")
	}
	status := "changes_requested"
	if review.Verdict == "approve" {
		if err := s.requestWorkflowInput(ctx, job.root, session); err != nil {
			return agentruntime.ChangeNodeResult{}, err
		}
		status = "awaiting_human_review"
	}
	return agentruntime.ChangeNodeResult{Node: input.Node, Snapshot: changeGraphSnapshot(session), Status: status, Verdict: review.Verdict}, nil
}
