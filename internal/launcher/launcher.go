// Package launcher opens Kogin on a project for the kogin command and
// the desktop app: it attaches to the daemon that answers at the address, or
// starts one beside the program when none does, and makes the project its
// current one. A daemon it started is stopped with the session.
package launcher

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultURL is the daemon's default address; only there does a launcher
	// start a daemon when none answers.
	DefaultURL = "http://127.0.0.1:9090"
	// DaemonBinary is the daemon program a launcher starts.
	DaemonBinary = "kogin-daemon"
)

// Folder resolves a project folder to its absolute, symlink-free path.
func Folder(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	if info, err := os.Stat(canonical); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", path)
	}
	return canonical, nil
}

// Options says which daemon to use and where a started one writes.
type Options struct {
	// URL is the daemon's address, DefaultURL when empty.
	URL string
	// Project is the folder to open, as Folder returns it.
	Project string
	// Notes receives what the launcher does: starting the daemon and the
	// configuration it reads.
	Notes io.Writer
	// Stdout and Stderr receive a started daemon's output.
	Stdout, Stderr io.Writer
	// LoginShellPath starts the daemon with the PATH of the user's login
	// shell. Apps opened from the Finder or a launcher get a minimal PATH,
	// without the folders where the agent CLIs, Go or Node are installed.
	LoginShellPath bool
}

// Session is a daemon with the project open in it.
type Session struct {
	Client *Client
	// Daemon is the daemon this session started, nil when it attached to a
	// running one.
	Daemon *Daemon
}

// Open connects to the daemon at opts.URL, starting one in the project folder
// when none answers at the default address, and makes the project its current
// one.
func Open(ctx context.Context, opts Options) (*Session, error) {
	url := opts.URL
	if url == "" {
		url = DefaultURL
	}
	session := &Session{Client: NewClient(url)}
	if !session.Client.Healthy(ctx) {
		if session.Client.Base != DefaultURL {
			return nil, fmt.Errorf("no daemon answers at %s; start it there or use the default address", url)
		}
		if err := session.start(ctx, opts); err != nil {
			return nil, err
		}
	}
	if err := session.Client.OpenProject(ctx, opts.Project); err != nil {
		session.Stop()
		return nil, err
	}
	return session, nil
}

func (s *Session) start(ctx context.Context, opts Options) error {
	binary, err := FindDaemon()
	if err != nil {
		return err
	}
	notes := writerOrDiscard(opts.Notes)
	fmt.Fprintf(notes, "Starting the Kogin daemon for %s…\n", opts.Project)
	fmt.Fprintln(notes, ConfigurationNote(opts.Project, binary))
	path := ""
	if opts.LoginShellPath {
		path = LoginShellPath(ctx)
	}
	daemon, err := StartDaemon(binary, opts.Project, path, writerOrDiscard(opts.Stdout), writerOrDiscard(opts.Stderr))
	if err != nil {
		return err
	}
	s.Daemon = daemon
	if err := s.Client.WaitHealthy(ctx, daemon); err != nil {
		daemon.Stop()
		return err
	}
	return nil
}

// Page is the Web UI's address.
func (s *Session) Page() string {
	return s.Client.Base + "/ui"
}

// Stop stops the daemon this session started; an attached daemon keeps running.
func (s *Session) Stop() {
	if s.Daemon != nil {
		s.Daemon.Stop()
	}
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// sameFolder reports whether a daemon's project path is folder.
func sameFolder(a, b string) bool {
	if a == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// trimBase drops trailing slashes from a daemon address.
func trimBase(url string) string {
	return strings.TrimRight(url, "/")
}
