package launcher

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/api"
)

// fakeDaemon answers like the daemon's project routes and records writes.
type fakeDaemon struct {
	mu      sync.Mutex
	current string
	writes  []string
	restore bool
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]string{"path": f.current})
		return
	}
	if r.Header.Get(api.WriteTokenHeader) != "launch-token" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"user_message": "Reload the Web UI to get this daemon's write token."})
		return
	}
	var body struct {
		ProjectPath string `json:"project_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.writes = append(f.writes, r.URL.Path)
	if r.URL.Path == "/api/projects/restore" && !f.restore {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.current = body.ProjectPath
	_ = json.NewEncoder(w).Encode(map[string]string{"path": f.current})
}

func TestOpenProjectSwitchesARunningDaemonOnlyWhenNeeded(t *testing.T) {
	token := filepath.Join(t.TempDir(), "daemon-token")
	t.Setenv("MINI_ORCA_TOKEN_FILE", token)
	daemon := &fakeDaemon{current: "/projects/other"}
	server := httptest.NewServer(daemon)
	defer server.Close()
	client := NewClient(server.URL + "/")
	folder := t.TempDir()
	if err := client.OpenProject(t.Context(), folder); err == nil || !strings.Contains(err.Error(), "write token") {
		t.Fatalf("missing token: %v", err)
	}
	if err := os.WriteFile(token, []byte("launch-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.OpenProject(t.Context(), folder); err != nil || daemon.current != folder || strings.Join(daemon.writes, ",") != "/api/projects/restore,/api/projects/import" {
		t.Fatalf("new project not imported: %v %+v", err, daemon)
	}
	daemon.writes, daemon.restore, daemon.current = nil, true, "/projects/other"
	if err := client.OpenProject(t.Context(), folder); err != nil || strings.Join(daemon.writes, ",") != "/api/projects/restore" {
		t.Fatalf("known project not restored: %v %+v", err, daemon.writes)
	}
	daemon.writes = nil
	if err := client.OpenProject(t.Context(), folder); err != nil || len(daemon.writes) != 0 {
		t.Fatalf("the current project was reopened: %v %+v", err, daemon.writes)
	}
}

func TestStartedDaemonThatExitsEarlyIsReported(t *testing.T) {
	daemon := &Daemon{done: make(chan struct{}), err: os.ErrProcessDone}
	close(daemon.done)
	client := NewClient("http://127.0.0.1:1")
	if err := client.WaitHealthy(t.Context(), daemon); err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("early exit: %v", err)
	}
	daemon.Stop() // An exited daemon needs no signal and must not block.
}

func TestConfigNoteNamesTheRunningDaemonsConfiguration(t *testing.T) {
	for body, want := range map[string]string{
		`{"scopes":{},"config_file":"/home/me/.config/mini-orca/config.yaml"}`: "uses /home/me/.config/mini-orca/config.yaml",
		`{"scopes":{},"config_file":""}`:                                       "built-in local model defaults",
		`{"scopes":{}}`:                                                        "Restart the daemon",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		if got := NewClient(server.URL).ConfigNote(t.Context()); !strings.Contains(got, want) {
			t.Errorf("%s: %q", body, got)
		}
		server.Close()
	}
}

func TestConfigurationNoteNamesTheProjectFileFirst(t *testing.T) {
	home, project, install := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("MINI_ORCA_CONFIG", "")
	binary := filepath.Join(install, DaemonBinary)
	if note := ConfigurationNote(project, binary); !strings.Contains(note, "built-in local defaults") || !strings.Contains(note, ".mini-orca/config.yaml") {
		t.Fatalf("no configuration: %q", note)
	}
	if err := os.WriteFile(filepath.Join(install, "config.yaml"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if note := ConfigurationNote(project, binary); !strings.HasPrefix(note, "Configuration: "+filepath.Join(install, "config.yaml")) || !strings.Contains(note, "set this project up") {
		t.Fatalf("daemon configuration: %q", note)
	}
	if err := os.MkdirAll(filepath.Join(project, ".mini-orca"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".mini-orca", "config.yaml"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if note := ConfigurationNote(project, binary); note != "Project configuration: "+filepath.Join(project, ".mini-orca", "config.yaml") {
		t.Fatalf("project configuration: %q", note)
	}
}

func TestVersionIsWhatTheDaemonReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","version":"5.0.0"}`)
	}))
	defer server.Close()
	if version, err := NewClient(server.URL).Version(t.Context()); err != nil || version != "5.0.0" {
		t.Fatalf("version = %q, %v", version, err)
	}
	if _, err := NewClient("http://127.0.0.1:1").Version(t.Context()); err == nil {
		t.Fatal("no daemon reported a version")
	}
}
