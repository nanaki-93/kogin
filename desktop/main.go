// Command kasuri-desktop is Kasuri as a desktop app: one window on the
// Web UI, with the daemon started beside it for the last project (or the
// folder the user chooses) and stopped when the app quits. A daemon that was
// already running is used as it is and keeps running.
package main

import (
	_ "embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed icon.png
var icon []byte

func main() {
	shell := newShell()
	app := application.New(application.Options{
		Name:        "Kasuri",
		Description: "Local-first coding assistant",
		Icon:        icon,
		// The status page shown until the Web UI loads.
		Assets: application.AssetOptions{Handler: shell},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// A second launch shows this window instead of starting another
		// daemon session.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID:               "io.github.nanaki-93.kasuri",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { shell.show() },
		},
		// After the window is gone: the daemon gets its time to stop
		// running work without the window hanging on screen.
		PostShutdown: shell.stop,
	})
	shell.app = app
	app.Menu.Set(shell.menu())
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		shell.openWindow()
		go shell.start()
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// menu is the app's menu bar: the standard App, Edit and Window menus, File to
// open another project, View to reload and zoom, Help to reach the daemon log.
func (s *shell) menu() *application.Menu {
	menu := application.NewMenu()
	menu.AddRole(application.AppMenu)

	file := menu.AddSubmenu("File")
	s.openProject = file.Add("Open Project…").SetAccelerator("CmdOrCtrl+O").SetEnabled(false)
	s.openProject.OnClick(func(*application.Context) { go s.chooseProject() })
	s.openInBrowser = file.Add("Open in Browser").SetEnabled(false)
	s.openInBrowser.OnClick(func(*application.Context) { s.openBrowser() })
	file.AddSeparator()
	file.AddRole(application.CloseWindow)

	menu.AddRole(application.EditMenu)

	view := menu.AddSubmenu("View")
	view.AddRole(application.Reload)
	view.AddSeparator()
	view.AddRole(application.ResetZoom)
	view.AddRole(application.ZoomIn)
	view.AddRole(application.ZoomOut)
	view.AddSeparator()
	view.AddRole(application.ToggleFullscreen)

	menu.AddRole(application.WindowMenu)

	help := menu.AddSubmenu("Help")
	help.Add("Show Daemon Log").OnClick(func(*application.Context) { s.showLog() })
	return menu
}
