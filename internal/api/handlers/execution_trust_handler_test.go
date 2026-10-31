package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

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
