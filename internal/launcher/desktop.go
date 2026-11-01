package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

// DesktopState is what the desktop app keeps between launches.
type DesktopState struct {
	// Project is the folder it opened last.
	Project string `json:"project"`
}

func desktopStatePath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the desktop app's settings: %w", err)
	}
	return filepath.Join(directory, "mini-orca", "desktop.json"), nil
}

// LastProject is the folder the desktop app opened last, when it still exists.
func LastProject() string {
	path, err := desktopStatePath()
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
	path, err := desktopStatePath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(DesktopState{Project: folder})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return storage.WriteFile(path, data, 0o600)
}

// DesktopLogFolder is where the desktop app writes the daemon's log:
// ~/Library/Logs/Mini-Orca on macOS, the user cache folder elsewhere.
func DesktopLogFolder() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Logs", "Mini-Orca"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "mini-orca"), nil
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
