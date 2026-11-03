package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestDocsRefreshHandlerUsesAStrictRevisionGuardedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs.","architecture":"One package.","components":[],"walkthroughs":[],"risks":[],"engineering_insight":null}`}}}})
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

func TestDocsComponentHandlerListsTheComponentsFilesAtTheCurrentRevision(t *testing.T) {
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
		name, query string
		want        int
	}{
		{name: "stale revision", query: "path=.&project_revision=stale", want: http.StatusConflict},
		{name: "unknown component", query: "path=nowhere&project_revision=" + index.ProjectRevision, want: http.StatusNotFound},
		{name: "root component", query: "path=.&project_revision=" + index.ProjectRevision, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.DocsComponent(response, httptest.NewRequest(http.MethodGet, "/api/projects/current/docs/component?"+test.query, nil))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if test.want != http.StatusOK {
				assertStructuredError(t, response)
				return
			}
			var files app.DocsComponentFiles
			if err := json.NewDecoder(response.Body).Decode(&files); err != nil || files.Path != "." || len(files.Files) != 1 || files.Files[0].Path != "main.go" {
				t.Fatalf("files = %+v, %v", files, err)
			}
		})
	}
}
