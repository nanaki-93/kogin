package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// DesktopState is what the desktop app keeps between launches.
type DesktopState = config.DesktopState

// StartupProject is the folder the desktop app opens on launch: a running
// daemon's active project when one already answers at url, otherwise
// LastProject.
func StartupProject(ctx context.Context, url string) string {
	if url == "" {
		url = DefaultURL
	}
	if current := NewClient(url).CurrentProject(ctx); current != "" {
		return current
	}
	return LastProject()
}

// LastProject is the folder the desktop app opened last, when it still exists.
func LastProject() string {
	path, err := config.DesktopStateFile()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state DesktopState
	if json.Unmarshal(data, &state) != nil || !filepath.IsAbs(state.Project) {
		return ""
	}
	folder, err := Folder(state.Project)
	if err != nil {
		return ""
	}
	return folder
}

// RememberProject saves the folder the desktop app opens next time.
func RememberProject(folder string) error {
	return config.RememberProject(folder)
}

// DesktopLogFolder is where the desktop app writes the daemon's log:
// ~/Library/Logs/Kogin on macOS, the user cache folder elsewhere.
func DesktopLogFolder() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Logs", "Kogin"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "kogin"), nil
}

// OpenDesktopLog starts this launch's daemon log in folder, keeping the
// previous launch's beside it as daemon.previous.log.
func OpenDesktopLog(folder string) (*os.File, error) {
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(folder, "daemon.log")
	if err := os.Rename(path, filepath.Join(folder, "daemon.previous.log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
