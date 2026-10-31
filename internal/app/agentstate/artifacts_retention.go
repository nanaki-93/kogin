package agentstate

import (
	"context"
	"fmt"
	"time"
)

// ArtifactInventoryEntry omits payload bytes. InUse is an advisory snapshot;
// deletion always checks the current run journal while holding the shared lock.
type ArtifactInventoryEntry struct {
	Artifact
	InUse bool
}

func (s *Store) ArtifactInventory(ctx context.Context) ([]ArtifactInventoryEntry, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	catalog, err := s.loadArtifacts()
	if err != nil {
		return nil, err
	}
	db, err := s.load()
	if err != nil {
		return nil, err
	}
	active := activeArtifactSessions(db)
	result := []ArtifactInventoryEntry{}
	for _, history := range catalog.Entries {
		for _, record := range history.Versions {
			record.Data = nil
			result = append(result, ArtifactInventoryEntry{Artifact: record, InUse: active[record.Key.Session.key()]})
		}
	}
	return result, context.Cause(ctx)
}

func activeArtifactSessions(db *database) map[string]bool {
	active := map[string]bool{}
	for _, run := range db.Runs {
		if unfinishedRun(run) {
			active[run.Session.key()] = true
			shared := run.Session
			shared.ID = "user"
			active[shared.key()] = true
		}
	}
	for _, event := range db.Progress {
		if !unfinishedRun(db.Runs[event.RunID]) && !unfinishedRun(db.Runs[event.RootID]) {
			continue
		}
		for _, reference := range event.Evidence {
			active[reference.Session.key()] = true
		}
	}
	return active
}

func (s *Store) DeleteArtifactVersion(ctx context.Context, key ArtifactKey, version int64, hash string, authorize func() error) error {
	return s.mutateArtifactsAuthorized(ctx, authorize, func(catalog *artifactCatalog) (bool, error) {
		if _, err := exactArtifact(catalog, key, version, hash); err != nil {
			return false, err
		}
		if err := s.artifactRunProtection(key); err != nil {
			return false, err
		}
		return pruneArtifacts(catalog.Entries[key.key()], ArtifactRetention{Version: version})
	})
}

// ArtifactRetention is applied only by an explicit deletion request. Zero
// Version deletes all eligible versions; KeepLatest and Before narrow that set.
type ArtifactRetention struct {
	Version    int64
	KeepLatest int
	Before     time.Time
}

func (s *Store) DeleteArtifacts(ctx context.Context, key ArtifactKey, retention ArtifactRetention) error {
	if key.validate() != nil || retention.Version < 0 || retention.KeepLatest < 0 || retention.KeepLatest > 128 {
		return fmt.Errorf("invalid artifact retention selection")
	}
	return s.mutateArtifacts(ctx, func(catalog *artifactCatalog) (bool, error) {
		history := catalog.Entries[key.key()]
		if history == nil {
			return false, nil
		}
		if err := s.artifactRunProtection(key); err != nil {
			return false, err
		}
		return pruneArtifacts(history, retention)
	})
}

func (s *Store) artifactRunProtection(key ArtifactKey) error {
	db, err := s.load()
	if err != nil {
		return err
	}
	if activeArtifactSessions(db)[key.scoped().Session.key()] {
		return ErrArtifactInUse
	}
	return nil
}

func pruneArtifacts(history *artifactHistory, retention ArtifactRetention) (bool, error) {
	keep := []Artifact{}
	cutoff := max(0, len(history.Versions)-retention.KeepLatest)
	for i, record := range history.Versions {
		remove := i < cutoff && (retention.Version == 0 || retention.Version == record.Version) && (retention.Before.IsZero() || record.Created.Before(retention.Before))
		if remove && record.Protected {
			return false, ErrArtifactInUse
		}
		if !remove {
			keep = append(keep, record)
		}
	}
	// Keep the last assigned counter even after deleting every payload.
	changed := len(history.Versions) != len(keep)
	history.Versions = keep
	return changed, nil
}
