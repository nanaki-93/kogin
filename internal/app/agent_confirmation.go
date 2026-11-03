package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type ChangeInputScope struct {
	Path         string `json:"path"`
	SourceExists bool   `json:"source_exists"`
	SourceHash   string `json:"source_hash"`
	ProposalHash string `json:"proposal_hash"`
}

// A saved decision is evidence of input, never source-write authority. Apply
// still requires the current process's checked and explicitly reviewed hash.
type ChangeInput struct {
	ChangeIdentity
	RequestID  string             `json:"request_id"`
	RunID      string             `json:"run_id"`
	WorkflowID string             `json:"workflow_id"`
	NodeID     string             `json:"node_id"`
	Action     string             `json:"action"`
	Schema     string             `json:"schema"`
	Status     string             `json:"status"`
	Scope      []ChangeInputScope `json:"scope"`
	ExpiresAt  time.Time          `json:"expires_at"`
	ReceivedAt time.Time          `json:"received_at,omitempty"`
}

type ChangeInputResponse struct {
	ChangeIdentity
	WorkflowID string `json:"workflow_id"`
	RequestID  string `json:"request_id"`
	Decision   string `json:"decision"`
}

func confirmationScope(session *ChangeSession) []ChangeInputScope {
	scope := make([]ChangeInputScope, 0, len(session.Targets))
	for _, target := range session.Targets {
		entry := ChangeInputScope{Path: target.Path, SourceExists: target.Exists, SourceHash: target.Hash, ProposalHash: target.Hash}
		for _, edit := range session.Changes {
			if edit.Path == target.Path {
				entry.ProposalHash = edit.Hash
				break
			}
		}
		scope = append(scope, entry)
	}
	slices.SortFunc(scope, func(a, b ChangeInputScope) int { return strings.Compare(a.Path, b.Path) })
	return scope
}

func confirmationRuntimeInput(session *ChangeSession, input *ChangeInput) (agentruntime.ConfirmationInput, error) {
	if input.ChangeIdentity != identityForChange(session) || input.WorkflowID != session.Workflow.ID || !slices.Equal(input.Scope, confirmationScope(session)) {
		return agentruntime.ConfirmationInput{}, project.ErrRevisionConflict
	}
	data, err := json.Marshal(input.Scope)
	if err != nil {
		return agentruntime.ConfirmationInput{}, fmt.Errorf("fingerprint approval scope: %w", err)
	}
	return agentruntime.ConfirmationInput{RequestID: input.RequestID, Action: input.Action, Snapshot: changeGraphSnapshot(session), ScopeHash: contentHash(data), ExpiresAt: input.ExpiresAt}, nil
}

func (s *Service) requestWorkflowInput(ctx context.Context, root string, session *ChangeSession) error {
	input := &ChangeInput{ChangeIdentity: identityForChange(session), RequestID: rand.Text(), WorkflowID: session.Workflow.ID, NodeID: "human_review", Action: "review_proposal", Schema: agentruntime.ConfirmationSchema, Status: "waiting", Scope: confirmationScope(session), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	request, err := confirmationRuntimeInput(session, input)
	if err != nil {
		return err
	}
	input.RunID, err = agentruntime.RequestConfirmation(ctx, request)
	if err != nil {
		return err
	}
	session.Workflow.Input = input
	return storeChangeSession(root, session)
}

func validateChangeInput(input *ChangeInput) error {
	if input == nil {
		return nil
	}
	if err := validateChangeInputShape(input); err != nil {
		return err
	}
	switch input.Status {
	case "waiting":
		if !input.ReceivedAt.IsZero() {
			return fmt.Errorf("waiting input has a receipt")
		}
	case "approved", "rejected", "canceled", "expired":
		if input.ReceivedAt.IsZero() {
			return fmt.Errorf("resolved input is missing its receipt")
		}
	default:
		return fmt.Errorf("invalid human input status")
	}
	return nil
}

func validateChangeInputShape(input *ChangeInput) error {
	for _, id := range []string{input.RequestID, input.RunID, input.WorkflowID, input.ProjectID, input.ProjectRevision, input.Hash} {
		if strings.TrimSpace(id) == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
			return fmt.Errorf("invalid human input identity")
		}
	}
	if input.NodeID != "human_review" || input.Action != "review_proposal" || input.Schema != agentruntime.ConfirmationSchema || input.Revision < 0 || input.ExpiresAt.IsZero() || len(input.Scope) == 0 || len(input.Scope) > maxStoredTaskPaths {
		return fmt.Errorf("invalid human input checkpoint")
	}
	return nil
}

func inputAllowsReview(w *ChangeWorkflow) bool {
	return w.Input == nil || w.Input.Status == "waiting" || w.Input.Status == "approved"
}

func (s *Service) cancelWaitingInput(ctx context.Context, root string, session *ChangeSession) (*ChangeSession, error) {
	if session.Workflow.Input == nil || session.Workflow.Input.Status != "waiting" {
		return session, nil
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return nil, err
	}
	return s.respondChangeInputLocked(ctx, root, session, ChangeInputResponse{ChangeIdentity: identityForChange(session), WorkflowID: session.Workflow.ID, RequestID: session.Workflow.Input.RequestID, Decision: "cancel"})
}

func (s *Service) RespondChangeInput(ctx context.Context, id string, request ChangeInputResponse) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	return s.respondChangeInputLocked(ctx, root, session, request)
}

func (s *Service) respondChangeInputLocked(ctx context.Context, root string, session *ChangeSession, request ChangeInputResponse) (*ChangeSession, error) {
	w := session.Workflow
	if w == nil || w.Input == nil || w.ID != request.WorkflowID || w.Input.RequestID != request.RequestID {
		return nil, project.ErrRevisionConflict
	}
	if request.Decision == "approve" {
		return s.reviewChangeLocked(ctx, root, session, "")
	}
	if request.Decision != "reject" && request.Decision != "cancel" {
		return nil, fmt.Errorf("decision must be approve, reject or cancel")
	}
	if err := s.resolveWorkflowInput(ctx, root, session, request.Decision); err != nil {
		return nil, err
	}
	return session, nil
}

// Caller owns changesMu and has checked current proposal/source identity. The
// receipt and pure native handoff are durable before domain approval is saved.
func (s *Service) resolveWorkflowInput(ctx context.Context, root string, session *ChangeSession, decision string) error {
	defer s.notifyEvent("agent_run")
	input := session.Workflow.Input
	request, err := confirmationRuntimeInput(session, input)
	if err != nil {
		return err
	}
	store, binding, err := s.confirmationJournal(ctx, root, input.RunID)
	if err != nil {
		return err
	}
	receipt, err := agentruntime.ResumeConfirmation(ctx, store, binding, input.RunID, request, decision)
	if err != nil {
		return fmt.Errorf("record human input: %w", err)
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return err
	}
	input.Status = map[string]string{"approve": "approved", "reject": "rejected", "cancel": "canceled", "expire": "expired"}[receipt.Decision]
	input.ReceivedAt = receipt.ReceivedAt
	session.Workflow.UpdatedAt, session.UpdatedAt = receipt.ReceivedAt, receipt.ReceivedAt
	if receipt.Decision != "approve" {
		revokeApproval(session)
		delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
		session.Workflow.Stages[3].Status = "blocked"
		setInputOutcome(session.Workflow, receipt.Decision)
	}
	return storeChangeSession(root, session)
}

func setInputOutcome(w *ChangeWorkflow, decision string) {
	switch decision {
	case "reject":
		w.Status, w.Reason = "changes_requested", "The checked proposal was rejected. Start a new workflow to revise it."
	case "cancel":
		w.Status, w.Reason = "canceled", "The pending review was canceled. No source was changed."
	case "expire":
		w.Status, w.Reason = "outdated", "The review request expired. Start a new workflow for a fresh request."
	}
}

func (s *Service) validateHumanReview(session *ChangeSession) error {
	if !canHumanReviewProposal(session) || len(session.Checks) == 0 || !requiredChecksPassed(session.Checks) {
		return fmt.Errorf("complete current proposal checks before review")
	}
	if err := requireApprovedChangePlan(session); err != nil {
		return err
	}
	if session.Plan != nil && !allChangeFilesReviewed(session) {
		return fmt.Errorf("mark each changed file reviewed before Apply")
	}
	if (session.Plan != nil || session.Workflow != nil) && !s.hasLivePassingChecks(session) {
		return fmt.Errorf("run current checks before review")
	}
	return nil
}

func (s *Service) reviewChangeLocked(ctx context.Context, root string, session *ChangeSession, notes string) (*ChangeSession, error) {
	if err := s.validateHumanReview(session); err != nil {
		return nil, err
	}
	if err := verifyChecksWorkspace(ctx, root, session); err != nil {
		return nil, err
	}
	if session.Workflow != nil && session.Workflow.Input != nil {
		if err := s.resolveWorkflowInput(ctx, root, session, "approve"); err != nil {
			return nil, err
		}
		if session.Workflow.Input.Status != "approved" {
			return nil, fmt.Errorf("review request expired; start a new workflow")
		}
	}
	session.ReviewedHash, session.ReviewNotes = session.Hash, notes
	if session.Workflow != nil {
		session.Workflow.Stages[3].Status = "completed"
	}
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	s.changeAuthority[session.ProjectID+"/"+session.ID] = "review:" + session.Hash
	return session, nil
}
