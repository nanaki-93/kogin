package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// rejectedWorkflow finishes a workflow whose architecture reviewer blocks the
// proposal while the correctness reviewer approves it with an advisory note.
func rejectedWorkflow(t *testing.T) (*Service, string, *ChangeSession) {
	t.Helper()
	s, root, session := workflowWithReviewer(t, func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		output := workflowApproval
		switch instruction := request.Messages[0].Content; {
		case strings.Contains(instruction, "architecture specialist"):
			output = `{"verdict":"changes_requested","summary":"Keep dependency direction.","findings":["Move the dependency into its owning package."]}`
		case strings.Contains(instruction, "correctness specialist"):
			output = `{"verdict":"approve","summary":"Correct.","findings":[],"notes":["Consider a table-driven test."]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
	})
	if _, err := s.StartChangeWorkflow(t.Context(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	result := finishedWorkflow(t, s, session.ID)
	if result.Workflow.Status != "changes_requested" || result.Workflow.Input != nil {
		t.Fatalf("model rejection did not finish as changes requested: %+v", result.Workflow)
	}
	return s, root, result
}

func overrideRequest(session *ChangeSession, reason string) ChangeReviewOverride {
	return ChangeReviewOverride{ChangeIdentity: identityForChange(session), WorkflowID: session.Workflow.ID, Reason: reason}
}

func TestAdvisoryNotesNeverDecideTheVerdict(t *testing.T) {
	_, _, rejected := rejectedWorkflow(t)
	review := rejected.Workflow.Review
	if review.Verdict != "changes_requested" || !slices.Equal(review.Findings, []ReviewItem{{Title: "Move the dependency into its owning package."}}) || !slices.Equal(review.Notes, []ReviewItem{{Title: "Consider a table-driven test."}}) {
		t.Fatalf("findings and notes were not kept apart: %+v", review)
	}
	if _, err := parseWorkflowReview(`{"verdict":"approve","summary":"Fine.","findings":[],"notes":["Optional."]}`, nil); err != nil {
		t.Fatalf("approve with notes rejected: %v", err)
	}
	if _, err := parseWorkflowReview(`{"verdict":"changes_requested","summary":"Notes only.","findings":[],"notes":["Optional."]}`, nil); err == nil {
		t.Fatal("advisory notes alone requested changes")
	}
	if _, err := parseWorkflowReview(`{"verdict":"approve","summary":"Too many.","findings":[],"notes":["1","2","3","4","5","6","7","8","9"]}`, nil); err == nil {
		t.Fatal("unbounded notes accepted")
	}
	forged := *rejected.Workflow
	forged.Review = &ChangeWorkflowReview{ProposalHash: review.ProposalHash, Verdict: review.Verdict, Summary: review.Summary, Findings: review.Findings}
	if err := validateWorkflowSpecialists(&forged); err == nil {
		t.Fatal("aggregate review dropped specialist notes")
	}
}

func TestHumanOverrideReopensReviewAtTheExactHash(t *testing.T) {
	s, root, rejected := rejectedWorkflow(t)
	if _, err := s.ReviewChange(t.Context(), rejected.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(rejected)}); err == nil {
		t.Fatal("rejected proposal approved without an override")
	}
	for name, request := range map[string]ChangeReviewOverride{
		"no reason":   overrideRequest(rejected, "  "),
		"long reason": overrideRequest(rejected, strings.Repeat("x", maxOverrideReason+1)),
		"other hash":  {ChangeIdentity: ChangeIdentity{ProjectID: rejected.ProjectID, ProjectRevision: rejected.ProjectRevision, Revision: rejected.Revision, Hash: "forged"}, WorkflowID: rejected.Workflow.ID, Reason: "Reviewed."},
	} {
		if _, err := s.OverrideChangeReview(t.Context(), rejected.ID, request); err == nil {
			t.Fatalf("%s: override accepted", name)
		}
	}
	other := overrideRequest(rejected, "Reviewed.")
	other.WorkflowID = "other"
	if _, err := s.OverrideChangeReview(t.Context(), rejected.ID, other); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("override for another workflow: %v", err)
	}
	overridden, err := s.OverrideChangeReview(t.Context(), rejected.ID, overrideRequest(rejected, " The dependency direction is intended here. "))
	if err != nil {
		t.Fatal(err)
	}
	w := overridden.Workflow
	if w.Status != "awaiting_human_review" || w.Stages[3].Status != "waiting" || w.Override == nil || w.Override.ProposalHash != overridden.Hash || w.Override.Reason != "The dependency direction is intended here." {
		t.Fatalf("override not recorded: %+v", w)
	}
	if w.Review.Verdict != "changes_requested" || overridden.ReviewedHash != "" {
		t.Fatal("override rewrote the model verdict or approved the proposal")
	}
	if _, err := s.OverrideChangeReview(t.Context(), rejected.ID, overrideRequest(overridden, "Again.")); err == nil {
		t.Fatal("override applied twice")
	}
	if _, err := s.ReviewChange(t.Context(), rejected.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(overridden), Notes: strings.Repeat("n", maxApprovalNotes+1)}); err == nil {
		t.Fatal("unbounded approval notes accepted")
	}
	approved, err := s.ReviewChange(t.Context(), rejected.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(overridden), Notes: "Follow up with a package move."})
	if err != nil {
		t.Fatal(err)
	}
	if approved.ReviewedHash != approved.Hash || approved.ReviewNotes != "Follow up with a package move." {
		t.Fatalf("approval with notes not recorded: %+v", approved)
	}
	stored, err := loadChangeSession(root, rejected.ID)
	if err != nil || stored.Workflow.Override == nil || stored.ReviewNotes == "" {
		t.Fatalf("override and notes did not persist: %+v %v", stored, err)
	}
	rechecked, err := s.CheckChange(t.Context(), rejected.ID, ChangeCheckRequest{ChangeIdentity: identityForChange(approved), CheckOptions: CheckOptions{RunTests: true}})
	if err != nil {
		t.Fatal(err)
	}
	if rechecked.ReviewedHash != "" || rechecked.ReviewNotes != "" || rechecked.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("new checks kept the approval or lost the override: %+v", rechecked.Workflow)
	}
}

func TestHumanRejectionCannotBeOverridden(t *testing.T) {
	s, _, ready := readyConfirmation(t)
	rejected, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "reject"))
	if err != nil || rejected.Workflow.Status != "changes_requested" {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if _, err := s.OverrideChangeReview(t.Context(), ready.ID, overrideRequest(rejected, "I changed my mind.")); err == nil {
		t.Fatal("override reversed a human rejection")
	}
}

func TestStoredOverrideAndNotesMustBindTheirEvidence(t *testing.T) {
	review := &ChangeWorkflowReview{ProposalHash: "proposal", Verdict: "approve", Summary: "Fine.", Findings: []ReviewItem{}}
	w := &ChangeWorkflow{Review: review, Override: &ChangeWorkflowOverride{ProposalHash: "proposal", Reason: "Reviewed.", At: time.Now()}}
	if reviewOverridden(w, "proposal") {
		t.Fatal("override applied to an approving review")
	}
	review.Verdict, review.Findings = "changes_requested", []ReviewItem{{Title: "Fix it."}}
	if !reviewOverridden(w, "proposal") || reviewOverridden(w, "other") {
		t.Fatal("override not bound to its proposal hash")
	}
	session := ChangeSession{ReviewNotes: "Orphaned.", Hash: changeProposalHash(nil)}
	if err := validateStoredChange(session); err == nil || !strings.Contains(err.Error(), "approval notes") {
		t.Fatalf("notes without an approval accepted: %v", err)
	}
}
