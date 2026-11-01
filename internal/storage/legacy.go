package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nanaki-93/mini-orca/v2/internal/brand"
)

// MigrateLegacyMetadata moves a project's saved data from .mini-orca, its
// folder before the rename to Kasuri, to .kasuri when that does not exist
// yet. Opening the project calls it first, so everything else only knows
// .kasuri. A .mini-orca that is a symlink is left alone. It reports whether
// it moved the folder.
func MigrateLegacyMetadata(root string) (bool, error) {
	clean := filepath.Clean(root)
	current, legacy := filepath.Join(clean, brand.MetadataDir), filepath.Join(clean, brand.LegacyMetadataDir)
	_, before := os.Lstat(current)
	if _, err := brand.MigrateDir(current, legacy); err != nil {
		return false, fmt.Errorf("move %s to %s: %w", legacy, current, err)
	}
	_, after := os.Lstat(current)
	return before != nil && after == nil, nil
}
