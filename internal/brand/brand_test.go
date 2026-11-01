package brand

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupEnvFallsBackToTheMiniOrcaName(t *testing.T) {
	t.Setenv("KASURI_BRAND_TEST", "")
	os.Unsetenv("KASURI_BRAND_TEST")
	t.Setenv("MINI_ORCA_BRAND_TEST", "old")
	if value, ok := LookupEnv("KASURI_BRAND_TEST"); !ok || value != "old" {
		t.Fatalf("LookupEnv = %q, %v; want the legacy value", value, ok)
	}
	t.Setenv("KASURI_BRAND_TEST", "new")
	if got := Getenv("KASURI_BRAND_TEST"); got != "new" {
		t.Fatalf("Getenv = %q; the new name wins", got)
	}
	if _, ok := LegacyEnv("PATH"); ok {
		t.Fatal("only KASURI_ variables have a legacy name")
	}
}

func TestMigrateDirMovesTheLegacyFolderOnce(t *testing.T) {
	root := t.TempDir()
	current, legacy := filepath.Join(root, ".kasuri"), filepath.Join(root, ".mini-orca")
	if err := os.MkdirAll(filepath.Join(legacy, "analysis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "index.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := MigrateDir(current, legacy)
	if err != nil || got != current {
		t.Fatalf("MigrateDir = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(current, "index.json")); err != nil {
		t.Fatalf("data did not move: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy folder still there: %v", err)
	}

	// With both present, nothing moves.
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := MigrateDir(current, legacy); err != nil || got != current {
		t.Fatalf("second MigrateDir = %q, %v", got, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("a legacy folder next to an existing new one must stay: %v", err)
	}
}

func TestMigrateDirLeavesSymlinksAndFilesAlone(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, ".mini-orca")
	if err := os.Symlink(target, legacy); err != nil {
		t.Skip(err)
	}
	if got, err := MigrateDir(filepath.Join(root, ".kasuri"), legacy); err != nil || got != filepath.Join(root, ".kasuri") {
		t.Fatalf("MigrateDir = %q, %v", got, err)
	}
	if info, err := os.Lstat(legacy); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was touched: %v", err)
	}
}
