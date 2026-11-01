package launcher

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

const (
	// Startup indexes the project before the daemon listens; large projects
	// take a while.
	startupTimeout = 2 * time.Minute
	stopGrace      = 20 * time.Second
	// A login shell that reads slow startup files must not hold up the launch.
	loginShellTimeout = 5 * time.Second
	pathMarker        = "__KASURI_PATH__"
)

// Daemon is a daemon process a launcher started.
type Daemon struct {
	cmd *exec.Cmd
	// done closes when the process exits; err is then its exit result.
	done chan struct{}
	err  error
}

// Done closes when the daemon exits.
func (d *Daemon) Done() <-chan struct{} { return d.done }

// Err is the daemon's exit result once Done is closed.
func (d *Daemon) Err() error {
	select {
	case <-d.done:
		return d.err
	default:
		return nil
	}
}

// FindDaemon finds the daemon beside this program, then on PATH.
func FindDaemon() (string, error) {
	if self, err := os.Executable(); err == nil {
		folders := []string{filepath.Dir(self)}
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			folders = append(folders, filepath.Dir(resolved))
		}
		for _, folder := range folders {
			sibling := filepath.Join(folder, DaemonBinary)
			if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
				return sibling, nil
			}
		}
	}
	if path, err := exec.LookPath(DaemonBinary); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("%s was not found beside this program or on PATH; run `just build` in the Kasuri repository", DaemonBinary)
}

// StartDaemon runs binary in the project folder, so the folder is its
// project. A non-empty path replaces its PATH.
func StartDaemon(binary, project, path string, stdout, stderr io.Writer) (*Daemon, error) {
	cmd := exec.Command(binary)
	cmd.Dir = project
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if path != "" {
		cmd.Env = withPath(os.Environ(), path)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	daemon := &Daemon{cmd: cmd, done: make(chan struct{})}
	go func() {
		daemon.err = cmd.Wait()
		close(daemon.done)
	}()
	return daemon, nil
}

// Stop asks the daemon to shut down and waits for it; it has its own budget
// to drain requests and stop running workflows.
func (d *Daemon) Stop() {
	select {
	case <-d.done:
		return
	default:
	}
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = d.cmd.Process.Kill()
	}
	select {
	case <-d.done:
	case <-time.After(stopGrace):
		_ = d.cmd.Process.Kill()
		<-d.done
	}
}

// ConfigurationNote names the configuration a daemon started from binary uses
// for project: the project's own file, the daemon's, or the defaults.
func ConfigurationNote(project, binary string) string {
	daemon := config.Find(binary)
	if path, err := config.ProjectFile(project); err == nil {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return "Project configuration: " + path
		}
	}
	if daemon != "" {
		return "Configuration: " + daemon + "\nThe Web UI offers to set this project up with its own configuration."
	}
	return fmt.Sprintf("No configuration found, so models use the built-in local defaults (%s).\nThe Web UI's setup saves this project's configuration in %s; a daemon-wide one goes in:\n  %s", config.DefaultProviderAPIBaseURL, config.ProjectConfigPath, strings.Join(config.Candidates(binary), "\n  "))
}

// LoginShellPath is the PATH the user's login shell sets up, followed by the
// entries of this process's PATH it lacks; empty when the shell cannot say.
func LoginShellPath(ctx context.Context) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	shell := os.Getenv("SHELL")
	switch {
	case shell != "":
	case runtime.GOOS == "darwin":
		shell = "/bin/zsh" // The login shell macOS gives new users.
	default:
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()
	// Interactive too: many people set PATH in .zshrc or .bashrc. The markers
	// skip anything the startup files print.
	cmd := exec.CommandContext(ctx, shell, "-i", "-l", "-c", `printf '%s%s%s' "`+pathMarker+`" "$PATH" "`+pathMarker+`"`)
	cmd.Stdin, cmd.Stderr = nil, io.Discard
	// A background job the startup files leave behind must not keep the
	// output open past the timeout.
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return mergePath(markedPath(string(output)), os.Getenv("PATH"))
}

// markedPath is the text between the first two path markers.
func markedPath(output string) string {
	_, rest, found := strings.Cut(output, pathMarker)
	if !found {
		return ""
	}
	path, _, found := strings.Cut(rest, pathMarker)
	if !found {
		return ""
	}
	return strings.TrimSpace(path)
}

// mergePath keeps the login PATH's order and appends the current entries it
// lacks.
func mergePath(login, current string) string {
	if login == "" {
		return ""
	}
	entries := filepath.SplitList(login)
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry] = true
	}
	for _, entry := range filepath.SplitList(current) {
		if entry != "" && !seen[entry] {
			entries = append(entries, entry)
			seen[entry] = true
		}
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

// withPath replaces PATH in an environment.
func withPath(environment []string, path string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "PATH=") {
			result = append(result, entry)
		}
	}
	return append(result, "PATH="+path)
}
