// Package agentstate owns private, SDK-independent agent records.
package agentstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

var (
	ErrNotFound        = errors.New("agent record not found")
	ErrCorrupt         = errors.New("agent journal is invalid; existing data was preserved")
	ErrPersistence     = errors.New("agent state could not be persisted")
	errJournalReplaced = errors.New("agent journal changed while opening")
)

const maxJournalBytes = 64 << 20

type Store struct {
	root, workspace string
	write           func(string, []byte, os.FileMode, func() error) error
}

type database struct {
	RuntimeFamily string                 `json:"runtime_family,omitempty"`
	Traces        map[string]TraceRecord `json:"traces,omitempty"`
	Settings      *SettingsRecord        `json:"settings,omitempty"`
	// Retired features (delegations, external API actions) kept their records
	// in this journal. They are preserved verbatim so older journals still open.
	RetiredDelegations jsontext.Value          `json:"delegations,omitempty"`
	RetiredActions     jsontext.Value          `json:"actions,omitempty"`
	ProgressEpoch      string                  `json:"progress_epoch,omitempty"`
	ProgressSequence   uint64                  `json:"progress_sequence,omitempty"`
	Progress           []ProgressEvent         `json:"progress,omitempty"`
	Version            int                     `json:"version"`
	Workspace          string                  `json:"workspace"`
	Sessions           map[string]*Session     `json:"sessions"`
	AppState           map[string]State        `json:"app_state"`
	UserState          map[string]State        `json:"user_state"`
	Runs               map[string]Run          `json:"runs"`
	Budgets            map[string]BudgetRecord `json:"budgets"`
}

// Open validates checkout identity without creating metadata or starting work.
func Open(root string) (*Store, error) {
	s, err := newStore(root)
	if err != nil {
		return nil, err
	}
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func newStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("agent checkout root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve agent checkout: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("agent checkout must be a directory")
	}
	digest := sha256.Sum256([]byte(canonical))
	return &Store{root: canonical, workspace: hex.EncodeToString(digest[:]), write: storage.WriteFileAuthorized}, nil
}

func (s *Store) path(name string) (string, error) {
	return storage.MetadataPath(s.root, ".kasuri", "agents", s.workspace, name)
}

func (s *Store) empty() *database {
	return &database{Version: journalVersion, RuntimeFamily: journalRuntimeFamily, Workspace: s.workspace, Sessions: map[string]*Session{}, AppState: map[string]State{}, UserState: map[string]State{}, Runs: map[string]Run{}, Budgets: map[string]BudgetRecord{}}
}

func (s *Store) load() (*database, error) {
	path, err := s.path("journal.json")
	if err != nil {
		return nil, err
	}
	data, err := readJournalFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.empty(), nil
	}
	if errors.Is(err, ErrCorrupt) {
		return nil, s.journalError("it is not a private file of at most 64 MiB", false)
	}
	if err != nil {
		return nil, fmt.Errorf("read agent journal: %w", err)
	}
	if !validJSON(data) {
		return nil, s.journalError("it is not valid JSON", false)
	}
	if version := journalFormat(data); version > journalVersion {
		return nil, s.journalError(fmt.Sprintf("format %d; this version reads format %d", version, journalVersion), true)
	}
	var db database
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&db); err != nil {
		return nil, s.journalError("it has unexpected content", false)
	}
	if err := s.validate(&db); err != nil {
		if errors.Is(err, ErrCorrupt) {
			return nil, s.journalError("its records are inconsistent", false)
		}
		return nil, err
	}
	return &db, nil
}

func readJournalFile(path string) ([]byte, error) {
	return readJournalFileOpening(path, os.Open)
}

func readJournalFileOpening(path string, open func(string) (*os.File, error)) ([]byte, error) {
	// Readers may overlap an atomic commit. Recheck the new inode instead of
	// calling that valid replacement corrupt; every attempt repeats all guards.
	for range 3 {
		data, err := readJournalFileAttempt(path, open)
		if !errors.Is(err, errJournalReplaced) {
			return data, err
		}
	}
	return nil, fmt.Errorf("read stable journal: %w", errJournalReplaced)
}

func readJournalFileAttempt(path string, open func(string) (*os.File, error)) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || !privateJournalMode(before) || before.Size() > maxJournalBytes {
		return nil, ErrCorrupt
	}
	file, err := open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !privateJournalMode(after) || after.Size() > maxJournalBytes {
		return nil, ErrCorrupt
	}
	if !os.SameFile(before, after) {
		return nil, errJournalReplaced
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxJournalBytes {
		return nil, ErrCorrupt
	}
	return data, nil
}

// Windows reports synthetic Unix permission bits; access there is governed by
// the checkout's ACLs. The shared atomic writer still requests mode 0600.
func privateJournalMode(info os.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm()&0077 == 0
}

func (s *Store) validate(db *database) error {
	if err := validateJournalFormat(db); err != nil {
		return err
	}
	if db.Workspace != s.workspace || db.Sessions == nil || db.AppState == nil || db.UserState == nil || db.Runs == nil || db.Budgets == nil {
		return ErrCorrupt
	}
	if len(db.Sessions) > 4096 || len(db.Runs) > 8192 || len(db.Budgets) > 2048 {
		return fmt.Errorf("agent journal record limit reached")
	}
	if err := validateSessions(db); err != nil {
		return ErrCorrupt
	}
	if err := validateRunRecords(db); err != nil {
		return err
	}
	return validateJournalDomains(db)
}

func validateJournalDomains(db *database) error {
	if err := validateTraces(db); err != nil {
		return err
	}
	if err := validateSettings(db.Settings); err != nil {
		return err
	}
	return validateProgress(db)
}

func validateSessions(db *database) error {
	for key, value := range db.Sessions {
		if value == nil || key != value.Key.key() || value.validate() != nil {
			return ErrCorrupt
		}
	}
	if err := validateSharedState(db.AppState, "app:", 1); err != nil {
		return err
	}
	return validateSharedState(db.UserState, "user:", 2)
}

func validateSharedState(states map[string]State, prefix string, parts int) error {
	for identity, state := range states {
		ids := strings.Split(identity, "\x00")
		if len(ids) != parts {
			return ErrCorrupt
		}
		for _, id := range ids {
			if !validID(id) {
				return ErrCorrupt
			}
		}
		if err := validateState(state); err != nil {
			return err
		}
		for key := range state {
			if !strings.HasPrefix(key, prefix) {
				return ErrCorrupt
			}
		}
	}
	return nil
}

func validJSON(data []byte) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	if err := decoder.SkipValue(); err != nil {
		return false
	}
	_, err := decoder.ReadToken()
	return err == io.EOF
}

func (s *Store) mutate(ctx context.Context, change func(*database) error) (err error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return persistenceOperationError(parent, "acquire journal lock", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: release journal lock: %w", ErrPersistence, closeErr))
		}
	}()
	db, err := s.load()
	if err != nil {
		return err
	}
	if err := migrateJournalForWrite(ctx, db); err != nil {
		return err
	}
	if err := change(db); err != nil {
		return err
	}
	if err := s.validate(db); err != nil {
		return err
	}
	data, err := json.Marshal(db)
	if err != nil {
		return fmt.Errorf("encode agent journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return fmt.Errorf("agent journal size limit reached")
	}
	path, err := s.path("journal.json")
	if err != nil {
		return err
	}
	if err := s.write(path, data, 0600, func() error { return context.Cause(ctx) }); err != nil {
		return persistenceOperationError(parent, "save journal", err)
	}
	return nil
}

// Cancellation before an atomic commit is not a failed disk write. Preserve
// genuine I/O errors and the store's own lock timeout as persistence failures.
func persistenceOperationError(parent context.Context, operation string, err error) error {
	if cause := context.Cause(parent); cause != nil && (errors.Is(err, parent.Err()) || errors.Is(err, cause)) {
		return cause
	}
	return fmt.Errorf("%w: %s: %w", ErrPersistence, operation, err)
}

func (s *Store) lock(ctx context.Context) (*os.File, error) {
	path, err := s.path("journal.lock")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if _, err := s.path("journal.lock"); err != nil {
		return nil, err
	}
	file, err := openJournalLock(path)
	if err != nil {
		return nil, err
	}
	for {
		if err := context.Cause(ctx); err != nil {
			_ = file.Close()
			return nil, err
		}
		locked, err := tryLock(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return file, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func openJournalLock(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if info != nil && !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.Join(ErrCorrupt, err)
	}
	return file, nil
}
