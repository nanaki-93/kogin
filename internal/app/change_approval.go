package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// ChangeReviewRequest approves the identified proposal. Notes are the
// reviewer's own remarks; they are kept with the approval and never block it.
type ChangeReviewRequest struct {
	ChangeIdentity
	Notes string `json:"notes,omitempty"`
}

// ChangeReviewOverride asks to review a proposal the model reviewers rejected.
type ChangeReviewOverride struct {
	ChangeIdentity
	WorkflowID string `json:"workflow_id"`
	Reason     string `json:"reason"`
}

// revokeApproval clears the human approval together with its notes; both bind
// the hash they were given for.
func revokeApproval(session *ChangeSession) {
	session.ReviewedHash, session.ReviewNotes = "", ""
}

func (s *Service) ReviewChange(ctx context.Context, id string, request ChangeReviewRequest) (*ChangeSession, error) {
	notes := strings.TrimSpace(request.Notes)
	if notes != "" && !boundedWorkflowText(notes, maxApprovalNotes) {
		return nil, fmt.Errorf("approval notes must be text of at most %d bytes", maxApprovalNotes)
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	return s.reviewChangeLocked(ctx, root, session, notes)
}

// OverrideChangeReview lets the human take the last word on a model review: a
// proposal the specialists rejected moves to human review at its exact hash.
// It approves nothing; viewed files, passing checks and an explicit approval
// are still required, and a human rejection cannot be overridden.
func (s *Service) OverrideChangeReview(ctx context.Context, id string, request ChangeReviewOverride) (*ChangeSession, error) {
	reason := strings.TrimSpace(request.Reason)
	if !boundedWorkflowText(reason, maxOverrideReason) {
		return nil, fmt.Errorf("give a reason of at most %d bytes for overriding the reviewers", maxOverrideReason)
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	w := session.Workflow
	if w == nil || w.ID != request.WorkflowID {
		return nil, project.ErrRevisionConflict
	}
	if err := s.overridableReview(session); err != nil {
		return nil, err
	}
	if err := verifyChecksWorkspace(ctx, root, session); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	w.Override = &ChangeWorkflowOverride{ProposalHash: session.Hash, Reason: reason, At: now}
	w.Status, w.Reason, w.Stages[3].Status = "awaiting_human_review", "", "waiting"
	w.UpdatedAt, session.UpdatedAt = now, now
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	s.notifyWorkflow(ctx, session, "human_review")
	return session, nil
}

func (s *Service) overridableReview(session *ChangeSession) error {
	w := session.Workflow
	if w.Status != "changes_requested" || w.Input != nil {
		return fmt.Errorf("only a proposal the model reviewers rejected can be overridden")
	}
	if w.Review == nil || w.Review.ProposalHash != session.Hash || w.Review.Verdict != "changes_requested" || !specialistsComplete(w, session.Hash) {
		return fmt.Errorf("the reviewers' verdict does not cover the current proposal")
	}
	if !s.hasLivePassingChecks(session) {
		return fmt.Errorf("run current checks before overriding the reviewers")
	}
	return nil
}
