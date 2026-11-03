package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func assertKnowledgeHistory(t *testing.T, s *Service, root, kind, projectID, revision string) {
	t.Helper()
	store, _, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History(t.Context())
	if err != nil || len(history.Runs) != 2 {
		t.Fatalf("knowledge history=%+v %v", history, err)
	}
	var parent, child agentstate.RunSnapshot
	for _, run := range history.Runs {
		if run.SchemaVersion == "knowledge-graph-v1" {
			parent = run
		} else {
			child = run
		}
		if run.Binding.Project != projectID || run.Binding.Revision != revision || run.Usage.ModelCalls != 1 || run.Usage.ActiveCalls != 0 {
			t.Fatalf("wrong scope or usage: %+v", run)
		}
	}
	if parent.ID == "" || parent.ParentID != "" || parent.Status != "completed" || child.ParentID != parent.ID || child.Status != "completed" {
		t.Fatalf("missing native ancestry: parent=%+v child=%+v", parent, child)
	}
	data, err := json.Marshal(history.Events)
	if err != nil || !strings.Contains(string(data), "knowledge_"+kind) || strings.Contains(string(data), "main.go") || strings.Contains(string(data), "package main") {
		t.Fatalf("unsafe or missing progress: %s %v", data, err)
	}
}

func TestKnowledgeImportBindsTheNewCheckoutAndRetainsPassiveRestore(t *testing.T) {
	var calls atomic.Int32
	server := changeProvider(t, func() string { calls.Add(1); return projectReportFixture })
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	originalRoot := s.manager.Root()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := s.ImportProject(t.Context(), root, true)
	if err != nil || result.AIStatus != project.ProjectAnalysisStatusFresh || calls.Load() != 1 || s.manager.Root() != originalRoot {
		t.Fatalf("import=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	assertKnowledgeHistory(t, s, root, "overview", result.ProjectID, result.ProjectRevision)
	if _, err := s.RestoreProject(root); err != nil || calls.Load() != 1 {
		t.Fatalf("restore dispatched: calls=%d err=%v", calls.Load(), err)
	}
}

func TestKnowledgeImportRejectsChangedSourceAndPolicyBeforePublication(t *testing.T) {
	for _, change := range []string{"source", "policy"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			server := changeProvider(t, func() string {
				path, content := "main.go", "package main\nfunc Changed() {}\n"
				if change == "policy" {
					path, content = ".koginignore", "main.go\n"
				}
				if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
					t.Error(err)
				}
				return projectReportFixture
			})
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			result, err := s.ImportProject(t.Context(), root, true)
			if result != nil || !errors.Is(err, project.ErrRevisionConflict) {
				t.Fatalf("stale import published: %+v %v", result, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".kogin", "project-analysis.json")); !os.IsNotExist(err) {
				t.Fatalf("stale report persisted: %v", err)
			}
		})
	}
}

func TestKnowledgeImportJournalFailurePreservesPriorReport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	server := changeProvider(t, func() string {
		if fail.Load() {
			journal := filepath.Join(root, ".kogin", "agents")
			if err := os.Rename(journal, filepath.Join(root, "journal-backup")); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(journal, []byte("blocked"), 0600); err != nil {
				t.Error(err)
			}
		}
		return projectReportFixture
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	if _, err := s.ImportProject(t.Context(), root, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".kogin", "project-analysis.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	result, err := s.ImportProject(t.Context(), root, true)
	if result != nil || !errors.Is(err, agentstate.ErrPersistence) {
		t.Fatalf("journal failure became inventory success: %+v %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("journal failure overwrote prior interpretation: %v", err)
	}
}
