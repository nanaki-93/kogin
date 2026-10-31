package main

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/api"
	"github.com/nanaki-93/mini-orca/v2/internal/api/handlers"
	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/version"
)

func TestDaemonStatusReportsCanonicalVersion(t *testing.T) {
	root := t.TempDir()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.Host = "localhost:9090"
	testHTTPMux(service, manager).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status response = %d: %s", response.Code, response.Body.String())
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Version != version.Version {
		t.Fatalf("daemon version = %q, want %q", status.Version, version.Version)
	}
}

func TestLocalAPIPolicyRejectsBrowserAndInvalidContentBeforeRoutes(t *testing.T) {
	t.Setenv("MINI_ORCA_BIND_ADDRESS", "")
	mux := daemonTestMux(t)

	tests := []struct {
		name        string
		method      string
		path        string
		host        string
		origin      string
		webUIHeader string
		contentType string
		preflight   bool
		wantStatus  int
	}{
		{name: "forged host", method: http.MethodGet, path: "/health", host: "attacker.example", wantStatus: http.StatusForbidden},
		{name: "hostile origin mutation", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", origin: "https://attacker.example", contentType: "application/json", wantStatus: http.StatusForbidden},
		{name: "hostile origin with web UI header", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", origin: "https://attacker.example", webUIHeader: "1", contentType: "application/json", wantStatus: http.StatusForbidden},
		{name: "different local port origin", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", origin: "http://localhost:3000", webUIHeader: "1", contentType: "application/json", wantStatus: http.StatusForbidden},
		{name: "same origin without web UI header", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", origin: "http://localhost:9090", contentType: "application/json", wantStatus: http.StatusForbidden},
		{name: "browser preflight", method: http.MethodOptions, path: "/api/projects/restore", host: "localhost:9090", origin: "https://attacker.example", preflight: true, wantStatus: http.StatusForbidden},
		{name: "missing JSON content type", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", wantStatus: http.StatusUnsupportedMediaType},
		{name: "non JSON mutation", method: http.MethodPost, path: "/api/projects/restore", host: "localhost:9090", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"project_path":"ignored"}`))
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.webUIHeader != "" {
				request.Header.Set("X-Mini-Orca-Web-UI", test.webUIHeader)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.preflight {
				request.Header.Set("Access-Control-Request-Method", http.MethodPost)
			}

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("unexpected CORS response header: %q", response.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestLocalAPIPolicyAllowsSameOriginWebUIFetchWithCustomHeader(t *testing.T) {
	handlerCalled := false
	req := httptest.NewRequest(http.MethodPost, "/api/projects/restore", strings.NewReader(`{"project_path":"ignored"}`))
	req.Host = "127.0.0.1:9090"
	req.Header.Set("Origin", "http://127.0.0.1:9090")
	req.Header.Set("X-Mini-Orca-Web-UI", "1")
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	api.LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusNoContent)
	}), api.HostPolicy{}).ServeHTTP(res, req)
	if res.Code != http.StatusNoContent || !handlerCalled {
		t.Fatalf("same-origin web UI request status = %d, handler called = %t", res.Code, handlerCalled)
	}
}

// Browsers attach Origin to ES module script fetches. Only same-origin GET/HEAD
// requests for the Web UI shell and static assets may omit the Web UI header.
func TestLocalAPIPolicyAllowsSameOriginModuleAssetsOnly(t *testing.T) {
	mux := daemonTestMux(t)
	script, stylesheet := webUIBundlePaths(t)
	tests := []struct {
		name, method, path, origin, webUIHeader string
		want                                    int
	}{
		{name: "module script", method: http.MethodGet, path: script, origin: "http://127.0.0.1:9090", want: http.StatusOK},
		{name: "stylesheet head", method: http.MethodHead, path: stylesheet, origin: "http://127.0.0.1:9090", want: http.StatusOK},
		{name: "shell", method: http.MethodGet, path: "/ui", origin: "http://127.0.0.1:9090", want: http.StatusOK},
		{name: "cross-origin asset", method: http.MethodGet, path: script, origin: "http://evil.example", want: http.StatusForbidden},
		{name: "other loopback port", method: http.MethodGet, path: script, origin: "http://127.0.0.1:3000", want: http.StatusForbidden},
		{name: "https origin", method: http.MethodGet, path: script, origin: "https://127.0.0.1:9090", want: http.StatusForbidden},
		{name: "api read without header", method: http.MethodGet, path: "/api/projects/current", origin: "http://127.0.0.1:9090", want: http.StatusForbidden},
		{name: "asset-like prefix outside assets", method: http.MethodGet, path: "/ui/assetsx/main.js", origin: "http://127.0.0.1:9090", want: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Host = "127.0.0.1:9090"
			req.Header.Set("Origin", test.origin)
			if test.webUIHeader != "" {
				req.Header.Set("X-Mini-Orca-Web-UI", test.webUIHeader)
			}
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			if res.Code != test.want {
				t.Fatalf("%s %s with Origin %s = %d, want %d", test.method, test.path, test.origin, res.Code, test.want)
			}
		})
	}
}

func TestWebUIRouteAcceptsDeepLinkWorktreeQueryParameter(t *testing.T) {
	mux := daemonTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/ui?session_id=chg-1&worktree_path=/tmp/task-wt&file=main.go", nil)
	req.Host = "127.0.0.1:9090"
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /ui with worktree_path returned %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("unexpected Content-Type = %q", res.Header().Get("Content-Type"))
	}
}

func TestLocalAPIPolicyAllowsNativeDesktopCallsAndHealth(t *testing.T) {
	t.Setenv("MINI_ORCA_BIND_ADDRESS", "")
	mux := daemonTestMux(t)
	health := httptest.NewRecorder()
	healthRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRequest.Host = "localhost:9090"
	mux.ServeHTTP(health, healthRequest)
	if health.Code != http.StatusOK {
		t.Fatalf("health response = %d: %s", health.Code, health.Body.String())
	}

	for _, test := range []struct {
		name string
		path string
		body io.Reader
	}{
		{name: "JSON request with charset", path: "/api/projects/restore", body: strings.NewReader(`{"project_path":"ignored"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			handlerCalled := false
			nativeRequest := httptest.NewRequest(http.MethodPost, test.path, test.body)
			nativeRequest.Host = "127.0.0.1:9090"
			if test.body != nil {
				nativeRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
			}
			response := httptest.NewRecorder()
			api.LocalOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusNoContent)
			}), api.HostPolicy{}).ServeHTTP(response, nativeRequest)
			if response.Code != http.StatusNoContent || !handlerCalled {
				t.Fatalf("native request status = %d, handler called = %t", response.Code, handlerCalled)
			}
		})
	}
}

func TestExplicitExternalBindAllowsAddressesAndListedHostNames(t *testing.T) {
	for _, bindAddress := range []string{"0.0.0.0:9090", ":9090"} {
		t.Run(bindAddress, func(t *testing.T) {
			t.Setenv("MINI_ORCA_BIND_ADDRESS", bindAddress)
			t.Setenv("MINI_ORCA_ALLOWED_HOSTS", "desktop.example, other.example")
			mux := daemonTestMux(t)
			for host, want := range map[string]int{
				"desktop.example:9090":      http.StatusOK,
				"192.168.1.20:9090":         http.StatusOK,
				"[fd00::20]:9090":           http.StatusOK,
				"localhost:9090":            http.StatusOK,
				"rebound.example:9090":      http.StatusForbidden,
				"desktop.example.evil:9090": http.StatusForbidden,
			} {
				request := httptest.NewRequest(http.MethodGet, "/health", nil)
				request.Host = host
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				if response.Code != want {
					t.Fatalf("external bind Host %s = %d, want %d: %s", host, response.Code, want, response.Body.String())
				}
			}
		})
	}
}

func daemonTestMux(t *testing.T) http.Handler {
	t.Helper()
	return testHTTPMux(newTestService(t))
}

func newTestService(t *testing.T) (*app.Service, *project.Manager) {
	t.Helper()
	root := t.TempDir()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}
	return service, manager
}

func TestReleaseDocumentationUsesCanonicalVersion(t *testing.T) {
	root := filepath.Join("..", "..")
	for path, expected := range map[string]string{
		"docs/openapi.yaml":    "version: " + version.Version,
		"docs/api-contract.md": "**Version:** " + version.Version,
		"RELEASE_NOTES.md":     "## v" + version.Version + " —",
		"DOCKER.md":            "mini-orca:" + version.Version,
	} {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(data), expected) {
			t.Errorf("%s does not contain canonical version marker %q", path, expected)
		}
	}
}

type documentedRoute struct {
	method string
	path   string
}

func TestDocumentedRoutesAreHandledByDaemon(t *testing.T) {
	routes := openAPIRoutes(t)
	if got := apiContractRoutes(t); !sameRoutes(got, routes) {
		t.Fatalf("API contract routes = %v, want OpenAPI routes %v", got, routes)
	}

	root := t.TempDir()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}
	mux := testHTTPMux(service, manager)
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, requestPathForDocumentedRoute(route.path), strings.NewReader("{}"))
			// Pass the local request policy so the status reflects the route table.
			req.Host = "localhost:9090"
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code == http.StatusNotFound || response.Code == http.StatusMethodNotAllowed {
				t.Fatalf("documented route is not registered: status %d", response.Code)
			}
		})
	}
}

// The built bundle has content-hashed names, so tests read them from the
// embedded document instead of hard-coding them.
var webUIScriptPath, webUIStylesheetPath = func() (string, string) {
	res := httptest.NewRecorder()
	handlers.NewWebUIHandler(testWriteToken.value).Serve(res, httptest.NewRequest(http.MethodGet, "/ui", nil))
	body := res.Body.String()
	script := regexp.MustCompile(`<script type="module"[^>]* src="(/ui/assets/[^"]+\.js)"`).FindStringSubmatch(body)
	style := regexp.MustCompile(`<link rel="stylesheet"[^>]* href="(/ui/assets/[^"]+\.css)"`).FindStringSubmatch(body)
	if script == nil || style == nil {
		return "", ""
	}
	return script[1], style[1]
}()

func webUIBundlePaths(t *testing.T) (string, string) {
	t.Helper()
	if webUIScriptPath == "" || webUIStylesheetPath == "" {
		t.Fatal("embedded Web UI document does not reference a script and a stylesheet under /ui/assets/")
	}
	return webUIScriptPath, webUIStylesheetPath
}

func requestPathForDocumentedRoute(path string) string {
	switch path {
	case "/api/projects/current/files/info", "/api/projects/current/files/analysis", "/api/projects/current/context":
		return path + "?path=missing.go"
	case "/api/projects/current/overview", "/api/projects/current/findings", "/api/projects/current/scan":
		return path + "?project_revision=sha256:missing"
	case "/ui/assets/{path}":
		return webUIScriptPath
	default:
		return path
	}
}

func TestDaemonDoesNotRegisterBrowserUIRoutes(t *testing.T) {
	root := t.TempDir()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}
	mux := testHTTPMux(service, manager)
	for _, path := range []string{"/", "/static/js/chat.js", "/api/render/file-tree", "/api/tree/expand", "/api/files/view"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "localhost:9090"
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("browser route %s returned %d, want 404", path, response.Code)
		}
	}
}

func TestDaemonBindsToLoopbackUnlessExplicitlyOverridden(t *testing.T) {
	t.Setenv("MINI_ORCA_BIND_ADDRESS", "")
	if got := daemonAddress(); got != "127.0.0.1:9090" {
		t.Fatalf("default daemon address = %q", got)
	}
	t.Setenv("MINI_ORCA_BIND_ADDRESS", "0.0.0.0:9090")
	if got := daemonAddress(); got != "0.0.0.0:9090" {
		t.Fatalf("override daemon address = %q", got)
	}
}

func openAPIRoutes(t *testing.T) []documentedRoute {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	var routes []documentedRoute
	for path, methods := range spec.Paths {
		for method := range methods {
			if method == "parameters" {
				continue
			}
			routes = append(routes, documentedRoute{strings.ToUpper(method), path})
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].method+routes[i].path < routes[j].method+routes[j].path })
	return routes
}

func sameRoutes(left, right []documentedRoute) bool {
	if len(left) != len(right) {
		return false
	}
	copyRight := append([]documentedRoute(nil), right...)
	sort.Slice(copyRight, func(i, j int) bool {
		return copyRight[i].method+copyRight[i].path < copyRight[j].method+copyRight[j].path
	})
	for i := range left {
		if left[i] != copyRight[i] {
			return false
		}
	}
	return true
}

func apiContractRoutes(t *testing.T) []documentedRoute {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "api-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	var routes []documentedRoute
	for _, line := range strings.Split(string(data), "\n") {
		columns := strings.Split(line, "|")
		if len(columns) < 4 || !strings.HasPrefix(strings.TrimSpace(columns[1]), "GET") && !strings.HasPrefix(strings.TrimSpace(columns[1]), "POST") && !strings.HasPrefix(strings.TrimSpace(columns[1]), "PATCH") && !strings.HasPrefix(strings.TrimSpace(columns[1]), "DELETE") {
			continue
		}
		routes = append(routes, documentedRoute{
			method: strings.TrimSpace(columns[1]),
			path:   strings.Trim(strings.TrimSpace(columns[2]), "`"),
		})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].method+routes[i].path < routes[j].method+routes[j].path })
	return routes
}

func TestUnifiedAnalysisRoutesUseLocalOriginPolicy(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}
	mux := testHTTPMux(service, manager)
	for _, route := range []string{"/api/projects/current/analysis/selection", "/api/projects/current/analysis/preview", "/api/projects/current/analysis/run", "/api/projects/current/analysis/run/control"} {
		request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{}`))
		request.Host = "localhost:9090"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://untrusted.example")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("origin bypass %s: %d %s", route, response.Code, response.Body)
		}
	}
}
