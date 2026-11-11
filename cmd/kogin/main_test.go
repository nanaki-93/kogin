package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/launcher"
)

func TestProjectFolderDefaultsToTheCurrentFolder(t *testing.T) {
	folder := t.TempDir()
	t.Chdir(folder)
	canonical, _ := filepath.EvalSymlinks(folder)
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.project != canonical || !opts.open || opts.url != launcher.DefaultURL {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	other := t.TempDir()
	if opts, err := parseOptions([]string{"--no-open", other}, io.Discard); err != nil || opts.open || filepath.Base(opts.project) != filepath.Base(other) {
		t.Fatalf("explicit folder: %+v %v", opts, err)
	}
	file := filepath.Join(folder, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{file}, {filepath.Join(folder, "missing")}, {folder, other}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRunRemembersTheOpenedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	token := filepath.Join(t.TempDir(), "daemon-token")
	t.Setenv("KOGIN_TOKEN_FILE", token)
	if err := os.WriteFile(token, []byte("launch-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, _ := filepath.EvalSymlinks(t.TempDir())
	next, _ := filepath.EvalSymlinks(t.TempDir())
	if err := launcher.RememberProject(previous); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = io.WriteString(w, `{"status":"ok","version":"dev"}`)
		case "/api/models":
			_, _ = io.WriteString(w, `{"scopes":{},"config_file":""}`)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"path": next})
		}
	}))
	defer server.Close()
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--no-open", "--url", server.URL, next}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := launcher.LastProject(); got != next {
		t.Fatalf("last project = %q, want %q", got, next)
	}
}
