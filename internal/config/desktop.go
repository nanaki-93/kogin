package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nanaki-93/kogin/v2/internal/brand"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

// DesktopState is what the desktop app keeps between launches.
type DesktopState struct {
	// Project is the folder it opened last.
	Project string `json:"project"`
}

// DesktopStateFile is where the desktop app and daemon record the most
// recently opened project folder.
func DesktopStateFile() (string, error) {
	directory, err := brand.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the desktop app's settings: %w", err)
	}
	return filepath.Join(directory, "desktop.json"), nil
}

// RememberProject saves the folder the desktop app opens next time.
func RememberProject(folder string) error {
	path, err := DesktopStateFile()
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
