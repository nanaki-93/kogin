package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func newHealthHandlerFixture(t *testing.T) (*ProjectHandler, string) {
	t.Helper()
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "go.mod", "module fixture\n\ngo 1.22\n")
	writeProjectHandlerFixture(t, root, "main.go", "package main\n\nfunc main() { run() }\n\nfunc run() {}\n\nfunc unused() {}\n")
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
	return NewProjectHandler(manager, service), index.ProjectRevision
}

func TestProfileIsReadSavedAndValidated(t *testing.T) {
	handler, revision := newHealthHandlerFixture(t)
	read := httptest.NewRecorder()
	handler.Profile(read, httptest.NewRequest(http.MethodGet, "/api/projects/current/profile", nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"saved":false`) || !strings.Contains(read.Body.String(), `"finish_line":"medium"`) {
		t.Fatalf("profile = %d %s", read.Code, read.Body.String())
	}
	save := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.SaveProfile(response, httptest.NewRequest(http.MethodPost, "/api/projects/current/profile", bytes.NewBufferString(body)))
		return response
	}
	saved := save(`{"project_revision":"` + revision + `","profile":{"kind":"personal","shape":"cli","platforms":["macos"]}}`)
	var profile app.ProjectProfile
	if err := json.Unmarshal(saved.Body.Bytes(), &profile); saved.Code != http.StatusOK || err != nil || !profile.Saved || profile.Security != "basic" {
		t.Fatalf("saved = %d %s", saved.Code, saved.Body.String())
	}
	for name, test := range map[string]struct {
		body   string
		status int
	}{
		"unknown kind":     {`{"project_revision":"` + revision + `","profile":{"kind":"hobby"}}`, http.StatusBadRequest},
		"unknown field":    {`{"project_revision":"` + revision + `","profile":{"kind":"team","color":"red"}}`, http.StatusBadRequest},
		"stale revision":   {`{"project_revision":"sha256:stale","profile":{"kind":"team"}}`, http.StatusConflict},
		"missing revision": {`{"profile":{"kind":"team"}}`, http.StatusConflict},
	} {
		if response := save(test.body); response.Code != test.status {
			t.Errorf("%s: %d %s", name, response.Code, response.Body.String())
		}
	}
}

func TestHealthMeasuresTheProjectAndTestsNeedExecutionTrust(t *testing.T) {
	handler, revision := newHealthHandlerFixture(t)
	health := httptest.NewRecorder()
	handler.Health(health, httptest.NewRequest(http.MethodGet, "/api/projects/current/health?project_revision="+revision, nil))
	var body app.ProjectHealth
	if err := json.Unmarshal(health.Body.Bytes(), &body); health.Code != http.StatusOK || err != nil {
		t.Fatalf("health = %d %s", health.Code, health.Body.String())
	}
	if body.Code.SourceFiles != 1 || len(body.Unused) != 1 || body.Unused[0].Function != "unused" || body.Tests != nil || body.FinishLine.Done || len(body.Trend.Days) != 14 || len(body.Tasks) != 1 || body.Tasks[0].Kind != "unused" {
		t.Fatalf("health = %+v", body)
	}
	stale := httptest.NewRecorder()
	handler.Health(stale, httptest.NewRequest(http.MethodGet, "/api/projects/current/health?project_revision=sha256:stale", nil))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale health = %d", stale.Code)
	}
	run := httptest.NewRecorder()
	handler.RunHealthTests(run, httptest.NewRequest(http.MethodPost, "/api/projects/current/health/tests", bytes.NewBufferString(`{"project_revision":"`+revision+`"}`)))
	if run.Code != http.StatusForbidden || !strings.Contains(run.Body.String(), "Trust local execution") {
		t.Fatalf("untrusted run = %d %s", run.Code, run.Body.String())
	}
}
