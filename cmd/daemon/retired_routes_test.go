package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetiredWorkflowRoutesAreUnavailable(t *testing.T) {
	mux := daemonTestMux(t)
	for _, route := range []struct{ method, path string }{
		{"POST", "chat/sessions"},
		{"POST", "chat/sessions/old/messages"},
		{"PATCH", "drafts/old"},
		{"POST", "drafts/old/checks"},
		{"GET", "drafts/old/benchmarks"},
		{"POST", "apply"},
		{"POST", "undo"},
		{"GET", "analysis-job"},
		{"POST", "analysis-job/resume"},
		{"GET", "performance"},
		{"GET", "performance/context"},
		{"POST", "performance-job"},
		// Single-file security work; every analysis run's stages do it now.
		{"POST", "files/security-scan"},
		{"POST", "security-review"},
		// Features that only the terminal UI exposed.
		{"GET", "missions"},
		{"GET", "live-sessions"},
		{"GET", "cloud-copies"},
		{"GET", "agent-triggers"},
		{"GET", "integrations"},
		{"GET", "apis"},
		{"GET", "api-actions"},
		{"GET", "agent-access"},
		{"GET", "remote-agents"},
		{"GET", "delegations"},
		{"GET", "research"},
		{"GET", "attachments"},
		{"GET", "artifacts"},
		{"GET", "memory"},
	} {
		t.Run(route.method+"/"+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, "/api/projects/current/"+route.path, strings.NewReader("{}"))
			request.Host = "localhost:9090"
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired route returned %d: %s", response.Code, response.Body)
			}
		})
	}
}
