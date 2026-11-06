package agentstate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrContextPending = errors.New("a context operation has an uncertain outcome; inspect its prior run before clearing the private context cache")

const maxContextRecords = 1024
const maxContextValue = 1 << 20
const maxContextCatalog = 32 << 20

// Fingerprint includes the complete source/instruction/schema/provider input.
// Binding adds checkout policy and configuration; neither key grants authority.
type ContextKey struct {
	Binding     Binding `json:"binding"`
	Kind        string  `json:"kind"`
	Fingerprint string  `json:"fingerprint"`
}

type ContextRecord struct {
	Key       ContextKey      `json:"key"`
	Claim     string          `json:"claim,omitempty"`
	State     string          `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
}

type contextCatalog struct {
	Version   int                      `json:"version"`
	Workspace string                   `json:"workspace"`
	Records   map[string]ContextRecord `json:"records"`
}

func (key ContextKey) identity() (string, error) {
	if key.Binding.Validate() != nil || !validHash(key.Fingerprint) || (key.Kind != "composition" && key.Kind != "summary" && key.Kind != "provider") {
		return "", fmt.Errorf("invalid context cache identity")
	}
	data, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (record ContextRecord) validate() error {
	if _, err := record.Key.identity(); err != nil {
		return err
	}
	if err := record.validateRetention(); err != nil {
		return err
	}
	if record.Key.Kind != "composition" && !validID(record.Claim) {
		return ErrCorrupt
	}
	switch record.State {
	case "pending":
		if record.Key.Kind == "composition" || len(record.Value) != 0 {
			return ErrCorrupt
		}
	case "ready":
		if len(record.Value) == 0 || len(record.Value) > maxContextValue || !validJSON(record.Value) {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	return nil
}

func (record ContextRecord) validateRetention() error {
	if record.CreatedAt.IsZero() || (record.Key.Kind == "summary") != record.ExpiresAt.IsZero() {
		return ErrCorrupt
	}
	if !record.ExpiresAt.IsZero() && (!record.ExpiresAt.After(record.CreatedAt) || record.ExpiresAt.Sub(record.CreatedAt) > 24*time.Hour) {
		return ErrCorrupt
	}
	return nil
}

func (s *Store) loadContext() (*contextCatalog, error) {
	location, err := s.path("context-cache.json")
	if err != nil {
		return nil, err
	}
	data, err := readJournalFile(location)
	if errors.Is(err, os.ErrNotExist) {
		return &contextCatalog{Version: 1, Workspace: s.workspace, Records: map[string]ContextRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxContextCatalog || !validJSON(data) {
		return nil, ErrCorrupt
	}
	var catalog contextCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || catalog.Version != 1 || catalog.Workspace != s.workspace || catalog.Records == nil || len(catalog.Records) > maxContextRecords {
		return nil, ErrCorrupt
	}
	for id, record := range catalog.Records {
		want, err := record.Key.identity()
		if err != nil || id != want || record.validate() != nil {
			return nil, ErrCorrupt
		}
	}
	return &catalog, nil
}

// Context reads without creating files or extending retention. Expiry remains
// explicit so an expired paid receipt cannot become an accidental cache miss.
func (s *Store) Context(ctx context.Context, key ContextKey) (ContextRecord, error) {
	if err := context.Cause(ctx); err != nil {
		return ContextRecord{}, err
	}
	id, err := key.identity()
	if err != nil {
		return ContextRecord{}, err
	}
	catalog, err := s.loadContext()
	if err != nil {
		return ContextRecord{}, err
	}
	record, ok := catalog.Records[id]
	if !ok {
		return ContextRecord{}, ErrNotFound
	}
	return record, context.Cause(ctx)
}

func contextWriteAuthority(ctx context.Context, authorize func() error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if authorize == nil {
		return fmt.Errorf("context publication requires current authority")
	}
	return authorize()
}

func (s *Store) mutateContext(ctx context.Context, authorize func() error, change func(*contextCatalog, time.Time) error) (err error) {
	if err := contextWriteAuthority(ctx, authorize); err != nil {
		return err
	}
	parent := ctx
	release, err := s.enterJournal(parent)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := s.lock(ctx)
	if err != nil {
		return persistenceOperationError(parent, "lock context cache", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: release context lock: %w", ErrPersistence, closeErr))
		}
	}()
	catalog, err := s.loadContext()
	if err != nil {
		return err
	}
	if err := contextWriteAuthority(ctx, authorize); err != nil {
		return err
	}
	now := time.Now().UTC()
	dropExpiredCompositions(catalog, now)
	if err := change(catalog, now); err != nil {
		return err
	}
	if len(catalog.Records) > maxContextRecords {
		return fmt.Errorf("context cache reached its %d-record limit", maxContextRecords)
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	if len(data) > maxContextCatalog {
		return fmt.Errorf("context cache exceeds 32 MiB")
	}
	location, err := s.path("context-cache.json")
	if err != nil {
		return err
	}
	if err := s.write(location, data, 0600, func() error { return contextWriteAuthority(ctx, authorize) }); err != nil {
		return persistenceOperationError(parent, "save context cache", err)
	}
	return nil
}

func dropExpiredCompositions(catalog *contextCatalog, now time.Time) {
	for id, record := range catalog.Records {
		if record.Key.Kind == "composition" && !record.ExpiresAt.After(now) {
			delete(catalog.Records, id)
		}
	}
}

// ClaimContext serializes paid work without retaining a lock across I/O. A
// process crash leaves a pending receipt, which a later process must not retry.
func (s *Store) ClaimContext(ctx context.Context, key ContextKey, expires time.Time, authorize func() error) (record ContextRecord, claimed bool, err error) {
	id, err := key.identity()
	if err != nil || key.Kind == "composition" {
		return record, false, fmt.Errorf("paid context identity is required")
	}
	err = s.mutateContext(ctx, authorize, func(catalog *contextCatalog, now time.Time) error {
		if previous, ok := catalog.Records[id]; ok {
			if previous.State == "pending" {
				return ErrContextPending
			}
			if previous.ExpiresAt.IsZero() || previous.ExpiresAt.After(now) {
				record = previous
				return nil
			}
		}
		record = ContextRecord{Key: key, Claim: rand.Text(), State: "pending", CreatedAt: now, ExpiresAt: expires}
		if err := record.validate(); err != nil {
			return fmt.Errorf("invalid context retention: %w", err)
		}
		catalog.Records[id], claimed = record, true
		return nil
	})
	return record, claimed && err == nil, err
}

// SaveContext publishes either deterministic composition or the exact claim's
// paid result. Paid receipts never overwrite another owner's pending operation.
func (s *Store) SaveContext(ctx context.Context, key ContextKey, claim string, value json.RawMessage, expires time.Time, authorize func() error) error {
	id, err := key.identity()
	if err != nil {
		return err
	}
	value = bytes.Clone(value)
	return s.mutateContext(ctx, authorize, func(catalog *contextCatalog, now time.Time) error {
		record := ContextRecord{Key: key, State: "ready", CreatedAt: now, ExpiresAt: expires, Value: value}
		if key.Kind != "composition" {
			previous, ok := catalog.Records[id]
			if !ok || previous.State != "pending" || previous.Claim != claim {
				return ErrContextPending
			}
			record.Claim, record.CreatedAt = claim, previous.CreatedAt
			if !previous.ExpiresAt.IsZero() && expires.After(previous.ExpiresAt) {
				return fmt.Errorf("context publication cannot extend admitted retention")
			}
		}
		if err := record.validate(); err != nil {
			return err
		}
		catalog.Records[id] = record
		return nil
	})
}
