// Package brand names the product and remembers the names it had before:
// Kogin was called Mini-Orca, and Kasuri for part of v6 development.
// Folders, files and environment variables made under an old name keep
// working; folders move to their new name the first time they are used.
package brand

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// Name is the product's name as people read it.
	Name = "Kogin"

	// MetadataDir is the project folder that holds the project's saved data.
	MetadataDir = ".kogin"

	// IgnoreFile lists, like .gitignore, files to leave out of the project.
	IgnoreFile = ".koginignore"

	// ConfigFolder is the folder name under the user's configuration
	// directories (~/.config, the platform configuration directory).
	ConfigFolder = "kogin"

	envPrefix = "KOGIN_"
)

// The names the product had before, newest first. Each list lines up with
// the one before it: index 0 is Kasuri, index 1 is Mini-Orca.
var (
	// LegacyNames are the product's earlier names.
	LegacyNames = []string{"Kasuri", "Mini-Orca"}
	// LegacyMetadataDirs are MetadataDir's earlier names.
	LegacyMetadataDirs = []string{".kasuri", ".mini-orca"}
	// LegacyIgnoreFiles are IgnoreFile's earlier names; they are still read.
	LegacyIgnoreFiles = []string{".kasuriignore", ".mini-orcaignore"}
	// LegacyConfigFolders are ConfigFolder's earlier names.
	LegacyConfigFolders = []string{"kasuri", "mini-orca"}

	legacyEnvPrefixes = []string{"KASURI_", "MINI_ORCA_"}
)

// MetadataDirs is MetadataDir followed by its earlier names.
func MetadataDirs() []string {
	return append([]string{MetadataDir}, LegacyMetadataDirs...)
}

// IsMetadataDir reports whether name is MetadataDir or one of its earlier
// names: folders that hold the product's own data, never the project's.
func IsMetadataDir(name string) bool {
	return name == MetadataDir || slices.Contains(LegacyMetadataDirs, name)
}

// IgnoreFiles is IgnoreFile followed by its earlier names.
func IgnoreFiles() []string {
	return append([]string{IgnoreFile}, LegacyIgnoreFiles...)
}

// ConfigFolders is ConfigFolder followed by its earlier names.
func ConfigFolders() []string {
	return append([]string{ConfigFolder}, LegacyConfigFolders...)
}

// LegacyEnvs are the earlier names of a KOGIN_ environment variable, newest
// first (KASURI_…, then MINI_ORCA_…).
func LegacyEnvs(name string) []string {
	rest, ok := strings.CutPrefix(name, envPrefix)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(legacyEnvPrefixes))
	for _, prefix := range legacyEnvPrefixes {
		names = append(names, prefix+rest)
	}
	return names
}

// LookupEnv reads a KOGIN_ environment variable, or the same setting under
// an earlier name (KASURI_…, then MINI_ORCA_…) when only that one is set.
func LookupEnv(name string) (string, bool) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true
	}
	for _, legacy := range LegacyEnvs(name) {
		if value, ok := os.LookupEnv(legacy); ok {
			return value, true
		}
	}
	return "", false
}

// Getenv is LookupEnv without the presence flag, like os.Getenv.
func Getenv(name string) string {
	value, _ := LookupEnv(name)
	return value
}

// MigrateDir moves a folder made under an old name to its new name (current)
// when current does not exist yet. The old names are tried in order and the
// first folder found moves; the others stay where they are. It returns the
// folder to use: current, or the old one when the move failed and the data
// is still there. An old path that is a symlink or not a folder is skipped.
func MigrateDir(current string, legacy ...string) (string, error) {
	if _, err := os.Lstat(current); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return current, nil
	}
	for _, old := range legacy {
		info, err := os.Lstat(old)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(current), 0o700); err != nil {
			return old, err
		}
		if err := os.Rename(old, current); err != nil {
			return old, err
		}
		return current, nil
	}
	return current, nil
}

// UserConfigDir is Kogin's folder in the platform configuration directory
// (~/Library/Application Support/kogin on macOS). A kasuri or mini-orca
// folder there from before the rename is moved to it the first time.
func UserConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	legacy := make([]string, 0, len(LegacyConfigFolders))
	for _, folder := range LegacyConfigFolders {
		legacy = append(legacy, filepath.Join(base, folder))
	}
	directory, _ := MigrateDir(filepath.Join(base, ConfigFolder), legacy...)
	return directory, nil
}
