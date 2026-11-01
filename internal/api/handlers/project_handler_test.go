package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestProjectRestoreActivatesStoredAnalysisWithoutModelAccess(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	if err := project.NewAnalyzerWithProvenance(nil, "", "analysis", "", "").StoreLocalInventory(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)
	body, err := json.Marshal(map[string]string{"project_path": root})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()

	handler.Restore(response, httptest.NewRequest(http.MethodPost, "/api/projects/restore", bytes.NewReader(body)))

	if response.Code != http.StatusOK {
		t.Fatalf("restore status = %d: %s", response.Code, response.Body.String())
	}
	if _, err := manager.Index(); err != nil {
		t.Fatalf("restored index: %v", err)
	}
}

func TestProjectIndexAndSymbolAPIs(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc Run(name string) string { return name }\n")
	writeProjectHandlerFixture(t, root, ".env", "TOKEN=must-not-expose")
	writeProjectHandlerFixture(t, root, "asset.bin", string([]byte{0, 1, 2}))
	handler := newIndexedProjectHandler(t, root)

	indexResponse := httptest.NewRecorder()
	handler.Index(indexResponse, httptest.NewRequest(http.MethodGet, "/api/projects/current/index", nil))
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d: %s", indexResponse.Code, indexResponse.Body.String())
	}
	var index project.ProjectIndex
	if err := json.NewDecoder(indexResponse.Body).Decode(&index); err != nil {
		t.Fatal(err)
	}
	if len(index.Files) != 2 || index.Files[0].Path != "asset.bin" || index.Files[1].Path != "main.go" {
		t.Fatalf("unsafe or unexpected index paths: %+v", index.Files)
	}

	envFileResponse := httptest.NewRecorder()
	handler.FileInfo(envFileResponse, httptest.NewRequest(http.MethodGet, "/api/project/file-info?path=.env", nil))
	if envFileResponse.Code != http.StatusForbidden {
		t.Fatalf("excluded file-info status = %d, want %d: %s", envFileResponse.Code, http.StatusForbidden, envFileResponse.Body.String())
	}

	stale := httptest.NewRecorder()
	handler.Reindex(stale, httptest.NewRequest(http.MethodPost, "/api/projects/current/reindex", bytes.NewBufferString(`{"project_revision":"sha256:stale"}`)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale reindex status = %d: %s", stale.Code, stale.Body.String())
	}
	assertStructuredError(t, stale)

	missingRevision := httptest.NewRecorder()
	handler.Reindex(missingRevision, httptest.NewRequest(http.MethodPost, "/api/projects/current/reindex", bytes.NewBufferString(`{}`)))
	if missingRevision.Code != http.StatusConflict {
		t.Fatalf("missing revision status = %d: %s", missingRevision.Code, missingRevision.Body.String())
	}
	assertStructuredError(t, missingRevision)
}

func TestPromptHandlersConfirmOnlyTheirOwnRemoteScope(t *testing.T) {
	bugCalls := 0
	bugProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bugCalls++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`}}}})
	}))
	defer bugProvider.Close()

	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(&config.Config{
		ModelScopes: config.ModelScopesConfig{
			Analyze: config.ModelProfileConfig{APIBaseURL: "https://analyze.example/v1", APIKey: "hidden", Model: "analyze"},
			Bug:     config.ModelProfileConfig{APIBaseURL: bugProvider.URL + "/v1", Model: "bug"},
			Code:    config.ModelProfileConfig{APIBaseURL: "https://function.example/v1", APIKey: "hidden", Model: "code"},
		},
		Retry: config.RetryConfig{MaxRetries: 0, BackoffBase: 1, BackoffMax: 1},
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	projectHandler := NewProjectHandler(manager, service)
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}

	importResponse := httptest.NewRecorder()
	projectHandler.Import(importResponse, httptest.NewRequest(http.MethodPost, "/api/projects/import", bytes.NewBufferString(`{"project_path":"`+root+`"}`)))
	// Without consent for the remote analyze model, import records only the
	// deterministic inventory.
	if importResponse.Code != http.StatusOK || bugCalls != 0 || strings.Contains(importResponse.Body.String(), "hidden") || !strings.Contains(importResponse.Body.String(), "not requested from the remote analyze model") {
		t.Fatalf("unconfirmed remote import = %d %s, bug calls %d", importResponse.Code, importResponse.Body.String(), bugCalls)
	}

	session, err := service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Change Run", Paths: []string{"main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(app.ChangeMessageRequest{ChangeIdentity: app.ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}, Message: "Change Run."})
	if err != nil {
		t.Fatal(err)
	}
	message := httptest.NewRequest(http.MethodPost, "/api/projects/current/changes/"+session.ID+"/messages", bytes.NewReader(body))
	message.SetPathValue("sessionID", session.ID)
	functionResponse := httptest.NewRecorder()
	NewChangeHandler(service, manager).Message(functionResponse, message)
	if functionResponse.Code != http.StatusBadRequest || strings.Contains(functionResponse.Body.String(), "hidden") || bugCalls != 0 {
		t.Fatalf("unconfirmed remote function = %d %s, bug calls %d", functionResponse.Code, functionResponse.Body.String(), bugCalls)
	}
	assertStructuredError(t, functionResponse)

	reindexResponse := httptest.NewRecorder()
	projectHandler.Reindex(reindexResponse, httptest.NewRequest(http.MethodPost, "/api/projects/current/reindex", bytes.NewBufferString(`{"project_revision":"`+index.ProjectRevision+`"}`)))
	if reindexResponse.Code != http.StatusOK || bugCalls != 0 {
		t.Fatalf("model-free reindex = %d %s, bug calls %d", reindexResponse.Code, reindexResponse.Body.String(), bugCalls)
	}
}

func TestProjectWorkspaceAPIsAreRevisionGuardedAndSourceFree(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "go.mod", "module fixture\n\ngo 1.22\n")
	writeProjectHandlerFixture(t, root, "main.go", "package fixture\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}

	overview := httptest.NewRecorder()
	handler.Overview(overview, httptest.NewRequest(http.MethodGet, "/api/projects/current/overview?project_revision="+index.ProjectRevision, nil))
	if overview.Code != http.StatusOK || strings.Contains(overview.Body.String(), "package fixture") {
		t.Fatalf("overview = %d %s", overview.Code, overview.Body.String())
	}

	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	completion := project.FindingCompletion{FindingTarget: project.FindingTarget{ProjectID: index.ProjectID, Kind: "performance", ID: "slow-loop", Path: "main.go"}, CommitOID: "merged-commit"}
	if err := store.Complete([]project.FindingCompletion{completion}); err != nil {
		t.Fatal(err)
	}
	findings := httptest.NewRecorder()
	handler.Findings(findings, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings?project_revision="+index.ProjectRevision+"&confidence=suggested", nil))
	if findings.Code != http.StatusOK || !strings.Contains(findings.Body.String(), `"findings":[]`) {
		t.Fatalf("findings = %d %s", findings.Code, findings.Body.String())
	}
	var response struct {
		Completed []project.FindingCompletion `json:"completed"`
	}
	if err := json.Unmarshal(findings.Body.Bytes(), &response); err != nil || len(response.Completed) != 1 || response.Completed[0] != completion {
		t.Fatalf("finding completion response: %s, %v", findings.Body, err)
	}

	for _, call := range []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{name: "overview", call: func(response *httptest.ResponseRecorder) {
			handler.Overview(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/overview?project_revision=stale", nil))
		}},
		{name: "findings", call: func(response *httptest.ResponseRecorder) {
			handler.Findings(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings?project_revision=stale", nil))
		}},
		{name: "triage", call: func(response *httptest.ResponseRecorder) {
			handler.UpdateFindingStatus(response, httptest.NewRequest(http.MethodPatch, "/api/projects/current/findings/finding:test", bytes.NewBufferString(`{"project_revision":"stale","status":"dismissed"}`)))
		}},
		{name: "issue task", call: func(response *httptest.ResponseRecorder) {
			handler.IssueTask(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings/finding:test/task?project_revision=stale", nil))
		}},
		{name: "issue task for an unlisted issue", call: func(response *httptest.ResponseRecorder) {
			request := httptest.NewRequest(http.MethodGet, "/api/projects/current/findings/missing/task?project_revision="+index.ProjectRevision, nil)
			request.SetPathValue("findingID", "missing")
			handler.IssueTask(response, request)
		}},
		{name: "issues task", call: func(response *httptest.ResponseRecorder) {
			handler.IssuesTask(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings/task?id=a&id=b&project_revision=stale", nil))
		}},
		{name: "issues task for unlisted issues", call: func(response *httptest.ResponseRecorder) {
			handler.IssuesTask(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings/task?id=a&id=b&project_revision="+index.ProjectRevision, nil))
		}},
	} {
		t.Run(call.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			call.call(response)
			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
			}
			assertStructuredError(t, response)
		})
	}
}

func TestIssuesTaskRefusesASelectionItCannotFixTogether(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := manager.Index()
	response := httptest.NewRecorder()
	handler := NewProjectHandler(manager, service)
	handler.IssuesTask(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/findings/task?project_revision="+index.ProjectRevision, nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "open issues of one file") {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	assertStructuredError(t, response)
}

func TestProjectIndexAPIsRequireAnActiveProject(t *testing.T) {
	root := t.TempDir()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, nil)
	for _, test := range []struct {
		name string
		call func(*httptest.ResponseRecorder)
	}{
		{name: "index", call: func(response *httptest.ResponseRecorder) {
			handler.Index(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/index", nil))
		}},
		{name: "reindex", call: func(response *httptest.ResponseRecorder) {
			handler.Reindex(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/reindex", bytes.NewBufferString(`{}`)))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.call(response)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
			}
			assertStructuredError(t, response)
		})
	}
}

func newIndexedProjectHandler(t *testing.T, root string) *ProjectHandler {
	t.Helper()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	return NewProjectHandler(manager, nil)
}

func writeProjectHandlerFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertStructuredError(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Type        string `json:"type"`
		Message     string `json:"message"`
		UserMessage string `json:"user_message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Type == "" || body.Message == "" || body.UserMessage == "" {
		t.Fatalf("unstructured error = %+v", body)
	}
}
