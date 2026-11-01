package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// testWriteToken guards the daemon in tests; testHTTPMux sends it with every
// request that does not set its own, as the Web UI does.
var testWriteToken = writeToken{value: "test-write-token", file: "/tmp/kasuri-test/daemon-token"}

func testHTTPMux(service *app.Service, manager *project.Manager) http.Handler {
	mux := newHTTPMux(service, manager, testWriteToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, set := r.Header[api.WriteTokenHeader]; !set {
			r.Header.Set(api.WriteTokenHeader, testWriteToken.value)
		}
		mux.ServeHTTP(w, r)
	})
}

func TestWriteTokenIsSavedPrivatelyPerLaunch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "daemon-token")
	t.Setenv("KASURI_TOKEN_FILE", path)
	first, err := newWriteToken()
	if err != nil || first.file != path || len(first.value) < 20 {
		t.Fatalf("token: %+v %v", first, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v %v", info, err)
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != first.value {
		t.Fatal("token file does not hold the token")
	}
	second, err := newWriteToken()
	if err != nil || second.value == first.value {
		t.Fatal("token was reused across launches")
	}
	t.Setenv("KASURI_TOKEN_FILE", "relative/token")
	if _, err := newWriteToken(); err == nil {
		t.Fatal("relative token path accepted")
	}
}

func TestDaemonRequiresTheWriteTokenToChangeState(t *testing.T) {
	service, manager := newTestService(t)
	mux := newHTTPMux(service, manager, testWriteToken)
	for name, header := range map[string]string{"missing": "", "wrong": "other"} {
		request := httptest.NewRequest(http.MethodPost, "/api/projects/current/changes", strings.NewReader("{}"))
		request.Host = "127.0.0.1:9090"
		request.Header.Set("Content-Type", "application/json")
		if header != "" {
			request.Header.Set(api.WriteTokenHeader, header)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), testWriteToken.file) {
			t.Fatalf("%s token: %d %s", name, response.Code, response.Body)
		}
	}
	page := httptest.NewRequest(http.MethodGet, "/ui", nil)
	page.Host = "127.0.0.1:9090"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, page)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `content="`+testWriteToken.value+`"`) {
		t.Fatalf("the Web UI document lacks the token: %d", response.Code)
	}
}
