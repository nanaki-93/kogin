// Package brand names the product and remembers the name it had before:
// Kasuri was called Mini-Orca. Folders, files and environment variables made
// under the old name keep working; folders move to their new name the first
// time they are used.
package brand

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Name is the product's name as people read it.
	Name = "Kasuri"
	// LegacyName is the name the product had before Kasuri.
	LegacyName = "Mini-Orca"

	// MetadataDir is the project folder that holds the project's saved data.
	MetadataDir = ".kasuri"
	// LegacyMetadataDir is MetadataDir's name before the rename.
	LegacyMetadataDir = ".mini-orca"

	// IgnoreFile lists, like .gitignore, files to leave out of the project.
	IgnoreFile = ".kasuriignore"
	// LegacyIgnoreFile is IgnoreFile's name before the rename; still read.
	LegacyIgnoreFile = ".mini-orcaignore"

	// ConfigFolder is the folder name under the user's configuration
	// directories (~/.config, the platform configuration directory).
	ConfigFolder = "kasuri"
	// LegacyConfigFolder is ConfigFolder's name before the rename.
	LegacyConfigFolder = "mini-orca"

	envPrefix       = "KASURI_"
	legacyEnvPrefix = "MINI_ORCA_"
)

// LegacyEnv is the Mini-Orca name of a KASURI_ environment variable.
func LegacyEnv(name string) (string, bool) {
	if !strings.HasPrefix(name, envPrefix) {
		return "", false
	}
	return legacyEnvPrefix + strings.TrimPrefix(name, envPrefix), true
}

// LookupEnv reads a KASURI_ environment variable, or the same setting under
// its Mini-Orca name (MINI_ORCA_…) when only that one is set.
func LookupEnv(name string) (string, bool) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true
	}
	if legacy, ok := LegacyEnv(name); ok {
		return os.LookupEnv(legacy)
	}
	return "", false
}

// Getenv is LookupEnv without the presence flag, like os.Getenv.
func Getenv(name string) string {
	value, _ := LookupEnv(name)
	return value
}

// MigrateDir moves a folder made under the old name (legacy) to its new
// name (current) when current does not exist yet. It returns the folder to
// use: current, or legacy when the move failed and the data is still there.
// A legacy path that is a symlink or not a folder is left alone.
func MigrateDir(current, legacy string) (string, error) {
	if _, err := os.Lstat(current); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return current, nil
	}
	info, err := os.Lstat(legacy)
	if err != nil || !info.IsDir() {
		return current, nil
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
		return legacy, err
	}
	if err := os.Rename(legacy, current); err != nil {
		return legacy, err
	}
	return current, nil
}

// UserConfigDir is Kasuri's folder in the platform configuration directory
// (~/Library/Application Support/kasuri on macOS). A mini-orca folder there
// from before the rename is moved to it the first time.
func UserConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	directory, _ := MigrateDir(filepath.Join(base, ConfigFolder), filepath.Join(base, LegacyConfigFolder))
	return directory, nil
}
