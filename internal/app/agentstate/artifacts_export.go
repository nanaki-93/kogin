package agentstate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

func exactArtifact(catalog *artifactCatalog, key ArtifactKey, version int64, hash string) (*Artifact, error) {
	if key.validate() != nil || version < 1 || len(hash) != 64 {
		return nil, fmt.Errorf("exact artifact identity, version and hash are required")
	}
	if history := catalog.Entries[key.key()]; history != nil {
		for _, record := range history.Versions {
			if record.Version == version {
				if record.Hash != hash {
					return nil, ErrArtifactConflict
				}
				return &record, nil
			}
		}
	}
	return nil, ErrNotFound
}

// ExportArtifactFile serializes against deletion and writes a fresh private
// metadata file. Neither caller input nor the artifact name can select a path.
func (s *Store) ExportArtifactFile(ctx context.Context, key ArtifactKey, version int64, hash string, authorize func() error) (string, error) {
	parent := ctx
	var path string
	err := s.withArtifacts(ctx, func(ctx context.Context, catalog *artifactCatalog) error {
		record, err := exactArtifact(catalog, key, version, hash)
		if err != nil {
			return err
		}
		path, err = storage.MetadataPath(s.root, ".kasuri", "exports", "artifacts", "artifact-"+rand.Text()+".bin")
		if err != nil {
			return err
		}
		guard := func() error { return freshArtifactExport(ctx, path, authorize) }
		if err := guard(); err != nil {
			return err
		}
		if err := s.write(path, record.Data, 0600, guard); err != nil {
			return persistenceOperationError(parent, "export artifact", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func freshArtifactExport(ctx context.Context, path string, authorize func() error) error {
	if err := artifactAuthorization(ctx, authorize); err != nil {
		return err
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("artifact export destination already exists")
}

// ExportArtifact verifies the payload and returns a private copy. It deliberately
// accepts no filesystem destination: exporting cannot mutate project source.
func (s *Store) ExportArtifact(ctx context.Context, key ArtifactKey, version int64) (*Artifact, error) {
	if key.validate() != nil || version < 0 {
		return nil, fmt.Errorf("invalid artifact selection")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	catalog, err := s.loadArtifacts()
	if err != nil {
		return nil, err
	}
	history := catalog.Entries[key.key()]
	if history != nil {
		for _, record := range slices.Backward(history.Versions) {
			if version == 0 || record.Version == version {
				return &record, nil
			}
		}
	}
	return nil, ErrNotFound
}
