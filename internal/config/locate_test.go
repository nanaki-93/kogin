package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSearchesTheUserThenTheInstallNotTheProject(t *testing.T) {
	home, project, install := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv(ConfigEnv, "")
	link := filepath.Join(t.TempDir(), "kogin-daemon")
	binary := filepath.Join(install, "kogin-daemon")
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write(binary)
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	// A config.yaml in the folder the daemon runs in belongs to that project.
	write(filepath.Join(project, "config.yaml"))
	t.Chdir(project)
	if got := Find(link); got != "" {
		t.Fatalf("nothing to find: %q", got)
	}
	// A daemon on PATH through a symlink still reads the config beside it.
	installed := write(filepath.Join(install, "config.yaml"))
	if got, want := Find(link), installed; !sameFile(t, got, want) {
		t.Fatalf("installed config: %q", got)
	}
	user := write(filepath.Join(home, ".config", "kogin", "config.yaml"))
	if got := Find(link); got != user {
		t.Fatalf("user config: %q", got)
	}
	explicit := filepath.Join(home, "missing.yaml")
	t.Setenv(ConfigEnv, explicit)
	if got := Find(link); got != explicit {
		t.Fatalf("explicit config: %q", got)
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func TestLoadConfigRecordsItsSourceAndRefusesAMissingExplicitFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("model_scopes:\n  analyze: {provider: agy, model: a}\n  bug: {provider: agy, model: b}\n  code: {provider: agy, model: c}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnv, path)
	cfg, err := LoadConfig()
	if err != nil || cfg.Source != path {
		t.Fatalf("source: %+v %v", cfg, err)
	}
	t.Setenv(ConfigEnv, filepath.Join(dir, "missing.yaml"))
	if _, err := LoadConfig(); err == nil {
		t.Fatal("a missing KOGIN_CONFIG fell back to defaults")
	}
}

func TestFindReadsTheMiniOrcaConfigFromBeforeTheRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv(ConfigEnv, "")
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	legacy := write(filepath.Join(home, ".config", "mini-orca", "config.yaml"))
	if got := Find(""); got != legacy {
		t.Fatalf("legacy user config: %q", got)
	}
	kasuri := write(filepath.Join(home, ".config", "kasuri", "config.yaml"))
	if got := Find(""); got != kasuri {
		t.Fatalf("the Kasuri config comes before Mini-Orca's: %q", got)
	}
	current := write(filepath.Join(home, ".config", "kogin", "config.yaml"))
	if got := Find(""); got != current {
		t.Fatalf("the Kogin config comes first: %q", got)
	}
	os.Unsetenv(ConfigEnv)
	explicit := filepath.Join(home, "old-explicit.yaml")
	t.Setenv("MINI_ORCA_CONFIG", explicit)
	if got := Find(""); got != explicit {
		t.Fatalf("MINI_ORCA_CONFIG still selects the file: %q", got)
	}
}
