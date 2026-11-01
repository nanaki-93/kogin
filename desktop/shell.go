package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/launcher"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// startupWait bounds how long quitting waits for a launch in progress, so a
// daemon it started is stopped rather than left running.
const startupWait = 30 * time.Second

// statusURL is the status page, served inside the app.
const statusURL = "/"

// shell is the desktop app around one daemon session.
type shell struct {
	app           *application.App
	window        *application.WebviewWindow
	openProject   *application.MenuItem
	openInBrowser *application.MenuItem

	mu sync.Mutex
	// launching closes when the daemon launch in progress ends; nil until
	// one starts.
	launching chan struct{}
	state     launchStatus
	session   *launcher.Session
	log       *os.File
}

func newShell() *shell {
	return &shell{state: launchStatus{Title: "Starting Mini-Orca…"}}
}

// openWindow shows the status page; the Web UI replaces it once the daemon
// answers.
func (s *shell) openWindow() {
	background := application.NewRGB(10, 11, 13)
	if !s.app.Env.IsDarkMode() {
		background = application.NewRGB(239, 239, 242)
	}
	s.window = s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Mini-Orca",
		Width:            1360,
		Height:           880,
		MinWidth:         900,
		MinHeight:        600,
		InitialPosition:  application.WindowCentered,
		BackgroundColour: background,
		URL:              statusURL,
		// Runs after every page load, the Web UI's included.
		JS: navigationKeys,
	})
}

// start opens the last project, or asks for one, in the daemon and shows the
// Web UI. Failures are shown and end the app.
func (s *shell) start() {
	project := launcher.LastProject()
	if project == "" {
		folder, err := s.firstProject()
		if err != nil {
			s.fail("Mini-Orca could not show the folder dialog", err.Error())
			return
		}
		if folder == "" {
			s.app.Quit()
			return
		}
		project = folder
	}
	s.status("Starting Mini-Orca", "Opening "+project+"…")
	launching := make(chan struct{})
	s.mu.Lock()
	s.launching = launching
	s.mu.Unlock()
	session, err := s.open(project)
	close(launching)
	if err != nil {
		s.fail("Mini-Orca could not start", err.Error())
		return
	}
	s.mu.Lock()
	s.session = session
	s.mu.Unlock()
	s.remember(project)
	s.openProject.SetEnabled(true)
	s.openInBrowser.SetEnabled(true)
	s.window.SetURL(session.Page())
	if session.Daemon != nil {
		go s.watch(session.Daemon)
	}
}

// open attaches to a running daemon or starts one, with its output in this
// launch's log.
func (s *shell) open(project string) (*launcher.Session, error) {
	folder, err := launcher.DesktopLogFolder()
	if err != nil {
		return nil, err
	}
	log, err := launcher.OpenDesktopLog(folder)
	if err != nil {
		return nil, fmt.Errorf("start the daemon log: %w", err)
	}
	s.mu.Lock()
	s.log = log
	s.mu.Unlock()
	session, err := launcher.Open(s.app.Context(), launcher.Options{
		URL:            os.Getenv("MINI_ORCA_URL"),
		Project:        project,
		Notes:          log,
		Stdout:         log,
		Stderr:         log,
		LoginShellPath: true,
	})
	if err == nil && session.Daemon == nil {
		fmt.Fprintf(log, "Using the daemon already running at %s; its log is where it was started, and it keeps running when Mini-Orca quits.\n", session.Client.Base)
	}
	return session, err
}

// chooseProject switches the daemon to another folder.
func (s *shell) chooseProject() {
	session := s.current()
	if session == nil {
		return
	}
	path, err := s.askFolder("Choose the project to open.")
	if err != nil {
		s.alert("Mini-Orca could not show the folder dialog", err.Error())
		return
	}
	if path == "" {
		return
	}
	folder, err := launcher.Folder(path)
	if err != nil {
		s.alert("Mini-Orca could not open "+filepath.Base(path), err.Error())
		return
	}
	s.status("Opening "+filepath.Base(folder), folder)
	s.window.SetURL(statusURL)
	if err := session.Client.OpenProject(s.app.Context(), folder); err != nil {
		s.alert("Mini-Orca could not open "+filepath.Base(folder), err.Error())
	} else {
		s.remember(folder)
	}
	s.window.SetURL(session.Page())
}

// firstProject asks for the project on a first launch until the user picks
// a folder or cancels (empty).
func (s *shell) firstProject() (string, error) {
	detail := "Mini-Orca opens the folder of a project you work on."
	for {
		s.prompt("Choose the project to open", detail)
		path, err := s.askFolder("Choose the project Mini-Orca opens.")
		if err != nil || path == "" {
			return "", err
		}
		folder, err := launcher.Folder(path)
		if err == nil {
			return folder, nil
		}
		detail = err.Error()
	}
}

// askFolder asks for a folder; empty when the user cancels.
func (s *shell) askFolder(message string) (string, error) {
	dialog := s.app.Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		SetTitle("Open Project").
		SetMessage(message).
		SetButtonText("Open")
	if s.window != nil {
		dialog = dialog.AttachToWindow(s.window)
	}
	return dialog.PromptForSingleSelection()
}

// watch ends the app when the daemon it started stops on its own.
func (s *shell) watch(daemon *launcher.Daemon) {
	<-daemon.Done()
	if s.app.Context().Err() != nil {
		return // Quitting stopped it.
	}
	s.fail("The Mini-Orca daemon stopped", fmt.Sprintf("It exited unexpectedly (%v).", daemon.Err()))
}

// fail shows what went wrong, with the daemon log at hand, and quits.
func (s *shell) fail(title, detail string) {
	if s.app.Context().Err() != nil {
		return // The app is quitting; nothing to show.
	}
	s.setState(launchStatus{Title: title, Detail: detail, Failed: true})
	s.window.SetURL(statusURL)
	dialog := s.app.Dialog.Error().SetTitle(title).SetMessage(detail + s.logHint())
	if s.logPath() != "" {
		dialog.AddButton("Show Log").OnClick(func() {
			s.showLog()
			s.app.Quit()
		})
	}
	dialog.AddButton("Quit").SetAsDefault().OnClick(s.app.Quit)
	dialog.AttachToWindow(s.window).Show()
}

// alert reports a failure the app can carry on after.
func (s *shell) alert(title, message string) {
	s.app.Dialog.Warning().SetTitle(title).SetMessage(message).AttachToWindow(s.window).Show()
}

// status says what the launch is doing on the status page.
func (s *shell) status(title, detail string) {
	s.setState(launchStatus{Title: title, Detail: detail})
}

// prompt says what the launch waits for from the user.
func (s *shell) prompt(title, detail string) {
	s.setState(launchStatus{Title: title, Detail: detail, Waiting: true})
}

func (s *shell) setState(state launchStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

func (s *shell) launch() launchStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *shell) remember(folder string) {
	if err := launcher.RememberProject(folder); err != nil {
		s.alert("Mini-Orca will not reopen this project", err.Error())
	}
}

func (s *shell) openBrowser() {
	if session := s.current(); session != nil {
		if err := s.app.Browser.OpenURL(session.Page()); err != nil {
			s.alert("The browser did not open", err.Error())
		}
	}
}

func (s *shell) showLog() {
	path := s.logPath()
	if path == "" {
		s.alert("No daemon log yet", "The log starts when Mini-Orca starts the daemon.")
		return
	}
	if err := s.app.Browser.OpenFile(path); err != nil {
		s.alert("The log did not open", fmt.Sprintf("%v\n\nIt is at %s.", err, path))
	}
}

// show brings the window forward, for a second launch.
func (s *shell) show() {
	if s.window == nil {
		return
	}
	s.window.UnMinimise()
	s.window.Show()
	s.window.Focus()
}

// stop ends the session after the window closed: a daemon this app started
// stops running work and exits. A launch still in progress finishes first,
// so the daemon it started is stopped too.
func (s *shell) stop() {
	s.mu.Lock()
	launching := s.launching
	s.mu.Unlock()
	if launching != nil {
		select {
		case <-launching:
		case <-time.After(startupWait):
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.Stop()
	}
	if s.log != nil {
		_ = s.log.Close()
	}
}

func (s *shell) current() *launcher.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

func (s *shell) logPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log == nil {
		return ""
	}
	return s.log.Name()
}

func (s *shell) logHint() string {
	if path := s.logPath(); path != "" {
		return "\n\nThe daemon's log is at " + path + "."
	}
	return ""
}
