// Package history keeps what the agents did on each task, so it outlives
// the task's worktree: its runs and stages with time, tokens, models and how
// each ended, its issue steps, its reviewers' findings and behavior changes,
// its checks and the task's final outcome. It is one SQLite file per
// project (.kogin/kogin.db), written as each stage ends.
//
// A snapshot replaces what the history holds for its task's run, so writing
// the same task state again changes nothing. The store is best effort for
// its callers: losing a write loses a record, never a task. Each write opens
// and closes the file, so nothing stays open between tasks.
package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // The pure-Go SQLite driver, registered as "sqlite".
)

// FileName is the history's file inside the project's .kogin directory.
const FileName = "kogin.db"

// schemaVersion is the history's schema; a newer one is refused.
const schemaVersion = 4

// Store is one project's history, open until Close.
type Store struct {
	db *sql.DB
}

// Open opens the history in dir (a project's .kogin directory), creating it
// on first use. Writers in other goroutines or processes wait for each other.
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, FileName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("history directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return store, nil
}

// SetAside moves an unreadable history out of the way, so the next Open
// starts a new one; the old file stays beside it for inspection.
func SetAside(dir string) error {
	path := filepath.Join(dir, FileName)
	aside := filepath.Join(dir, "kogin.unreadable-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("set aside the unreadable history: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	return nil
}

// Close closes the history.
func (s *Store) Close() error {
	return s.db.Close()
}

// ErrNewerSchema refuses a history written by a newer Kogin.
var ErrNewerSchema = errors.New("the history was written by a newer Kogin")

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history version: %w", err)
	}
	if version > schemaVersion {
		return ErrNewerSchema
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Each version's statements bring a history from the version before it.
	for from, statements := range [][]string{schema, schemaV2, schemaV3, schemaV4} {
		if version > from {
			continue
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("bring history to version %d: %w", from+1, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// schemaV4 keeps, with a task's lesson, the plan its human wrote before
// seeing the investigator's, and their mark of how close it was.
var schemaV4 = []string{
	`ALTER TABLE lessons ADD COLUMN own_plan TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE lessons ADD COLUMN own_match TEXT NOT NULL DEFAULT ''`,
}

// schema is version 1 of the history. Times are RFC 3339 text in UTC.
var schema = []string{
	`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		title TEXT NOT NULL,
		branch TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		outcome_at TEXT NOT NULL DEFAULT '',
		commit_oid TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		attempt INTEGER NOT NULL,
		status TEXT NOT NULL,
		failure TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE stages (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		failure TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT '',
		finished_at TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		model_calls INTEGER NOT NULL DEFAULT 0,
		tool_calls INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, position)
	)`,
	`CREATE TABLE issue_steps (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		finding_id TEXT NOT NULL,
		path TEXT NOT NULL,
		title TEXT NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		commit_oid TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, position)
	)`,
	`CREATE TABLE review_items (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		proposal_hash TEXT NOT NULL,
		reviewer TEXT NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('finding', 'note', 'behavior_change')),
		title TEXT NOT NULL,
		path TEXT NOT NULL DEFAULT '',
		line INTEGER NOT NULL DEFAULT 0,
		detail TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE checks (
		run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		proposal_hash TEXT NOT NULL,
		name TEXT NOT NULL,
		required INTEGER NOT NULL,
		state TEXT NOT NULL,
		outcome TEXT NOT NULL DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX runs_task ON runs(task_id)`,
	`CREATE INDEX review_items_kind ON review_items(kind, path)`,
}

// schemaV2 adds the plan the investigation proposed for a run: what it said,
// whether the human approved it, and its issue verdicts as JSON.
var schemaV2 = []string{
	`CREATE TABLE plans (
		run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
		hash TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		approved INTEGER NOT NULL DEFAULT 0,
		problem TEXT NOT NULL DEFAULT '',
		root_cause TEXT NOT NULL DEFAULT '',
		approach TEXT NOT NULL DEFAULT '',
		verdicts TEXT NOT NULL DEFAULT '',
		investigated_at TEXT NOT NULL DEFAULT ''
	)`,
}

// schemaV3 adds the learning log: one lesson per accepted task (what was
// wrong, the approach, the concepts its change used) and saved answers from
// Ask the code. It outlives the tasks it came from.
var schemaV3 = []string{
	`CREATE TABLE lessons (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL CHECK (kind IN ('task', 'ask')),
		task_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		problem TEXT NOT NULL DEFAULT '',
		insight TEXT NOT NULL DEFAULT '',
		concepts TEXT NOT NULL DEFAULT '',
		paths TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX lessons_created ON lessons(created_at)`,
}

// stamp writes a time as the history stores it; the zero time is empty.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
