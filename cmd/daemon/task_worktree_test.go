package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// A task created in its own worktree is served through that worktree's scope:
// read, trusted to run checks, and fed by edits made in that worktree.
func TestTaskInItsOwnWorktreeIsServedThroughItsScope(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"still_valid":true,"reason":"README still lacks the usage section."}`}}}})
	}))
	defer provider.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".kogin/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "README.md", ".gitignore"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=" + os.DevNull, "commit", "-m", "Initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s, %v", output, err)
		}
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	root = manager.Root()
	if err := manager.Set(root, &project.Analysis{Path: root, Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ModelScopes.Bug.APIBaseURL = provider.URL
	service, err := app.New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := manager.Index()
	if _, err := service.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	mux := testHTTPMux(service, manager)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *strings.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = strings.NewReader(string(data))
		} else {
			reader = strings.NewReader("")
		}
		request := httptest.NewRequest(method, path, reader)
		request.Host = "localhost:9090"
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	response := call("POST", "/api/projects/current/changes", map[string]any{"project_id": index.ProjectID, "project_revision": index.ProjectRevision, "kind": "feature", "title": "Document usage", "paths": []string{"README.md"}, "confirm_execution": true})
	var opened struct {
		app.ChangeSession
		WorktreePath string   `json:"worktree_path"`
		Warnings     []string `json:"warnings"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &opened) != nil || opened.WorktreePath == "" || len(opened.Warnings) != 0 {
		t.Fatalf("open in a worktree: %d %s", response.Code, response.Body)
	}
	scope := "worktree_path=" + url.QueryEscape(opened.WorktreePath)
	own := url.Values{"project_id": {opened.ProjectID}, "project_revision": {opened.ProjectRevision}}.Encode()
	if got := call("GET", "/api/projects/current/changes/"+opened.ID+"?"+own+"&"+scope, nil); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"freshness":"current"`) {
		t.Fatalf("task through its worktree: %d %s", got.Code, got.Body)
	}
	main := url.Values{"project_id": {index.ProjectID}, "project_revision": {index.ProjectRevision}}.Encode()
	if got := call("GET", "/api/projects/current/changes/"+opened.ID+"?"+main, nil); got.Code == http.StatusOK {
		t.Fatal("the task was also served by the main checkout")
	}
	trust := call("GET", "/api/projects/current/execution-trust?project_revision="+url.QueryEscape(opened.ProjectRevision)+"&"+scope, nil)
	if trust.Code != http.StatusOK || !strings.Contains(trust.Body.String(), `"trusted":true`) {
		t.Fatalf("worktree trust: %d %s", trust.Code, trust.Body)
	}

	if err := os.WriteFile(filepath.Join(opened.WorktreePath, "README.md"), []byte("# Fixture\nEdited in the worktree.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// An edit made in the task's own worktree becomes part of its proposal.
	adopted := call("GET", "/api/projects/current/changes/"+opened.ID+"?"+own+"&"+scope, nil)
	var session app.ChangeSession
	if adopted.Code != http.StatusOK || json.Unmarshal(adopted.Body.Bytes(), &session) != nil || session.Freshness != "current" || len(session.Changes) != 1 || !strings.Contains(session.Changes[0].Content, "Edited in the worktree.") || session.Worktree == nil {
		t.Fatalf("worktree edit through its scope: %d %s", adopted.Code, adopted.Body)
	}
	identity := map[string]any{"project_id": session.ProjectID, "project_revision": session.ProjectRevision, "revision": session.Revision, "hash": session.Hash}
	if reanalyzed := call("POST", "/api/projects/current/changes/"+opened.ID+"/reanalyze?"+scope, identity); reanalyzed.Code != http.StatusBadRequest {
		t.Fatalf("re-analysis of a task in its own worktree: %d %s", reanalyzed.Code, reanalyzed.Body)
	}
}
