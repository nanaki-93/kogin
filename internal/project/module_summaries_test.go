package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModuleSummariesRoundTripAndSetAsideWhatTheyCannotRead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0o755); err != nil {
		t.Fatal(err)
	}
	empty, err := LoadModuleSummaries(root, "p")
	if err != nil || len(empty.Entries) != 0 || empty.ProjectID != "p" {
		t.Fatalf("missing file = %+v, %v", empty, err)
	}
	empty.Entries["store"] = ModuleSummaryRecord{ModuleSummary: ModuleSummary{Path: "store", Summary: "Saves items.", Responsibilities: []string{"Items"}}, Key: "k", Model: "m", GeneratedAt: time.Now().UTC()}
	if err := StoreModuleSummaries(root, empty); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadModuleSummaries(root, "p")
	if err != nil || stored.Entries["store"].Summary != "Saves items." || stored.Summaries()["store"].Responsibilities[0] != "Items" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	// Another project's summaries are not this one's.
	if other, err := LoadModuleSummaries(root, "q"); err != nil || len(other.Entries) != 0 {
		t.Fatalf("other project = %+v, %v", other, err)
	}
	path := filepath.Join(root, ".kogin", "module-summaries.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if corrupt, err := LoadModuleSummaries(root, "p"); err != nil || len(corrupt.Entries) != 0 {
		t.Fatalf("corrupt file = %+v, %v", corrupt, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".kogin"))
	if err != nil {
		t.Fatal(err)
	}
	setAside := false
	for _, entry := range entries {
		setAside = setAside || strings.HasPrefix(entry.Name(), "module-summaries.json.corrupt-")
	}
	if !setAside {
		t.Fatalf("the unreadable file was not set aside: %v", entries)
	}
	// A write that cannot land says so.
	if err := os.Chmod(filepath.Join(root, ".kogin"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, ".kogin"), 0o755) })
	if os.Geteuid() != 0 {
		if err := StoreModuleSummaries(root, stored); err == nil {
			t.Fatal("a failed write was reported as saved")
		}
	}
}
