package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
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
