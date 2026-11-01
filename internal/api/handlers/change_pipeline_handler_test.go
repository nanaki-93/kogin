package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

func TestChangeWorkflowHTTPAdmissionProgressAndCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{}`}}}})
	}))
	defer provider.Close()
	defer close(release)
	h := newChangeHandlerFixture(t, provider.URL)
	defer h.service.CancelChangeWorkflows()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.service.ShutdownChangeWorkflows(ctx); err != nil {
			t.Error(err)
		}
	})
	index, _ := h.manager.Index()
	session, err := h.service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "security", Title: "Fix unsafe input", Paths: []string{"main.go", "main_test.go"}})
	if err != nil {
		t.Fatal(err)
	}
	request := app.ChangeWorkflowRequest{ChangeIdentity: app.ChangeIdentity{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Revision: session.Revision, Hash: session.Hash}, Message: "Fix and test."}
	for _, handler := range []http.HandlerFunc{h.StartWorkflow, h.CancelWorkflow, h.RespondInput} {
		for _, body := range []string{`{"unknown":true}`, `{}`, `{} {}`} {
			r := httptest.NewRequest("POST", "/changes/id/workflow", strings.NewReader(body))
			r.SetPathValue("sessionID", session.ID)
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code < 400 {
				t.Fatalf("accepted invalid body: %s", body)
			}
		}
	}
	denied := workflowResponse(t, h.StartWorkflow, "POST", "/changes/id/workflow", session.ID, request)
	if denied.Code != 400 {
		t.Fatalf("missing intent/trust: %d", denied.Code)
	}
	request.ConfirmSecurity = true
	if _, err := h.service.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	unavailable := request
	unavailable.Models.Review = "configured:" + strings.Repeat("0", 64)
	if conflict := workflowResponse(t, h.StartWorkflow, "POST", "/changes/id/workflow", session.ID, unavailable); conflict.Code != http.StatusConflict {
		t.Fatalf("unavailable catalog model: %d %s", conflict.Code, conflict.Body.String())
	}
	started := workflowResponse(t, h.StartWorkflow, "POST", "/changes/id/workflow", session.ID, request)
	if started.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", started.Code, started.Body.String())
	}
	var active app.ChangeSession
	if err := json.Unmarshal(started.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.Workflow.Models != (app.ChangeWorkflowModels{Create: "bug", Test: "code", Review: "analyze"}) {
		t.Fatalf("omitted models were not resolved: %+v", active.Workflow.Models)
	}
	if len(active.Workflow.Specialists) != 4 || active.Workflow.Specialists[3].ID != "security" {
		t.Fatalf("explicit security intent did not admit its specialist: %+v", active.Workflow.Specialists)
	}
	for _, report := range active.Workflow.Specialists {
		if report.Status != "pending" || report.Review != nil || report.Model.Profile != "analyze" || report.Evidence != "model_advice" {
			t.Fatalf("admission fabricated review evidence: %+v", report)
		}
	}
	<-entered
	query := "/changes/id?project_id=" + index.ProjectID + "&project_revision=" + index.ProjectRevision
	if progress := workflowResponse(t, h.Get, "GET", query, session.ID, nil); progress.Code != 200 || !strings.Contains(progress.Body.String(), `"status":"running"`) {
		t.Fatalf("progress: %d %s", progress.Code, progress.Body.String())
	}
	control := app.ChangeWorkflowControl{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, WorkflowID: "old-run"}
	if rejected := workflowResponse(t, h.CancelWorkflow, "POST", "/changes/id/workflow/cancel", session.ID, control); rejected.Code != 409 {
		t.Fatalf("stale control: %d", rejected.Code)
	}
	control.WorkflowID = active.Workflow.ID
	if canceled := workflowResponse(t, h.CancelWorkflow, "POST", "/changes/id/workflow/cancel", session.ID, control); canceled.Code != 200 {
		t.Fatalf("cancel: %d %s", canceled.Code, canceled.Body.String())
	}
}

func TestWorkflowInputHTTPGuardedReceipt(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		output := `{"verdict":"approve","summary":"The captured change meets its criteria.","findings":[]}`
		switch calls.Add(1) {
		case 1:
			output = `{"explanation":"Add helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 2 }\n"}]}`
		case 2:
			output = `{"explanation":"Test helper.","changes":[{"path":"new_test.go","content":"package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 2 { t.Fatal(\"wrong result\") } }\n"}]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
	}))
	defer provider.Close()
	h := newChangeHandlerFixture(t, provider.URL)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.service.ShutdownChangeWorkflows(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(h.manager.Root(), "go.mod"), []byte("module example.test/confirmation\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	index, err := h.service.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.TrustProjectExecution(t.Context(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	initial, err := h.service.OpenChangeSession(t.Context(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Add Helper", Paths: []string{"new.go", "new_test.go"}})
	if err != nil {
		t.Fatal(err)
	}
	identity := app.ChangeIdentity{ProjectID: initial.ProjectID, ProjectRevision: initial.ProjectRevision, Revision: initial.Revision, Hash: initial.Hash}
	if _, err := h.service.StartChangeWorkflow(t.Context(), initial.ID, app.ChangeWorkflowRequest{ChangeIdentity: identity, Message: "Add Helper returning two and a regression test."}); err != nil {
		t.Fatal(err)
	}
	ready := awaitHandlerInput(t, h, initial.ID)
	request := app.ChangeInputResponse{ChangeIdentity: app.ChangeIdentity{ProjectID: ready.ProjectID, ProjectRevision: ready.ProjectRevision, Revision: ready.Revision, Hash: ready.Hash}, WorkflowID: ready.Workflow.ID, RequestID: "wrong", Decision: "approve"}
	if response := workflowResponse(t, h.RespondInput, "POST", "/changes/id/workflow/input", ready.ID, request); response.Code != http.StatusConflict {
		t.Fatalf("stale request: %d %s", response.Code, response.Body)
	}
	request.RequestID = ready.Workflow.Input.RequestID
	before := calls.Load()
	for range 2 {
		response := workflowResponse(t, h.RespondInput, "POST", "/changes/id/workflow/input", ready.ID, request)
		var approved app.ChangeSession
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &approved) != nil || approved.Workflow.Input.Status != "approved" || approved.ReviewedHash != ready.Hash {
			t.Fatalf("response: %d %s", response.Code, response.Body)
		}
	}
	if calls.Load() != before {
		t.Fatal("input response dispatched provider")
	}
	if _, err := os.Stat(filepath.Join(h.manager.Root(), "new.go")); !os.IsNotExist(err) {
		t.Fatal("input response applied source")
	}
}

func awaitHandlerInput(t *testing.T, h *ChangeHandler, id string) *app.ChangeSession {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		session, err := h.service.ChangeSession(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if session.Workflow.Status != "running" {
			if session.Workflow.Status != "awaiting_human_review" || session.Workflow.Input == nil {
				t.Fatalf("handoff: %+v", session.Workflow)
			}
			return session
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("workflow did not request input")
		}
	}
}
