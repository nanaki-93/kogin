package brand

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupEnvFallsBackToEarlierNamesNewestFirst(t *testing.T) {
	for _, name := range []string{"KOGIN_BRAND_TEST", "KASURI_BRAND_TEST", "MINI_ORCA_BRAND_TEST"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	if _, ok := LookupEnv("KOGIN_BRAND_TEST"); ok {
		t.Fatal("nothing is set yet")
	}
	t.Setenv("MINI_ORCA_BRAND_TEST", "mini-orca")
	if value, ok := LookupEnv("KOGIN_BRAND_TEST"); !ok || value != "mini-orca" {
		t.Fatalf("LookupEnv = %q, %v; want the Mini-Orca value", value, ok)
	}
	t.Setenv("KASURI_BRAND_TEST", "kasuri")
	if got := Getenv("KOGIN_BRAND_TEST"); got != "kasuri" {
		t.Fatalf("Getenv = %q; Kasuri's name wins over Mini-Orca's", got)
	}
	t.Setenv("KOGIN_BRAND_TEST", "kogin")
	if got := Getenv("KOGIN_BRAND_TEST"); got != "kogin" {
		t.Fatalf("Getenv = %q; the current name wins", got)
	}
	if got := LegacyEnvs("PATH"); got != nil {
		t.Fatalf("only KOGIN_ variables have earlier names, got %v", got)
	}
}

func TestMetadataDirNames(t *testing.T) {
	for _, name := range []string{".kogin", ".kasuri", ".mini-orca"} {
		if !IsMetadataDir(name) {
			t.Fatalf("%s is the product's folder", name)
		}
	}
	if IsMetadataDir(".git") || IsMetadataDir("kogin") {
		t.Fatal("only the product's own folders count")
	}
	if got := IgnoreFiles(); len(got) != 3 || got[0] != ".koginignore" {
		t.Fatalf("IgnoreFiles = %v", got)
	}
}

func TestMigrateDirMovesTheLegacyFolderOnce(t *testing.T) {
	root := t.TempDir()
	current, legacy := filepath.Join(root, ".kogin"), filepath.Join(root, ".mini-orca")
	if err := os.MkdirAll(filepath.Join(legacy, "analysis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "index.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := MigrateDir(current, filepath.Join(root, ".kasuri"), legacy)
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

func TestMigrateDirPrefersTheNewestEarlierName(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".kogin")
	kasuri, miniOrca := filepath.Join(root, ".kasuri"), filepath.Join(root, ".mini-orca")
	for _, dir := range []string{kasuri, miniOrca} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "from"), []byte(filepath.Base(dir)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := MigrateDir(current, kasuri, miniOrca); err != nil || got != current {
		t.Fatalf("MigrateDir = %q, %v", got, err)
	}
	if data, err := os.ReadFile(filepath.Join(current, "from")); err != nil || string(data) != ".kasuri" {
		t.Fatalf("moved %q, %v; want the .kasuri folder", data, err)
	}
	if _, err := os.Stat(miniOrca); err != nil {
		t.Fatalf("the older folder stays where it is: %v", err)
	}
}

func TestMigrateDirLeavesSymlinksAndFilesAlone(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, ".kasuri")
	if err := os.Symlink(target, legacy); err != nil {
		t.Skip(err)
	}
	if got, err := MigrateDir(filepath.Join(root, ".kogin"), legacy); err != nil || got != filepath.Join(root, ".kogin") {
		t.Fatalf("MigrateDir = %q, %v", got, err)
	}
	if info, err := os.Lstat(legacy); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was touched: %v", err)
	}
}
