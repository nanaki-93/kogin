package project

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFindingCompletionSurvivesRefreshAndReopen(t *testing.T) {
	root := t.TempDir()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := FindingInput{ProjectID: "project", ProjectRevision: "before"}
	reported := SuggestedFindingsForFile(FileAnalysis{Path: "main.go", Status: AnalysisStatusFresh, Risks: []Finding{{Severity: "high", Summary: "Fix this finding"}, {Severity: "low", Summary: "Leave this finding open"}}})
	findings, err := store.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil {
		t.Fatal(err)
	}
	completion := FindingCompletion{FindingTarget: FindingTarget{ProjectID: input.ProjectID, Kind: "semantic", ID: findings[0].ID, Path: "main.go"}, CommitOID: "merged-commit"}
	if err := store.Complete([]FindingCompletion{completion, completion}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input.ProjectRevision = "after"
	// Source changes make old suggestions stale; reconciliation must retain Done.
	current, err := restarted.ReconcileSource(input, FindingSourceAI, nil)
	if err != nil || len(current) != 2 {
		t.Fatalf("refresh: %+v, %v", current, err)
	}
	for _, finding := range current {
		if finding.ID == completion.ID && (finding.Status != FindingStatusFixed || finding.Freshness != FindingFreshnessStale || finding.Confidence != FindingConfidenceSuggested) {
			t.Fatalf("completion changed evidence or lost triage: %+v", finding)
		}
		if finding.ID != completion.ID && finding.Status == FindingStatusFixed {
			t.Fatal("completed unrelated finding")
		}
	}
	completed, err := restarted.Completions(input.ProjectID)
	if err != nil || len(completed) != 1 || completed[0] != completion {
		t.Fatalf("restored completions: %+v, %v", completed, err)
	}
	if other, err := restarted.Completions("other-project"); err != nil || len(other) != 0 {
		t.Fatalf("leaked another project's completion: %+v, %v", other, err)
	}
	if err := restarted.SetStatus(completion.ID, FindingStatusOpen); err != nil {
		t.Fatal(err)
	}
	current, err = restarted.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range current {
		if finding.ID == completion.ID && finding.Status != FindingStatusOpen {
			t.Fatalf("reopen did not override completion: %+v", finding)
		}
	}
	if completed, err := restarted.Completions(input.ProjectID); err != nil || len(completed) != 0 {
		t.Fatalf("reopen retained completion: %+v, %v", completed, err)
	}
	if err := restarted.SetStatus(completion.ID, FindingStatusFixed); err != nil {
		t.Fatal(err)
	}
	current, err = restarted.ReconcileSource(input, FindingSourceAI, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range current {
		if finding.ID == completion.ID && finding.Status != FindingStatusFixed {
			t.Fatalf("manual Mark as Done was overwritten by reconciliation: %+v", finding)
		}
	}
	perfTarget := FindingTarget{ProjectID: input.ProjectID, Kind: "performance", ID: "perf-1", Path: "main.go"}
	if err := restarted.SetTargetStatus(perfTarget, FindingStatusFixed, ""); err != nil {
		t.Fatal(err)
	}
	if completed, err := restarted.Completions(input.ProjectID); err != nil || len(completed) != 2 {
		t.Fatalf("expected semantic and performance completions: %+v, %v", completed, err)
	}
	if err := restarted.SetTargetStatus(perfTarget, FindingStatusOpen, ""); err != nil {
		t.Fatal(err)
	}
	if completed, err := restarted.Completions(input.ProjectID); err != nil || len(completed) != 1 {
		t.Fatalf("reopen target did not remove completion: %+v, %v", completed, err)
	}
}

func TestFindingCompletionConcurrentStoresPreserveAllProducers(t *testing.T) {
	root := t.TempDir()
	var workers sync.WaitGroup
	for _, target := range []FindingTarget{
		{ProjectID: "project", Kind: "semantic", ID: "same", Path: "main.go"},
		{ProjectID: "project", Kind: "performance", ID: "same", Path: "main.go"},
		{ProjectID: "project", Kind: "security", Source: SecuritySourceAI, ID: "same", Path: "main.go"},
		{ProjectID: "project", Kind: "security", Source: SecuritySourceDeterministic, ID: "same", Path: "main.go"},
	} {
		workers.Add(1)
		go func(target FindingTarget) {
			defer workers.Done()
			store, err := NewFindingStore(root)
			if err != nil {
				t.Error(err)
				return
			}
			if err := store.Complete([]FindingCompletion{{FindingTarget: target, CommitOID: "commit"}}); err != nil {
				t.Error(err)
			}
		}(target)
	}
	workers.Wait()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := store.Completions("project"); err != nil || len(completed) != 4 {
		t.Fatalf("lost concurrent completion: %+v, %v", completed, err)
	}
}

func TestFindingCompletionFailurePreservesStoredData(t *testing.T) {
	for _, failure := range []string{"corrupt", "write"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewFindingStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, ".kasuri"), 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, findingStorePath)
			data := []byte("{broken")
			if failure == "write" {
				data = []byte(`{"schema_version":"1","findings":[]}`)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if failure == "write" {
				if os.Geteuid() == 0 {
					t.Skip("root ignores directory permissions, so the write failure cannot be induced")
				}
				if err := os.Chmod(filepath.Dir(file), 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(file), 0700) })
			}
			if err := store.Complete([]FindingCompletion{{FindingTarget: FindingTarget{ProjectID: "project", Kind: "performance", ID: "risk", Path: "main.go"}, CommitOID: "commit"}}); err == nil {
				t.Fatal("completion failure reported success")
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != string(data) {
				t.Fatalf("failed completion changed existing data: %s, %v", got, err)
			}
		})
	}
}
