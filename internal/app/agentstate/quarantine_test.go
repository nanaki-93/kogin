package agentstate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJournal(t *testing.T, s *Store, data string) string {
	t.Helper()
	path, err := s.path("journal.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnreadableJournalIsNamedAndSetAside(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeJournal(t, s, `{"version":2,`)
	_, err = Open(root)
	var problem *JournalError
	if !errors.As(err, &problem) || !errors.Is(err, ErrCorrupt) || problem.Newer || !strings.HasPrefix(problem.Path, ".kasuri/agents/") || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("corrupt journal error: %v", err)
	}
	record, err := Quarantine(t.Context(), root)
	if err != nil || record == nil || record.File != problem.Path || !strings.Contains(record.QuarantinedAs, "journal.quarantined-") {
		t.Fatalf("quarantine: %+v %v", record, err)
	}
	if moved, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(record.QuarantinedAs))); err != nil || string(moved) != `{"version":2,` {
		t.Fatal("set-aside journal lost its bytes", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreadable journal left in place")
	}
	if _, err := Open(root); err != nil {
		t.Fatalf("new journal: %v", err)
	}
	records, err := Quarantines(root)
	if err != nil || len(records) != 1 || records[0] != *record {
		t.Fatalf("records: %+v %v", records, err)
	}
	if again, err := Quarantine(t.Context(), root); err != nil || again != nil {
		t.Fatalf("readable journal set aside: %+v %v", again, err)
	}
}

func TestNewerJournalIsNeverSetAside(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	path := writeJournal(t, s, `{"version":9,"future_field":true}`)
	_, err = Open(root)
	if !errors.Is(err, ErrNewerJournal) || !strings.Contains(err.Error(), "upgrade Kasuri") || !strings.Contains(err.Error(), "format 9") {
		t.Fatalf("newer journal error: %v", err)
	}
	if record, err := Quarantine(t.Context(), root); record != nil || !errors.Is(err, ErrNewerJournal) {
		t.Fatalf("newer journal set aside: %+v %v", record, err)
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "future_field") {
		t.Fatal("newer journal changed", err)
	}
}
