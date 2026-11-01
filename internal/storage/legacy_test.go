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
	if _, err := os.Stat(filepath.Join(root, ".kasuri", "index.json")); err != nil {
		t.Fatalf("index did not move: %v", err)
	}
	if moved, err := MigrateLegacyMetadata(root); err != nil || moved {
		t.Fatalf("second call = %v, %v; want nothing to move", moved, err)
	}
	if _, err := MetadataPath(root, ".kasuri", "index.json"); err != nil {
		t.Fatalf("moved data is not metadata: %v", err)
	}
}

func TestMigrateLegacyMetadataWithoutLegacyFolder(t *testing.T) {
	if moved, err := MigrateLegacyMetadata(t.TempDir()); err != nil || moved {
		t.Fatalf("MigrateLegacyMetadata = %v, %v", moved, err)
	}
}
