package agentstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// An unreadable journal blocks every agent run in its checkout. The host may
// set it aside: its bytes move to a quarantined file next to it, a record of
// the move is kept, and the next run starts a new journal. A journal written
// by a newer Kasuri is never set aside; it needs that version.

// ErrNewerJournal marks a journal in a format this version cannot read.
var ErrNewerJournal = errors.New("agent journal was written by a newer Kasuri")

const maxQuarantineRecords = 32

// JournalError names the journal a problem was found in and what to do.
type JournalError struct {
	// Path is the journal relative to its checkout.
	Path   string
	Reason string
	Newer  bool
}

func (e *JournalError) Error() string {
	if e.Newer {
		return fmt.Sprintf("agent journal %s was written by a newer Kasuri (%s); upgrade Kasuri to use it, or move the file away to start a new journal", e.Path, e.Reason)
	}
	return fmt.Sprintf("agent journal %s is unreadable: %s; existing data was preserved", e.Path, e.Reason)
}

func (e *JournalError) Unwrap() []error {
	if e.Newer {
		return []error{ErrNewerJournal, ErrCorrupt}
	}
	return []error{ErrCorrupt}
}

// QuarantineRecord tells where an unreadable journal was set aside and why.
type QuarantineRecord struct {
	File          string    `json:"file"`
	QuarantinedAs string    `json:"quarantined_as"`
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
}

func (s *Store) relative(name string) string {
	path, err := s.path(name)
	if err != nil {
		return name
	}
	if relative, err := filepath.Rel(s.root, path); err == nil {
		return filepath.ToSlash(relative)
	}
	return path
}

func (s *Store) journalError(reason string, newer bool) error {
	return &JournalError{Path: s.relative("journal.json"), Reason: reason, Newer: newer}
}

// journalFormat reads only the format version, so a newer journal is told
// apart from a corrupt one before its fields are checked.
func journalFormat(data []byte) int {
	var header struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &header) != nil {
		return 0
	}
	return header.Version
}

// Quarantine sets aside the checkout's journal when it is unreadable and
// returns the record of the move. A readable or newer journal is left alone.
func Quarantine(ctx context.Context, root string) (*QuarantineRecord, error) {
	s, err := newStore(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock, err := s.lock(ctx)
	if err != nil {
		return nil, persistenceOperationError(ctx, "acquire journal lock", err)
	}
	defer lock.Close()
	_, loadErr := s.load()
	var problem *JournalError
	if loadErr == nil || !errors.As(loadErr, &problem) || problem.Newer {
		return nil, loadErr
	}
	now := time.Now().UTC()
	name := "journal.quarantined-" + now.Format("20060102T150405.000000000Z") + ".json"
	from, err := s.path("journal.json")
	if err != nil {
		return nil, err
	}
	to, err := s.path(name)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(from, to); err != nil {
		return nil, fmt.Errorf("%w: set aside the agent journal: %w", ErrPersistence, err)
	}
	record := QuarantineRecord{File: problem.Path, QuarantinedAs: s.relative(name), Reason: problem.Reason, At: now}
	records, err := s.quarantines()
	if err != nil {
		records = nil // An unreadable log of earlier moves does not block this one.
	}
	records = append(records, record)
	if len(records) > maxQuarantineRecords {
		records = slices.Clone(records[len(records)-maxQuarantineRecords:])
	}
	data, err := json.Marshal(records)
	if err != nil {
		return nil, err
	}
	path, err := s.path("quarantine.json")
	if err != nil {
		return nil, err
	}
	if err := s.write(path, data, 0600, func() error { return context.Cause(ctx) }); err != nil {
		return &record, fmt.Errorf("%w: record the set-aside journal: %w", ErrPersistence, err)
	}
	return &record, nil
}

// Quarantines lists the journals set aside in the checkout, oldest first.
func Quarantines(root string) ([]QuarantineRecord, error) {
	s, err := newStore(root)
	if err != nil {
		return nil, err
	}
	return s.quarantines()
}

func (s *Store) quarantines() ([]QuarantineRecord, error) {
	path, err := s.path("quarantine.json")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []QuarantineRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []QuarantineRecord
	if err := json.Unmarshal(data, &records); err != nil || len(records) > maxQuarantineRecords {
		return nil, fmt.Errorf("the record of set-aside journals is unreadable")
	}
	return records, nil
}
