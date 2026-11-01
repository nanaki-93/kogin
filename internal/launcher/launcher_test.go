package launcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"net/http/httptest"
)

func TestOpenAttachesToARunningDaemonAndSwitchesItsProject(t *testing.T) {
	token := filepath.Join(t.TempDir(), "daemon-token")
	t.Setenv("KASURI_TOKEN_FILE", token)
	if err := os.WriteFile(token, []byte("launch-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := &fakeDaemon{current: "/projects/other", restore: true}
	server := httptest.NewServer(daemon)
	defer server.Close()
	folder, err := Folder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := Open(t.Context(), Options{URL: server.URL + "/", Project: folder})
	if err != nil || session.Daemon != nil || daemon.current != folder {
		t.Fatalf("attach: %+v %v %+v", session, err, daemon)
	}
	if session.Page() != server.URL+"/ui" {
		t.Fatalf("page = %q", session.Page())
	}
	session.Stop() // An attached daemon keeps running.
}

func TestOpenStartsADaemonOnlyAtTheDefaultAddress(t *testing.T) {
	_, err := Open(t.Context(), Options{URL: "http://127.0.0.1:1", Project: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no daemon answers at http://127.0.0.1:1") {
		t.Fatalf("missing daemon: %v", err)
	}
}

func TestFolderResolvesExistingFoldersOnly(t *testing.T) {
	folder := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(folder)
	if got, err := Folder(link); err != nil || got != canonical {
		t.Fatalf("symlink: %q %v", got, err)
	}
	file := filepath.Join(folder, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(folder, "missing")} {
		if _, err := Folder(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

// lockedBuffer collects a process's output while it runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartedDaemonRunsInTheProjectWithTheGivenPath(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	var output lockedBuffer
	daemon, err := StartDaemon(script(t, "echo \"$PATH\"\npwd\n"), project, "/login/bin:/bin:/usr/bin", &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	<-daemon.Done()
	if daemon.Err() != nil || output.String() != "/login/bin:/bin:/usr/bin\n"+project+"\n" {
		t.Fatalf("output %q, exit %v", output.String(), daemon.Err())
	}
	daemon.Stop() // An exited daemon needs no signal and must not block.
}

func TestStopAsksTheDaemonToShutDown(t *testing.T) {
	var output lockedBuffer
	daemon, err := StartDaemon(script(t, "trap 'echo stopping; exit 0' TERM\necho ready\nwhile true; do sleep 0.05; done\n"), t.TempDir(), "", &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(output.String(), "ready") {
		time.Sleep(10 * time.Millisecond)
	}
	if daemon.Err() != nil {
		t.Fatal("a running daemon reported an exit")
	}
	daemon.Stop()
	if daemon.Err() != nil || !strings.Contains(output.String(), "stopping") {
		t.Fatalf("stop: %q %v", output.String(), daemon.Err())
	}
}

func TestLoginShellPathComesFirstAndKeepsTheRest(t *testing.T) {
	// The fake shell prints a banner, then runs the command it was given with
	// the PATH its startup files would set.
	shell := script(t, "echo 'Welcome back'\nPATH=/login/bin:/usr/bin\nexport PATH\nshift 3\nexec /bin/sh -c \"$1\"\n")
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", "/usr/bin:/extra/bin")
	if got := LoginShellPath(t.Context()); got != "/login/bin:/usr/bin:/extra/bin" {
		t.Fatalf("login PATH = %q", got)
	}
	t.Setenv("SHELL", script(t, "exit 1\n"))
	if got := LoginShellPath(t.Context()); got != "" {
		t.Fatalf("failing shell gave %q", got)
	}
}

func TestMarkedPathNeedsBothMarkers(t *testing.T) {
	for output, want := range map[string]string{
		"banner\n" + pathMarker + "/a:/b" + pathMarker: "/a:/b",
		pathMarker + "/a:/b":                           "",
		"no markers":                                   "",
	} {
		if got := markedPath(output); got != want {
			t.Errorf("%q: %q", output, got)
		}
	}
	if got := withPath([]string{"HOME=/h", "PATH=/old"}, "/new"); strings.Join(got, ",") != "HOME=/h,PATH=/new" {
		t.Fatalf("environment = %v", got)
	}
}

func TestTheDesktopAppRemembersAnExistingProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	if LastProject() != "" {
		t.Fatal("a project before any was opened")
	}
	project, _ := filepath.EvalSymlinks(t.TempDir())
	if err := RememberProject(project); err != nil {
		t.Fatal(err)
	}
	if got := LastProject(); got != project {
		t.Fatalf("last project = %q", got)
	}
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	if got := LastProject(); got != "" {
		t.Fatalf("a removed folder came back: %q", got)
	}
	path, _ := desktopStatePath()
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LastProject(); got != "" {
		t.Fatalf("a corrupt file gave %q", got)
	}
}

func TestTheDesktopLogKeepsThePreviousLaunch(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "logs")
	for _, text := range []string{"first launch\n", "second launch\n"} {
		log, err := OpenDesktopLog(folder)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.WriteString(text); err != nil {
			t.Fatal(err)
		}
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := os.ReadFile(filepath.Join(folder, "daemon.log"))
	previous, _ := os.ReadFile(filepath.Join(folder, "daemon.previous.log"))
	if string(current) != "second launch\n" || string(previous) != "first launch\n" {
		t.Fatalf("logs: %q %q", current, previous)
	}
}
