package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

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
