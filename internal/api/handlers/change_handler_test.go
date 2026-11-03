package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func newChangeHandlerFixture(t *testing.T, baseURL string) *ChangeHandler {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig(baseURL), manager)
	if err != nil {
		t.Fatal(err)
	}
	return NewChangeHandler(service, manager)
}

func workflowResponse(t *testing.T, handler http.HandlerFunc, method, path, id string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.SetPathValue("sessionID", id)
	request.SetPathValue("featureID", id)
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func TestChangeHTTPSelectedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"explanation":"Document the behavior.","changes":[{"path":"README.md","content":"# Behavior\nReturns the result.\n"}]}`}}}})
	}))
	defer server.Close()
	h := newChangeHandlerFixture(t, server.URL)
	index, _ := h.manager.Index()
	session, err := h.service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Title: "Document behavior", Kind: "fix", Paths: []string{"README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	request := app.ChangeMessageRequest{ChangeIdentity: app.ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}, Message: "Document behavior.", Profile: "unknown"}
	if w := workflowResponse(t, h.Message, "POST", "/changes/id/messages", session.ID, request); w.Code < 400 {
		t.Fatal("unknown model profile was accepted")
	}
	request.Profile = "analyze"
	w := workflowResponse(t, h.Message, "POST", "/changes/id/messages", session.ID, request)
	var proposal app.ChangeSession
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &proposal) != nil || proposal.ContextManifest.Scope != "analyze" {
		t.Fatalf("profile selection = %d %s", w.Code, w.Body)
	}
}

func TestChangeHTTPPreservesFindingTargetsAndRejectsInvalidScope(t *testing.T) {
	h := gitChangeHandlerFixture(t)
	index, _ := h.manager.Index()
	target := project.FindingTarget{ProjectID: "origin-project", Kind: "semantic", ID: "finding:origin", Path: "main.go"}
	for _, state := range []string{"valid", "legacy", "duplicate", "outside scope", "unknown producer", "unknown security source", "missing origin", "too many"} {
		t.Run(state, func(t *testing.T) {
			request := app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "fix", Title: "Fix selected finding", Paths: []string{"main.go"}, FindingTargets: []project.FindingTarget{target}}
			switch state {
			case "legacy":
				request.FindingTargets = nil
			case "duplicate":
				request.FindingTargets = append(request.FindingTargets, target)
			case "outside scope":
				request.FindingTargets[0].Path = "other.go"
			case "unknown producer":
				request.FindingTargets[0].Kind = "unknown"
			case "unknown security source":
				request.FindingTargets[0].Kind, request.FindingTargets[0].Source = "security", "unknown"
			case "missing origin":
				request.FindingTargets[0].ProjectID = ""
			case "too many":
				request.FindingTargets = make([]project.FindingTarget, 17)
			}
			response := workflowResponse(t, h.Open, "POST", "/changes", "", app.ChangeOpenRequest{ChangeCreateRequest: request, ConfirmExecution: true})
			if state != "valid" && state != "legacy" {
				if response.Code != http.StatusBadRequest {
					t.Fatalf("invalid finding targets: %d %s", response.Code, response.Body)
				}
				return
			}
			var opened openedTask
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &opened) != nil {
				t.Fatalf("open: %d %s", response.Code, response.Body)
			}
			child, _, err := h.service.WorktreeService(context.Background(), opened.WorktreePath)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := child.ChangeSession(context.Background(), opened.ID)
			if err != nil || len(restored.FindingTargets) != len(request.FindingTargets) {
				t.Fatalf("restored targets: %+v, %v", restored, err)
			}
			if state == "valid" && restored.FindingTargets[0] != target {
				t.Fatalf("lost original finding identity: %+v", restored.FindingTargets)
			}
		})
	}
	// Invalid input leaves no worktree behind.
	if entries, err := os.ReadDir(filepath.Join(h.manager.Root(), ".worktrees")); err != nil || len(entries) != 2 {
		t.Fatalf("worktrees: %d, %v", len(entries), err)
	}
}

func TestChangeHTTPReadGuardsAndStrictRequests(t *testing.T) {
	handler := newChangeHandlerFixture(t, "http://127.0.0.1:1")
	index, _ := handler.manager.Index()
	query := "?project_id=" + index.ProjectID + "&project_revision=" + index.ProjectRevision
	for _, step := range []http.HandlerFunc{handler.List} {
		denied := workflowResponse(t, step, "GET", "/changes", "", nil)
		if denied.Code != 409 {
			t.Fatal("unguarded history read accepted")
		}
		response := workflowResponse(t, step, "GET", "/changes"+query, "", nil)
		if response.Code != 200 {
			t.Fatalf("passive read: %d %s", response.Code, response.Body.String())
		}
	}
	bad := httptest.NewRecorder()
	handler.Open(bad, httptest.NewRequest("POST", "/changes", bytes.NewBufferString(`{"paths":["main.go"],"unexpected":true}`)))
	if bad.Code != 400 {
		t.Fatal("unknown field accepted")
	}
	assertStructuredError(t, bad)
}

// The Web UI stops a long revision by closing its request: the daemon must
// cancel the model call and keep the proposal as it was.
func TestChangeHTTPClosedRevisionRequestCancelsTheModelCall(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A read body lets the server notice the daemon closing the call.
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer provider.Close()
	h := newChangeHandlerFixture(t, provider.URL)
	index, _ := h.manager.Index()
	session, err := h.service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Title: "Document behavior", Kind: "fix", Paths: []string{"README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /changes/{sessionID}/messages", func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		h.Message(w, r)
	})
	daemon := httptest.NewServer(mux)
	defer daemon.Close()
	body, _ := json.Marshal(app.ChangeMessageRequest{ChangeIdentity: app.ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}, Message: "Document behavior.", Profile: "code"})
	ctx, stop := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, daemon.URL+"/changes/"+session.ID+"/messages", bytes.NewReader(body))
	sent := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
		sent <- err
	}()
	<-entered
	stop()
	if err := <-sent; err == nil {
		t.Fatal("the stopped request still received a response")
	}
	<-canceled
	<-finished
	stored, err := h.service.ChangeSession(context.Background(), session.ID)
	if err != nil || stored.Revision != session.Revision || len(stored.Messages) != 0 || len(stored.Changes) != 0 {
		t.Fatalf("a stopped revision changed the proposal: %+v, %v", stored, err)
	}
}

func TestChangeHTTPReanalyzeAnswersWithTheDecision(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"still_valid":false,"reason":"Run now handles the case."}`}}}})
	}))
	defer provider.Close()
	h := newChangeHandlerFixture(t, provider.URL)
	index, _ := h.manager.Index()
	session, err := h.service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Title: "Fix: Run", Kind: "fix", Paths: []string{"main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	request := app.ChangeReanalysisRequest{ChangeIdentity: app.ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}}
	if w := workflowResponse(t, h.Reanalyze, "POST", "/changes/id/reanalyze", session.ID, request); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "matches the current files") {
		t.Fatalf("current task = %d %s", w.Code, w.Body)
	}
	if w := workflowResponse(t, h.Reanalyze, "POST", "/changes/id/reanalyze", "change-"+strings.Repeat("0", 32), request); w.Code != http.StatusNotFound {
		t.Fatalf("missing task = %d %s", w.Code, w.Body)
	}
	if err := os.WriteFile(filepath.Join(h.manager.Root(), "main.go"), []byte("package main\n\nfunc Run() { println() }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	w := workflowResponse(t, h.Reanalyze, "POST", "/changes/id/reanalyze", session.ID, request)
	var result app.ChangeReanalysis
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Outcome != app.ReanalysisNotValid || result.Deleted != session.ID || result.Reason != "Run now handles the case." {
		t.Fatalf("re-analysis = %d %s", w.Code, w.Body)
	}
}

func TestQuarantineDeleteHTTP(t *testing.T) {
	handler := newChangeHandlerFixture(t, "http://127.0.0.1:1")
	index, _ := handler.manager.Index()
	root := handler.manager.Root()
	setAside := ".kogin/analysis/run.quarantined-20261028T130610.402215000Z.json"
	if err := os.MkdirAll(filepath.Join(root, ".kogin", "analysis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(setAside)), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	record := `[{"kind":"analysis_run","file":".mini-orca/analysis/run.json","quarantined_as":".mini-orca/analysis/run.quarantined-20261028T130610.402215000Z.json","reason":"saved analysis progress is unreadable","remedy":"","at":"2026-10-28T13:06:10Z"}]`
	if err := os.WriteFile(filepath.Join(root, ".kogin", "quarantine.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	request := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.DeleteQuarantined(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/quarantine/delete", bytes.NewBufferString(body)))
		return response
	}
	identity := `"project_id":"` + index.ProjectID + `","project_revision":"` + index.ProjectRevision + `"`
	for body, want := range map[string]int{
		`{` + identity + `,"quarantined_as":"` + setAside + `","unexpected":true}`:                                http.StatusBadRequest,
		`{"project_id":"` + index.ProjectID + `","project_revision":"stale","quarantined_as":"` + setAside + `"}`: http.StatusConflict,
		`{` + identity + `,"quarantined_as":".kogin/index.json"}`:                                                 http.StatusNotFound,
	} {
		if response := request(body); response.Code != want {
			t.Fatalf("%s: status = %d, want %d: %s", body, response.Code, want, response.Body.String())
		} else {
			assertStructuredError(t, response)
		}
	}
	response := request(`{` + identity + `,"quarantined_as":"` + setAside + `"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	var report app.QuarantineReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil || len(report.Files) != 0 || report.Skipped == nil {
		t.Fatalf("report: %s %v", response.Body.String(), err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(setAside))); !os.IsNotExist(err) {
		t.Fatalf("set-aside file kept: %v", err)
	}
}

func TestChangeHTTPKeepsTheIssuesATaskFixesOneAtATime(t *testing.T) {
	h := gitChangeHandlerFixture(t)
	index, _ := h.manager.Index()
	targets := []project.FindingTarget{{ProjectID: index.ProjectID, Kind: "semantic", ID: "finding:a", Path: "main.go"}, {ProjectID: index.ProjectID, Kind: "semantic", ID: "finding:b", Path: "main.go"}}
	issues := []app.ChangeIssue{{FindingID: "finding:a", Path: "main.go", Title: "Run ignores errors", Detail: "line 3"}, {FindingID: "finding:b", Path: "main.go", Title: "Run never logs"}}
	request := app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "fix", Title: "Fix 2 issues in main.go", Paths: []string{"main.go"}, FindingTargets: targets, Issues: issues}
	response := workflowResponse(t, h.Open, "POST", "/changes", "", app.ChangeOpenRequest{ChangeCreateRequest: request, ConfirmExecution: true})
	var session openedTask
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &session) != nil || len(session.Issues) != 2 || session.Issues[0].Status != app.IssuePending || session.Issues[1].Title != "Run never logs" {
		t.Fatalf("open: %d %s", response.Code, response.Body)
	}
	unlinked := request
	unlinked.Issues = []app.ChangeIssue{issues[0], {FindingID: "finding:c", Path: "main.go", Title: "Not linked"}}
	started := request
	started.Issues = []app.ChangeIssue{issues[0], {FindingID: "finding:b", Path: "main.go", Title: "Run never logs", Status: app.IssueFixed}}
	for name, invalid := range map[string]app.ChangeCreateRequest{"an unlinked issue": unlinked, "an issue with progress": started} {
		if response := workflowResponse(t, h.Open, "POST", "/changes", "", app.ChangeOpenRequest{ChangeCreateRequest: invalid, ConfirmExecution: true}); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, response.Code, response.Body)
		} else {
			assertStructuredError(t, response)
		}
	}
}
