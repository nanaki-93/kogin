package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyMetadataMovesMiniOrcaData(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mini-orca", "analysis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mini-orca", "index.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateLegacyMetadata(root)
	if err != nil || !moved {
		t.Fatalf("MigrateLegacyMetadata = %v, %v; want moved", moved, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kogin", "index.json")); err != nil {
		t.Fatalf("index did not move: %v", err)
	}
	if moved, err := MigrateLegacyMetadata(root); err != nil || moved {
		t.Fatalf("second call = %v, %v; want nothing to move", moved, err)
	}
	if _, err := MetadataPath(root, ".kogin", "index.json"); err != nil {
		t.Fatalf("moved data is not metadata: %v", err)
	}
}

func TestMigrateLegacyMetadataWithoutLegacyFolder(t *testing.T) {
	if moved, err := MigrateLegacyMetadata(t.TempDir()); err != nil || moved {
		t.Fatalf("MigrateLegacyMetadata = %v, %v", moved, err)
	}
}

func TestMigrateLegacyMetadataMovesKasuriData(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kasuri", "analysis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kasuri", "index.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateLegacyMetadata(root)
	if err != nil || !moved {
		t.Fatalf("MigrateLegacyMetadata = %v, %v; want moved", moved, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kogin", "index.json")); err != nil {
		t.Fatalf("index did not move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kasuri")); !os.IsNotExist(err) {
		t.Fatalf(".kasuri is still there: %v", err)
	}
}

func TestCurrentMetadataPathMapsEveryEarlierFolder(t *testing.T) {
	for in, want := range map[string]string{
		".kasuri/agents/a.json":    ".kogin/agents/a.json",
		".mini-orca/agents/a.json": ".kogin/agents/a.json",
		".kogin/agents/a.json":     ".kogin/agents/a.json",
		"src/.kasuri/x":            "src/.kasuri/x",
	} {
		if got := CurrentMetadataPath(in); got != want {
			t.Fatalf("CurrentMetadataPath(%q) = %q; want %q", in, got, want)
		}
	}
}
