package config

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/nanaki-93/mini-orca/v2/internal/brand"
)

// ConfigEnv names an explicit configuration file; it replaces the search.
const ConfigEnv = "KASURI_CONFIG"

// Candidates lists, in order, the daemon configuration files a daemon run
// from executable reads: KASURI_CONFIG (or MINI_ORCA_CONFIG) alone when set,
// otherwise the user's ~/.config/kasuri/config.yaml, the platform
// configuration directory's kasuri/config.yaml, the same two under their
// mini-orca names from before the rename, and the config.yaml beside the
// executable (its symlinks resolved). A project's own configuration is
// ProjectConfigPath inside it, applied on top (see LoadProject); a
// config.yaml in the project folder belongs to the project, not to Kasuri.
func Candidates(executable string) []string {
	if explicit := brand.Getenv(ConfigEnv); explicit != "" {
		return []string{explicit}
	}
	var list []string
	home, homeErr := os.UserHomeDir()
	dir, dirErr := os.UserConfigDir()
	for _, folder := range []string{brand.ConfigFolder, brand.LegacyConfigFolder} {
		if homeErr == nil {
			list = append(list, filepath.Join(home, ".config", folder, "config.yaml"))
		}
		if dirErr == nil {
			list = append(list, filepath.Join(dir, folder, "config.yaml"))
		}
	}
	if executable != "" {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			list = append(list, filepath.Join(filepath.Dir(resolved), "config.yaml"))
		}
		list = append(list, filepath.Join(filepath.Dir(executable), "config.yaml"))
	}
	unique := make([]string, 0, len(list))
	for _, path := range list {
		if path = filepath.Clean(path); !slices.Contains(unique, path) {
			unique = append(unique, path)
		}
	}
	return unique
}

// Find returns the first candidate that is a regular file, or "" when none
// is and the built-in local defaults apply. An explicit KASURI_CONFIG is
// returned even when missing, so loading it reports the error.
func Find(executable string) string {
	candidates := Candidates(executable)
	if brand.Getenv(ConfigEnv) != "" {
		return candidates[0]
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}
