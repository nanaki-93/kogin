package agentstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

func (s *Store) loadArtifacts() (*artifactCatalog, error) {
	path, err := s.path("artifacts.json")
	if err != nil {
		return nil, err
	}
	data, err := readJournalFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &artifactCatalog{Version: 1, Workspace: s.workspace, Entries: map[string]*artifactHistory{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read artifact catalog: %w", err)
	}
	var catalog artifactCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if !validJSON(data) || decoder.Decode(&catalog) != nil {
		return nil, ErrCorrupt
	}
	if err := s.validateArtifacts(&catalog); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func (s *Store) validateArtifacts(catalog *artifactCatalog) error {
	if catalog.Version != 1 || catalog.Workspace != s.workspace || catalog.Entries == nil || len(catalog.Entries) > 4096 {
		return ErrCorrupt
	}
	for key, history := range catalog.Entries {
		if history == nil || key != history.Key.key() || history.validate() != nil {
			return ErrCorrupt
		}
	}
	return nil
}

func (history *artifactHistory) validate() error {
	if history.Key.validate() != nil || history.Key != history.Key.scoped() || history.Last < 1 || history.Last > 1<<53 || len(history.Versions) > 128 {
		return ErrCorrupt
	}
	previous := int64(0)
	for _, version := range history.Versions {
		if version.Key != history.Key || version.Version <= previous || version.Version > history.Last || version.validate() != nil {
			return ErrCorrupt
		}
		previous = version.Version
	}
	return nil
}

func (s *Store) withArtifacts(ctx context.Context, action func(context.Context, *artifactCatalog) error) (err error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := s.lock(ctx)
	if err != nil {
		return persistenceOperationError(parent, "lock artifacts", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: release artifact lock: %w", ErrPersistence, closeErr))
		}
	}()
	catalog, err := s.loadArtifacts()
	if err != nil {
		return err
	}
	return action(ctx, catalog)
}

func (s *Store) mutateArtifacts(ctx context.Context, change func(*artifactCatalog) (bool, error)) error {
	return s.mutateArtifactsAuthorized(ctx, nil, change)
}

func artifactAuthorization(ctx context.Context, authorize func() error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if authorize != nil {
		return authorize()
	}
	return nil
}

func (s *Store) mutateArtifactsAuthorized(ctx context.Context, authorize func() error, change func(*artifactCatalog) (bool, error)) error {
	parent := ctx
	return s.withArtifacts(ctx, func(ctx context.Context, catalog *artifactCatalog) error {
		guard := func() error { return artifactAuthorization(ctx, authorize) }
		if err := guard(); err != nil {
			return err
		}
		return s.commitArtifacts(parent, catalog, guard, change)
	})
}

func (s *Store) commitArtifacts(parent context.Context, catalog *artifactCatalog, guard func() error, change func(*artifactCatalog) (bool, error)) error {
	changed, err := change(catalog)
	if err != nil {
		return err
	}
	if !changed {
		return guard()
	}
	if err := s.validateArtifacts(catalog); err != nil {
		return err
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("encode artifact catalog: %w", err)
	}
	if len(data) > maxJournalBytes {
		return fmt.Errorf("artifact catalog exceeds 64 MiB")
	}
	path, err := s.path("artifacts.json")
	if err != nil {
		return err
	}
	if err := s.write(path, data, 0600, guard); err != nil {
		return persistenceOperationError(parent, "save artifacts", err)
	}
	return nil
}
