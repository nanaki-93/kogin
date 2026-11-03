package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func readyConfirmation(t *testing.T) (*Service, string, *ChangeSession) {
	t.Helper()
	s, root, initial := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval }})
	if _, err := s.StartChangeWorkflow(t.Context(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, initial.ID)
	if ready.Workflow.Status != "awaiting_human_review" || ready.Workflow.Input == nil || ready.Workflow.Input.Status != "waiting" || ready.ReviewedHash != "" {
		t.Fatalf("missing waiting input: %+v", ready.Workflow)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("checkpoint wrote source")
	}
	return s, root, ready
}

func inputResponse(session *ChangeSession, decision string) ChangeInputResponse {
	return ChangeInputResponse{ChangeIdentity: identityForChange(session), WorkflowID: session.Workflow.ID, RequestID: session.Workflow.Input.RequestID, Decision: decision}
}

func TestWorkflowSettingsStayFrozenThroughWorkAndHumanReview(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(started); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	control := settingsControl(t, s)
	control.Values.Roles = agentstate.RoleSettings{Creation: "bug", Testing: "analyze", Review: "code"}
	control.Values.Limits.ConcurrentCalls = 1
	if _, err := s.ControlAgentSettings(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	request := workflowRequest(initial)
	request.Models = ChangeWorkflowModels{}
	if _, err := s.StartChangeWorkflow(t.Context(), initial.ID, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("workflow did not reach its captured Creation role")
	}
	control = settingsControl(t, s)
	control.Values.Limits.ModelCalls = 1
	control.Values.Roles.Creation = "code"
	if _, err := s.ControlAgentSettings(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	ready := finishedWorkflow(t, s, initial.ID)
	if ready.Workflow.Status != "awaiting_human_review" || ready.Workflow.Models != (ChangeWorkflowModels{Create: "bug", Test: "analyze", Review: "code"}) {
		t.Fatal("future settings changed active workflow", ready.Workflow)
	}
	approved, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve"))
	if err != nil || approved.Workflow.Input.Status != "approved" {
		t.Fatal("settings edit invalidated captured pure human handoff", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("settings or review wrote project source", err)
	}
}

func TestWorkflowInputStillRejectsChangedOperatorConfiguration(t *testing.T) {
	s, _, ready := readyConfirmation(t)
	s.conf().runtimes.function.profile.Model = "changed-by-operator"
	if _, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve")); err == nil {
		t.Fatal("changed operator configuration resumed old handoff")
	}
}

func TestWorkflowInputApprovalAndDuplicateRetainExplicitApplyBoundary(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	input := ready.Workflow.Input
	if input.Action != "review_proposal" || input.Schema != agentruntime.ConfirmationSchema || input.NodeID != "human_review" || len(input.Scope) != 2 || input.Scope[0].Path != "new.go" || input.Scope[0].SourceExists {
		t.Fatalf("input scope: %+v", input)
	}
	if _, err := s.ApplyChange(t.Context(), ready.ID, ChangeApplyRequest{ChangeIdentity: identityForChange(ready), Confirm: true}); err == nil {
		t.Fatal("waiting input authorized Apply")
	}
	approved, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve"))
	if err != nil || approved.Workflow.Input.Status != "approved" || approved.ReviewedHash != ready.Hash {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	firstReceipt := approved.Workflow.Input.ReceivedAt
	again, err := s.ReviewChange(t.Context(), ready.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(ready)})
	if err != nil || !again.Workflow.Input.ReceivedAt.Equal(firstReceipt) {
		t.Fatalf("legacy Review duplicated receipt: %+v %v", again, err)
	}
	if _, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "reject")); err == nil {
		t.Fatal("conflicting response replaced approval")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("approval applied source")
	}
	// Approved, the task passes the gate; one made in the main checkout is then refused.
	if _, err := s.ApplyChange(t.Context(), ready.ID, ChangeApplyRequest{ChangeIdentity: identityForChange(ready), Confirm: true}); !errors.Is(err, ErrCheckoutTask) {
		t.Fatalf("approved checkout task: %v", err)
	}
}

func TestWorkflowInputRejectAndCancelStopReviewWithoutDispatch(t *testing.T) {
	for _, decision := range []string{"reject", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			s, root, ready := readyConfirmation(t)
			var result *ChangeSession
			var err error
			if decision == "cancel" {
				result, err = s.CancelChangeWorkflow(t.Context(), ready.ID, ChangeWorkflowControl{ProjectID: ready.ProjectID, ProjectRevision: ready.ProjectRevision, WorkflowID: ready.Workflow.ID})
			} else {
				result, err = s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, decision))
			}
			if err != nil || result.ReviewedHash != "" || inputAllowsReview(result.Workflow) {
				t.Fatalf("response: %+v %v", result, err)
			}
			again, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, decision))
			if err != nil || again.Workflow.Input.Status != result.Workflow.Input.Status || again.Workflow.Input.ReceivedAt != result.Workflow.Input.ReceivedAt {
				t.Fatalf("duplicate: %+v %v", again, err)
			}
			checked, err := s.CheckChange(t.Context(), ready.ID, ChangeCheckRequest{ChangeIdentity: identityForChange(ready), CheckOptions: CheckOptions{RunTests: true}})
			if err != nil {
				t.Fatal(err)
			}
			if canHumanReviewProposal(checked) {
				t.Fatal("rerunning checks bypassed negative human decision")
			}
			if _, err := s.ReviewChange(t.Context(), ready.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(ready)}); err == nil {
				t.Fatal("negative response authorized review")
			}
			if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
				t.Fatal("negative response wrote source")
			}
		})
	}
}

func TestWorkflowInputRejectsForgedIdentitySourceAndHistoricalChecks(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	for _, alter := range []func(*ChangeInputResponse){
		func(r *ChangeInputResponse) { r.RequestID = "previous" },
		func(r *ChangeInputResponse) { r.WorkflowID = "previous" },
		func(r *ChangeInputResponse) { r.Hash = "previous" },
		func(r *ChangeInputResponse) { r.Revision++ },
		func(r *ChangeInputResponse) { r.Decision = "apply" },
	} {
		request := inputResponse(ready, "approve")
		alter(&request)
		if _, err := s.RespondChangeInput(t.Context(), ready.ID, request); err == nil {
			t.Fatal("forged response accepted")
		}
	}
	delete(s.changeAuthority, ready.ProjectID+"/"+ready.ID)
	if _, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve")); err == nil {
		t.Fatal("historical checks authorized approval")
	}
	s.changeAuthority[ready.ProjectID+"/"+ready.ID] = "checks:" + ready.Hash
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve")); err == nil {
		t.Fatal("changed source authorized approval")
	}
	stored, err := loadChangeSession(root, ready.ID)
	if err != nil || stored.Workflow.Input.Status != "waiting" || stored.ReviewedHash != "" {
		t.Fatalf("failed response published: %+v %v", stored, err)
	}
}

func TestWorkflowInputRestoreRequiresFreshChecksAndExplicitResponse(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	if _, err := s.ReviewChange(t.Context(), ready.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(ready)}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(s.conf().runtimes.bug.profile.APIBaseURL)
	cfg.ModelScopes.Bug.APIBaseURL, cfg.ModelScopes.Bug.Model = s.conf().runtimes.bug.profile.APIBaseURL, s.conf().runtimes.bug.profile.Model
	cfg.ModelScopes.Analyze.APIBaseURL, cfg.ModelScopes.Analyze.Model = s.conf().runtimes.analyze.profile.APIBaseURL, s.conf().runtimes.analyze.profile.Model
	cfg.ModelScopes.Code.APIBaseURL, cfg.ModelScopes.Code.Model = s.conf().runtimes.function.profile.APIBaseURL, s.conf().runtimes.function.profile.Model
	restarted, err := New(cfg, s.manager)
	if err != nil {
		t.Fatal(err)
	}
	read, err := restarted.ChangeSession(t.Context(), ready.ID)
	if err != nil || read.Workflow.Input.Status != "approved" || restarted.changeWorkflow.job != nil {
		t.Fatalf("passive read: %+v %v", read, err)
	}
	if _, err := restarted.ApplyChange(t.Context(), ready.ID, ChangeApplyRequest{ChangeIdentity: identityForChange(ready), Confirm: true}); err == nil {
		t.Fatal("saved receipt restored Apply authority")
	}
	if _, err := restarted.ReviewChange(t.Context(), ready.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(ready)}); err == nil {
		t.Fatal("saved checks restored review authority")
	}
	if _, err := restarted.TrustProjectExecution(t.Context(), ready.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.CheckChange(t.Context(), ready.ID, ChangeCheckRequest{ChangeIdentity: identityForChange(ready), CheckOptions: CheckOptions{RunTests: true}}); err != nil {
		t.Fatal(err)
	}
	approved, err := restarted.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve"))
	if err != nil || approved.Workflow.Input.ReceivedAt != read.Workflow.Input.ReceivedAt {
		t.Fatalf("fresh admission failed or replayed receipt: %+v %v", approved, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("restart replayed Apply")
	}
}

func TestWorkflowInputJournalStateCannotGrantReview(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	store, _, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Sessions(t.Context(), agentstate.StateApp, "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.State["mini_orca.confirmation"] == nil {
			continue
		}
		_, err := store.AppendEvent(t.Context(), record.Key, agentstate.Event{ID: "forged", Data: []byte(`{"Actions":{"StateDelta":{"approved":true}}}`)}, agentstate.State{"approved": []byte(`true`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplyChange(t.Context(), ready.ID, ChangeApplyRequest{ChangeIdentity: identityForChange(ready), Confirm: true}); err == nil {
		t.Fatal("SDK state granted Apply authority")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.RespondChangeInput(canceled, ready.ID, inputResponse(ready, "approve")); err == nil {
		t.Fatal("canceled request accepted")
	}
	ready.Workflow.Input.Scope[0].ProposalHash = "forged"
	if err := storeChangeSession(root, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewChange(t.Context(), ready.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(ready)}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("forged scope accepted: %v", err)
	}
}

func TestWorkflowInputRecoversReceiptBeforeDomainPublication(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	store, binding, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	request, err := confirmationRuntimeInput(ready, ready.Workflow.Input)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after native resume but before the domain record is saved.
	receipt, err := agentruntime.ResumeConfirmation(t.Context(), store, binding, ready.Workflow.Input.RunID, request, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyChange(t.Context(), ready.ID, ChangeApplyRequest{ChangeIdentity: identityForChange(ready), Confirm: true}); err == nil {
		t.Fatal("private receipt alone authorized Apply")
	}
	stored, err := loadChangeSession(root, ready.ID)
	if err != nil || stored.Workflow.Input.Status != "waiting" {
		t.Fatalf("unexpected domain receipt: %+v %v", stored, err)
	}
	// An external edit can arrive after admission while journal I/O completes.
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s.changesMu.Lock()
	err = s.resolveWorkflowInput(t.Context(), root, ready, "approve")
	s.changesMu.Unlock()
	if !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("source changed before publication but approval was published: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "new.go")); err != nil {
		t.Fatal(err)
	}
	result, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve"))
	if err != nil || !result.Workflow.Input.ReceivedAt.Equal(receipt.ReceivedAt) || result.ReviewedHash != ready.Hash {
		t.Fatalf("receipt recovery: %+v %v", result, err)
	}
}

func TestWorkflowInputExpiryIsRecordedWithoutApproval(t *testing.T) {
	s, root, ready := readyConfirmation(t)
	ctx, cancel, err := s.workflowAgentContext(t.Context(), ready.Workflow.Models)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	// Use the same host/native boundary with a short deadline to exercise expiry.
	ready.Workflow.Input.RequestID = "expiring-request"
	ready.Workflow.Input.ExpiresAt = time.Now().UTC().Add(time.Second)
	request, err := confirmationRuntimeInput(ready, ready.Workflow.Input)
	if err != nil {
		t.Fatal(err)
	}
	ready.Workflow.Input.RunID, err = agentruntime.RequestConfirmation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeChangeSession(root, ready); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Until(request.ExpiresAt))
	defer timer.Stop()
	<-timer.C
	if _, err := s.RespondChangeInput(t.Context(), ready.ID, inputResponse(ready, "approve")); err == nil {
		t.Fatal("expired request approved")
	}
	stored, err := loadChangeSession(root, ready.ID)
	if err != nil || stored.Workflow.Input.Status != "expired" || stored.ReviewedHash != "" || stored.Workflow.Status != "outdated" {
		t.Fatalf("expiry not persisted: %+v %v", stored, err)
	}
}
