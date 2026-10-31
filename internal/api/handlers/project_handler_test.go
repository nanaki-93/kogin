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
	"sync"
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

func TestFileAnalysisAPIsRequireRevisionAndExposeStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"high","title":"Ignored errors","summary":"Run ignores errors.","task_spec":{"schema_version":"2","target_path":"main.go","acceptance_criteria":["Return errors."],"non_goals":[]}}],"suggestions":[]}`}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedHandlerConfig(server.URL)
	cfg.Retry = config.RetryConfig{MaxRetries: 1, BackoffBase: 1, BackoffMax: 1}
	service, err := app.New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewRecorder()
	handler.FileAnalysis(missing, httptest.NewRequest(http.MethodGet, "/api/projects/current/files/analysis?path=main.go&project_revision="+index.ProjectRevision, nil))
	if missing.Code != http.StatusOK || !strings.Contains(missing.Body.String(), `"status":"missing"`) {
		t.Fatalf("missing analysis = %d %s", missing.Code, missing.Body.String())
	}
	stale := httptest.NewRecorder()
	handler.FileAnalysis(stale, httptest.NewRequest(http.MethodGet, "/api/projects/current/files/analysis?path=main.go&project_revision=sha256:stale", nil))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revision = %d %s", stale.Code, stale.Body.String())
	}
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

func TestSecurityScanIsRevisionGuardedSourceOnlyAndReportsUnavailableLanguages(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n")
	writeProjectHandlerFixture(t, root, "notes.txt", "run it\n")
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

	response := httptest.NewRecorder()
	handler.ScanSecurityFile(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/files/security-scan", bytes.NewBufferString(`{"path":"main.go","project_revision":"`+index.ProjectRevision+`"}`)))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "package fixture") || !strings.Contains(response.Body.String(), `"evidence_kind":"rule_match"`) {
		t.Fatalf("security scan = %d %s", response.Code, response.Body.String())
	}

	stale := httptest.NewRecorder()
	handler.ScanSecurityFile(stale, httptest.NewRequest(http.MethodPost, "/api/projects/current/files/security-scan", bytes.NewBufferString(`{"path":"main.go","project_revision":"stale"}`)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale security scan = %d %s", stale.Code, stale.Body.String())
	}

	unsupported := httptest.NewRecorder()
	handler.ScanSecurityFile(unsupported, httptest.NewRequest(http.MethodPost, "/api/projects/current/files/security-scan", bytes.NewBufferString(`{"path":"notes.txt","project_revision":"`+index.ProjectRevision+`"}`)))
	if unsupported.Code != http.StatusUnprocessableEntity || !strings.Contains(unsupported.Body.String(), "unavailable") {
		t.Fatalf("unsupported security scan = %d %s", unsupported.Code, unsupported.Body.String())
	}
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

func TestExecutionTrustIsExplicitAndRevisionBound(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "go.mod", "module fixture\n\ngo 1.22\n")
	writeProjectHandlerFixture(t, root, "main.go", "package fixture\n")
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
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)
	status := httptest.NewRecorder()
	handler.ExecutionTrust(status, httptest.NewRequest(http.MethodGet, "/api/projects/current/execution-trust?project_revision="+index.ProjectRevision, nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"trusted":false`) || !strings.Contains(status.Body.String(), `"go","test","./..."`) {
		t.Fatalf("untrusted scope = %d %s", status.Code, status.Body.String())
	}
	invalidScope := httptest.NewRecorder()
	handler.ExecutionTrust(invalidScope, httptest.NewRequest(http.MethodGet, "/api/projects/current/execution-trust?project_revision="+index.ProjectRevision+"&task_test_name=not-a-test", nil))
	if invalidScope.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope = %d %s", invalidScope.Code, invalidScope.Body.String())
	}
	denied := httptest.NewRecorder()
	handler.TrustProjectExecution(denied, httptest.NewRequest(http.MethodPost, "/api/projects/current/execution-trust", bytes.NewBufferString(`{"project_revision":"`+index.ProjectRevision+`","confirm":false}`)))
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("denied trust = %d %s", denied.Code, denied.Body.String())
	}
	trusted := httptest.NewRecorder()
	handler.TrustProjectExecution(trusted, httptest.NewRequest(http.MethodPost, "/api/projects/current/execution-trust", bytes.NewBufferString(`{"project_revision":"`+index.ProjectRevision+`","confirm":true}`)))
	if trusted.Code != http.StatusOK || !strings.Contains(trusted.Body.String(), `"trusted":true`) {
		t.Fatalf("trusted scope = %d %s", trusted.Code, trusted.Body.String())
	}
	service, err = app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	handler = NewProjectHandler(manager, service)
	restored := httptest.NewRecorder()
	handler.ExecutionTrust(restored, httptest.NewRequest(http.MethodGet, "/api/projects/current/execution-trust?project_revision="+index.ProjectRevision, nil))
	if restored.Code != http.StatusOK || !strings.Contains(restored.Body.String(), `"trusted":true`) {
		t.Fatalf("restarted service lost saved consent: %d %s", restored.Code, restored.Body.String())
	}
	revoked := httptest.NewRecorder()
	handler.TrustProjectExecution(revoked, httptest.NewRequest(http.MethodPost, "/api/projects/current/execution-trust", bytes.NewBufferString(`{"project_revision":"`+index.ProjectRevision+`","confirm":false}`)))
	if revoked.Code != http.StatusOK || !strings.Contains(revoked.Body.String(), `"trusted":false`) {
		t.Fatalf("revoked scope = %d %s", revoked.Code, revoked.Body.String())
	}
}

func TestDeclarationExplanationHandlerRejectsUnknownRequestFields(t *testing.T) {
	handler := &ProjectHandler{}
	response := httptest.NewRecorder()
	handler.ExplainDeclaration(
		response,
		httptest.NewRequest(http.MethodPost, "/api/projects/current/files/explanation", strings.NewReader(`{"project_id":"p","unknown":true}`)),
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	assertStructuredError(t, response)

	missingConfirmation := httptest.NewRecorder()
	handler.ExplainDeclaration(
		missingConfirmation,
		httptest.NewRequest(http.MethodPost, "/api/projects/current/files/explanation", strings.NewReader(`{"project_id":"p","project_revision":"r","base_file_hash":"h","target_path":"main.go"}`)),
	)
	if missingConfirmation.Code != http.StatusBadRequest {
		t.Fatalf("missing confirmation status = %d: %s", missingConfirmation.Code, missingConfirmation.Body.String())
	}
	assertStructuredError(t, missingConfirmation)
}

func TestSecurityReviewHandlerUsesStrictRevisionGuardedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"findings":[]}`}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := manager.Index()
	file, _ := manager.IndexedFile("main.go")
	handler := NewProjectHandler(manager, service)
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "unknown field", body: `{"project_id":"x","unknown":true}`, want: http.StatusBadRequest},
		{name: "missing confirmation", body: `{"project_id":"x","project_revision":"x","base_file_hash":"x","path":"main.go"}`, want: http.StatusBadRequest},
		{name: "stale revision", body: `{"project_id":"` + index.ProjectID + `","project_revision":"stale","base_file_hash":"` + file.ContentHash + `","path":"main.go","confirm_remote_provider":false}`, want: http.StatusConflict},
		{name: "valid", body: `{"project_id":"` + index.ProjectID + `","project_revision":"` + index.ProjectRevision + `","base_file_hash":"` + file.ContentHash + `","path":"main.go","confirm_remote_provider":false}`, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ReviewSecurityFile(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/security-review", strings.NewReader(test.body)))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if test.want != http.StatusOK {
				assertStructuredError(t, response)
			}
		})
	}
}

func TestDeclarationExplanationHandlerRequiresAnActiveProject(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/projects/current/files/explanation", strings.NewReader(`{"project_id":"project","project_revision":"revision","base_file_hash":"sha256:file","target_path":"main.go","confirm_remote_provider":false}`))

	NewProjectHandler(manager, service).ExplainDeclaration(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
	}
	assertStructuredError(t, response)
}

func TestDeclarationExplanationHandlerReturnsTheTransientContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"version":"v1","summary":"Runs.","behavior":[],"inputs":[],"outputs":[],"side_effects":[],"error_behavior":[]}`}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc Run() {}\n")
	writeProjectHandlerFixture(t, root, "asset.bin", string([]byte{0, 1, 2}))
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := manager.Index()
	file, _ := manager.IndexedFile("main.go")
	body, _ := json.Marshal(map[string]any{
		"project_id": index.ProjectID, "project_revision": index.ProjectRevision,
		"base_file_hash": file.ContentHash, "target_path": "main.go",
		"confirm_remote_provider": false,
	})
	response := httptest.NewRecorder()
	NewProjectHandler(manager, service).ExplainDeclaration(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/files/explanation", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var explanation app.DeclarationExplanation
	if err := json.NewDecoder(response.Body).Decode(&explanation); err != nil {
		t.Fatal(err)
	}
	if explanation.Anchor.Path != "main.go" || explanation.ContextManifest.Scope != "code" {
		t.Fatalf("explanation = %+v", explanation)
	}
	binaryFile, _ := manager.IndexedFile("asset.bin")
	unsupportedBody, _ := json.Marshal(map[string]any{
		"project_id": index.ProjectID, "project_revision": index.ProjectRevision,
		"base_file_hash": binaryFile.ContentHash, "target_path": "asset.bin",
		"confirm_remote_provider": false,
	})
	unsupported := httptest.NewRecorder()
	NewProjectHandler(manager, service).ExplainDeclaration(unsupported, httptest.NewRequest(http.MethodPost, "/api/projects/current/files/explanation", bytes.NewReader(unsupportedBody)))
	if unsupported.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported status = %d: %s", unsupported.Code, unsupported.Body.String())
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

type sseTestRecorder struct {
	mu      sync.Mutex
	header  http.Header
	code    int
	body    bytes.Buffer
	flushed chan string
}

func newSSETestRecorder() *sseTestRecorder {
	return &sseTestRecorder{
		header:  make(http.Header),
		code:    http.StatusOK,
		flushed: make(chan string, 8),
	}
}

func (r *sseTestRecorder) Header() http.Header {
	return r.header
}

func (r *sseTestRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.code = statusCode
}

func (r *sseTestRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}

func (r *sseTestRecorder) Flush() {
	r.mu.Lock()
	snapshot := r.body.String()
	r.mu.Unlock()
	select {
	case r.flushed <- snapshot:
	default:
	}
}

func TestProjectEventsSSEStream(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.py", "def run():\n    return 1\n")
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
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	handler := NewProjectHandler(manager, service)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/current/events", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")
	rec := newSSETestRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(rec, req)
	}()

	readySnapshot := <-rec.flushed
	if !strings.Contains(readySnapshot, "event: ready") {
		t.Fatalf("SSE initial flush missing ready event: %s", readySnapshot)
	}

	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	eventSnapshot := <-rec.flushed
	cancel()
	<-done

	if rec.code != http.StatusOK {
		t.Fatalf("SSE status = %d", rec.code)
	}
	if !strings.Contains(eventSnapshot, "event: update") || !strings.Contains(eventSnapshot, `"kind":"project_index"`) {
		t.Fatalf("SSE stream missing reindex update event: %s", eventSnapshot)
	}
}

func TestDocsRefreshHandlerUsesAStrictRevisionGuardedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs.","architecture":"One package.","components":[],"entry_points":[],"flows":[],"risks":[],"next_steps":[],"engineering_insight":null}`}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := manager.Index()
	handler := NewProjectHandler(manager, service)
	for _, test := range []struct {
		name, body string
		want       int
	}{
		{name: "unknown field", body: `{"project_id":"x","project_revision":"x","unknown":true}`, want: http.StatusBadRequest},
		{name: "stale revision", body: `{"project_id":"` + index.ProjectID + `","project_revision":"stale"}`, want: http.StatusConflict},
		{name: "valid", body: `{"project_id":"` + index.ProjectID + `","project_revision":"` + index.ProjectRevision + `"}`, want: http.StatusAccepted},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.RefreshDocs(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/docs/refresh", strings.NewReader(test.body)))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if test.want != http.StatusAccepted {
				assertStructuredError(t, response)
				return
			}
			var refresh app.DocsRefresh
			if err := json.NewDecoder(response.Body).Decode(&refresh); err != nil || refresh.Status != "running" {
				t.Fatalf("refresh = %+v, %v", refresh, err)
			}
			// A second start while it runs is refused, and the overview
			// reads how it ended.
			again := httptest.NewRecorder()
			handler.RefreshDocs(again, httptest.NewRequest(http.MethodPost, "/api/projects/current/docs/refresh", strings.NewReader(test.body)))
			if again.Code != http.StatusConflict && again.Code != http.StatusAccepted {
				t.Fatalf("second start = %d %s", again.Code, again.Body.String())
			}
			if err := service.ShutdownChangeWorkflows(t.Context()); err != nil {
				t.Fatal(err)
			}
			overview, err := service.ProjectOverview()
			if err != nil || overview.DocsRefresh == nil || overview.DocsRefresh.Status == "running" {
				t.Fatalf("overview refresh = %+v, %v", overview, err)
			}
		})
	}
}

func TestAskHandlerNeedsTheCurrentProjectAndAnAgentModel(t *testing.T) {
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc Run() {}\n")
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
	handler := NewProjectHandler(manager, service)
	for _, test := range []struct {
		name, body string
		want       int
	}{
		{name: "unknown field", body: `{"project_id":"x","project_revision":"x","question":"Why?","unknown":true}`, want: http.StatusBadRequest},
		{name: "stale revision", body: `{"project_id":"` + index.ProjectID + `","project_revision":"stale","question":"Why?"}`, want: http.StatusConflict},
		{name: "no question", body: `{"project_id":"` + index.ProjectID + `","project_revision":"` + index.ProjectRevision + `","question":" "}`, want: http.StatusBadRequest},
		{name: "review model without tools", body: `{"project_id":"` + index.ProjectID + `","project_revision":"` + index.ProjectRevision + `","question":"Why?"}`, want: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.Ask(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/ask", strings.NewReader(test.body)))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			assertStructuredError(t, response)
		})
	}
}
