# Desktop app

Kasuri.app is the Web UI in a window of its own, with the daemon started
beside it. Nothing about the interface changes: the window shows the same pages
the daemon serves at `http://127.0.0.1:9090/ui`. The app is built with
[Wails v3](https://wails.io) and lives in `desktop/`, a Go module of its own so
the daemon's build and tests never need it.

## Build and install (macOS)

```sh
just desktop            # or: make install-desktop
```

This builds `build/Kasuri.app` (the app and its daemon, ad-hoc signed) and
copies it into `/Applications`, the folder the Finder shows as Applications
(`~/Applications` when you cannot write there; `DESKTOP_INSTALL_DIR` picks
another folder). Open it from Launchpad, Spotlight or that folder. It needs Go
and the Xcode Command Line Tools (`xcode-select --install`), and runs on macOS
13 or newer, like Go 1.27. `make desktop-app` builds the app without installing
it; `make desktop` builds only the program (`build/kasuri-desktop`), which
starts the `kasuri-daemon` it finds beside it or on `PATH`.

The app reads the daemon's configuration the same way the launcher does:
`KASURI_CONFIG`, else `~/.config/kasuri/config.yaml` (`make install` puts
the repository's `config.yaml` there once), and each project's own
`.kasuri/config.yaml`. See [CONFIG.md](CONFIG.md).

## What it does

- **Launch.** The window says what it is doing while the daemon starts. The app
  opens the project it opened last; the first time, it asks for the project
  folder. When a daemon already answers at `127.0.0.1:9090` (one started with
  `kasuri` or `just dev`, say), the app uses it and leaves it running when it
  quits; when that daemon is another version, the app says so, since the
  window then shows that version's pages. Otherwise it starts its own daemon in
  the project folder.
- **Your PATH.** An app opened from the Finder or the Dock gets a minimal
  `PATH`. The app starts its daemon with the `PATH` of your login shell, so the
  agent CLIs (`claude`, `agy`, `pi`), Git, Go and Node are found as in a terminal.
- **Open another project.** File → Open Project… (Cmd+O) picks a folder and
  switches the daemon to it; the app reopens that project next time.
- **Quit.** Closing the window or Cmd+Q quits. A daemon the app started gets the
  same graceful stop as Ctrl+C on `kasuri`: running workflows are stopped and
  the daemon exits.
- **If the daemon stops.** The window says so, with Show Log and Quit.
- **Notifications.** While the window is not in front, macOS tells you when
  agents finish a task (ready for your review, changes requested, failed, out
  of date, paused at a plan limit) and when an analysis run ends (finished,
  finished with failures, failed, paused). Clicking one brings the window
  forward on that task or on Analysis. In front, the Web UI's own messages say
  the same. The first launch asks for permission; System Settings →
  Notifications → Kasuri turns them off. Notifications need the app bundle
  (`make desktop-app`); the bare `make desktop` program runs without them, and
  the log says why when they are off.
- **Keys.** Cmd+[ and Cmd+] go back and forward (Alt+Left and Alt+Right on
  Linux), as do the mouse's side buttons. Cmd+R reloads, Cmd+0, Cmd++
  and Cmd+- zoom. File → Open in Browser opens the same page in your browser.
  A second launch brings the open window forward.

## Files

| What | Where |
| --- | --- |
| The daemon's log | `~/Library/Logs/Kasuri/daemon.log`, the previous launch's in `daemon.previous.log` (Help → Show Daemon Log). On Linux, `~/.cache/kasuri/`. |
| The project it reopens | `desktop.json` in the user configuration folder's `kasuri` (`~/Library/Application Support/kasuri/` on macOS), beside the daemon's write token. |

The app icon's source is `desktop/macos/icon.svg`; `desktop/macos/icon.icns`
and `desktop/icon.png` are rendered from it.

## Other platforms

`make desktop` builds the program on Linux too (it needs the GTK 4 and
WebKitGTK 6.0 development packages); only the macOS app bundle is packaged.
`make desktop-check` vets and builds the app for macOS and Linux without cgo,
so it runs anywhere Go does; `make check` and `./scripts/validate.sh` include
it, and `./scripts/quality.sh` runs staticcheck on the macOS build.

## Not yet

- No menu bar icon: closing the window quits the app, so notifications come
  only while it is open.
- The window shows a page served over `http://127.0.0.1`; links that would open
  a new browser window do nothing.
- The app is ad-hoc signed for the Mac that built it. Sharing it with others
  needs a Developer ID signature and notarization.
