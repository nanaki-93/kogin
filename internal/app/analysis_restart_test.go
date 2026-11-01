package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestAnalysisRestartPreservesSavedResults(t *testing.T) {
	for _, artifacts := range []string{"unchanged", "ignored file added", "ignored file removed"} {
		t.Run(artifacts, func(t *testing.T) {
			server, calls := analysisResponseServer(t, analysisReply)
			base, root := newAnalysisFreshnessService(t, server.URL, false)
			if err := os.WriteFile(filepath.Join(root, ".mini-orcaignore"), []byte("*.log\n.mini-orcaignore\n"), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "runtime.log")
			if artifacts == "ignored file removed" {
				if err := os.WriteFile(log, []byte("previous session\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := base.Reindex(); err != nil {
				t.Fatal(err)
			}
			cfg := scopedTestConfig(server.URL)
			s, err := New(cfg, base.manager)
			if err != nil {
				t.Fatal(err)
			}
			index, err := s.manager.Index()
			if err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{
				ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision,
				Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{100, 30, 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			completed := completedAnalysisRun(t, s)
			if completed.Status != AnalysisRunCompleted {
				t.Fatalf("initial analysis status=%s", completed.Status)
			}
			before, err := s.ProjectOverview()
			if err != nil {
				t.Fatal(err)
			}
			results := savedAnalysisResults(t, s, completed)
			metadata, err := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			if err != nil {
				t.Fatal(err)
			}
			requests := calls.Load()
			switch artifacts {
			case "ignored file added":
				err = os.WriteFile(log, []byte("new session\n"), 0600)
			case "ignored file removed":
				err = os.Remove(log)
			}
			if err != nil {
				t.Fatal(err)
			}
			manager, err := project.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := New(cfg, manager)
			if err != nil {
				t.Fatal(err)
			}
			analysis, err := reopened.RestoreProject(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := reopened.RestoreActiveProject(root, analysis); err != nil {
				t.Fatal(err)
			}
			after, err := reopened.ProjectOverview()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after.Run, before.Run) || after.Coverage != before.Coverage {
				t.Errorf("reopening changed saved progress: before=%s %+v after=%s %+v", before.Run.Status, before.Coverage, after.Run.Status, after.Coverage)
			}
			if !reflect.DeepEqual(savedAnalysisResults(t, reopened, completed), results) {
				t.Error("reopening changed saved findings or their freshness")
			}
			restoredMetadata, err := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			if err != nil || string(restoredMetadata) != string(metadata) {
				t.Errorf("reopening rewrote saved progress: %v", err)
			}
			if calls.Load() != requests {
				t.Fatal("restoration dispatched model requests")
			}
		})
	}
}

// savedAnalysisResults reads a run's saved results, which must hold findings
// of every category.
func savedAnalysisResults(t *testing.T, s *Service, run *AnalysisRun) []savedFileResults {
	t.Helper()
	results := readSavedAnalysisResults(t, s, run)
	for _, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		count := 0
		for _, file := range results {
			count += file.findings(category)
		}
		if count == 0 {
			t.Fatalf("%s saved results=%+v", category, results)
		}
	}
	return results
}

func TestSavedAnalysisDetectsLiveChangesOutsideSelectedFiles(t *testing.T) {
	for _, change := range []string{"edited source", "added source", "deleted source", "policy", "provider"} {
		t.Run(change, func(t *testing.T) {
			server, calls := analysisResponseServer(t, emptyAnalysisReply)
			s, root := newAnalysisFreshnessService(t, server.URL, true)
			excludeAnalysisFiles(t, s, []string{"helper.go"})
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			completed := completedAnalysisRun(t, s)
			if completed.Status != AnalysisRunCompletedEmpty {
				t.Fatalf("initial status=%s", completed.Status)
			}
			var err error
			switch change {
			case "edited source":
				err = os.WriteFile(filepath.Join(root, "helper.go"), []byte("package main\nfunc Changed() {}\n"), 0600)
			case "added source":
				err = os.WriteFile(filepath.Join(root, "added.go"), []byte("package main\n"), 0600)
			case "deleted source":
				err = os.Remove(filepath.Join(root, "helper.go"))
			case "policy":
				err = os.WriteFile(filepath.Join(root, ".mini-orca", "context-policy.json"), []byte(`{"exclude":["helper.go"]}`), 0600)
			case "provider":
				s.conf().runtimes.analyze.effective.Model = "replacement"
			}
			if err != nil {
				t.Fatal(err)
			}
			// No reindex: freshness must inspect live source, including context
			// outside the selected per-file analysis queue.
			run, err := s.currentTestAnalysisRun(context.Background())
			if err != nil || run.Status != AnalysisRunStale || run.Identity != completed.Identity || calls.Load() != 3 {
				t.Fatalf("changed analysis=%+v calls=%d error=%v", run, calls.Load(), err)
			}
		})
	}
}

func TestAnalysisRestartRecoversFalseStalenessWithIneligibleStages(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newAnalysisFreshnessService(t, server.URL, false)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunCompletedEmpty {
		t.Fatalf("initial status=%s", completed.Status)
	}
	old := cloneAnalysisRun(completed)
	old.Status = AnalysisRunStale
	// Saved with the wording runs had before; it reads as today's.
	old.Reason = legacyAnalysisRunStaleReason
	refreshAnalysisSections(old)
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
		t.Fatal(err)
	}
	requests := calls.Load()
	s = transitionAnalysisProject(t, s, "restart")
	run, err := s.currentTestAnalysisRun(context.Background())
	if err != nil || !reflect.DeepEqual(run, completed) || calls.Load() != requests {
		t.Fatalf("saved evidence did not recover: status=%s calls=%d error=%v", run.Status, calls.Load(), err)
	}
}

func TestAStaleRunSavedWithTheOldWordingReadsInPlainWords(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newAnalysisFreshnessService(t, server.URL, false)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	old := cloneAnalysisRun(completedAnalysisRun(t, s))
	old.Status = AnalysisRunStale
	old.Reason = legacyAnalysisRunStaleReason
	refreshAnalysisSections(old)
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
		t.Fatal(err)
	}
	run, err := loadAnalysisRun(root)
	if err != nil || run == nil || run.Status != AnalysisRunStale || run.Reason != analysisRunStaleReason {
		t.Fatalf("run = %+v, %v", run, err)
	}
}
