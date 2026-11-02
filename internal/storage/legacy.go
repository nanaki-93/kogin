package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/brand"
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

// CurrentMetadataPath names a checkout-relative path recorded under
// .mini-orca, before the rename to Kasuri, by where it is now: opening the
// project moved that folder to .kasuri. Other paths are returned unchanged.
func CurrentMetadataPath(relative string) string {
	if rest, ok := strings.CutPrefix(relative, brand.LegacyMetadataDir+"/"); ok {
		return brand.MetadataDir + "/" + rest
	}
	return relative
}
