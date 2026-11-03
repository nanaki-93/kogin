package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/brand"
)

// MigrateLegacyMetadata moves a project's saved data from .kasuri or
// .mini-orca, its folder before the rename to Kogin, to .kogin when that
// does not exist yet. Opening the project calls it first, so everything else
// only knows .kogin. An old folder that is a symlink is left alone. It
// reports whether it moved a folder.
func MigrateLegacyMetadata(root string) (bool, error) {
	clean := filepath.Clean(root)
	current := filepath.Join(clean, brand.MetadataDir)
	legacy := make([]string, 0, len(brand.LegacyMetadataDirs))
	for _, name := range brand.LegacyMetadataDirs {
		legacy = append(legacy, filepath.Join(clean, name))
	}
	_, before := os.Lstat(current)
	if used, err := brand.MigrateDir(current, legacy...); err != nil {
		return false, fmt.Errorf("move %s to %s: %w", used, current, err)
	}
	_, after := os.Lstat(current)
	return before != nil && after == nil, nil
}

// CurrentMetadataPath names a checkout-relative path recorded under .kasuri
// or .mini-orca, before the rename to Kogin, by where it is now: opening the
// project moved that folder to .kogin. Other paths are returned unchanged.
func CurrentMetadataPath(relative string) string {
	for _, name := range brand.LegacyMetadataDirs {
		if rest, ok := strings.CutPrefix(relative, name+"/"); ok {
			return brand.MetadataDir + "/" + rest
		}
	}
	return relative
}
