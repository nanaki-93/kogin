package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

func TestAnalysisRunFailedSavesStopRequestsAndRecoverWithoutResettingEvidence(t *testing.T) {
	for _, point := range []string{"admission", "reservation", "result", "completion"} {
		t.Run(point, func(t *testing.T) {
			server, calls := analysisResponseServer(t, emptyAnalysisReply)
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			var fault atomic.Bool
			fault.Store(true)
			s.writeAnalysisRun = func(name string, data []byte, mode os.FileMode) error {
				var run AnalysisRun
				if err := json.Unmarshal(data, &run); err != nil {
					return err
				}
				first := run.Files[0].Stages[0]
				fail := point == "admission" && run.Status == AnalysisRunQueued || point == "reservation" && first.Attempts == 1 || point == "result" && first.FindingCount != nil || point == "completion" && run.Status == AnalysisRunCompletedEmpty
				if fault.Load() && fail {
					return errors.New("private-path / secret-provider-value")
				}
				return storage.WriteFile(name, data, mode)
			}
			_, startErr := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
			if (startErr != nil) != (point == "admission") {
				t.Fatalf("start=%v", startErr)
			}
			waitAnalysisWindow(t, s)
			stopped, err := s.currentTestAnalysisRun(context.Background())
			if !errors.Is(err, errAnalysisRunPersistence) || stopped == nil || stopped.Status != AnalysisRunInterrupted {
				t.Fatalf("fault=%+v %v", stopped, err)
			}
			expected := int32(0)
			if point == "result" {
				expected = 1
			}
			if point == "completion" {
				expected = 3
			}
			if calls.Load() != expected || strings.Contains(stopped.Reason, "private") || strings.Contains(stopped.Reason, "secret-provider") {
				t.Fatalf("fault calls=%d progress=%+v", calls.Load(), stopped)
			}
			overview, err := s.ProjectOverview()
			if err != nil || overview.Run.Status != AnalysisRunInterrupted || overview.Coverage.Total != 1 || calls.Load() != expected {
				t.Fatalf("overview lost retained progress after save failure: %+v %v", overview, err)
			}
			if point == "result" {
				cached, err := s.CachedFileAnalysis("main.go")
				if err != nil || cached.Status != project.AnalysisStatusFresh {
					t.Fatal("completed evidence lost after failed progress save")
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			resume := analysisRunPreviewFor(t, s, preview.Limits, &stopped.Identity)
			after, _ := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			if string(before) != string(after) || calls.Load() != expected {
				t.Fatal("preview retried fault or dispatched requests")
			}
			fault.Store(false)
			confirmations := analysisStartFor(resume).Confirmations
			if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: stopped.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
				t.Fatal(err)
			}
			completed := completedAnalysisRun(t, s)
			if completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 {
				t.Fatalf("recovered=%+v calls=%d", completed, calls.Load())
			}
			if point == "reservation" && completed.Files[0].Stages[0].Attempts != 2 {
				t.Fatal("in-memory reservation allowance reset")
			}
		})
	}
}

func TestAnalysisRunRejectsSourcePolicyProviderAndProjectReplacement(t *testing.T) {
	for _, change := range []string{"source", "policy", "provider", "reindex", "project"} {
		t.Run(change, func(t *testing.T) {
			server, calls, started, release := analysisBlockingServer(t)
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			waitForTestSignal(t, started, "request before replacement")
			switch change {
			case "source":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed(){}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "policy":
				if err := os.WriteFile(filepath.Join(root, ".mini-orcaignore"), []byte("main.go\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "provider":
				// No provider metadata is read during the blocked HTTP response; release
				// synchronizes the subsequent validation with this simulated config change.
				s.analysisRun.mu.Lock()
				s.conf().runtimes.analyze.effective.ReasoningEffort = "replacement"
				s.analysisRun.mu.Unlock()
			case "reindex":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed(){}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			case "project":
				other := t.TempDir()
				if err := os.WriteFile(filepath.Join(other, "main.go"), []byte("package other\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.ActivateProject(other, &project.Analysis{Name: "replacement"}); err != nil {
					t.Fatal(err)
				}
			}
			release()
			waitAnalysisWindow(t, s)
			run, err := s.currentTestAnalysisRun(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if change == "project" {
				if run != nil {
					t.Fatal("old progress attached to replacement project")
				}
			} else if run == nil || run.Status != AnalysisRunStale {
				t.Fatalf("stale=%+v", run)
			}
			entries, err := os.ReadDir(filepath.Join(root, ".mini-orca/file-analysis"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 || calls.Load() != 1 {
				t.Fatalf("late evidence=%v calls=%d", entries, calls.Load())
			}
		})
	}
}

func TestAnalysisRunCancelRecoversAWriteFaultAfterSourceInvalidation(t *testing.T) {
	server, calls, started, _ := analysisBlockingServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	var fault atomic.Bool
	s.writeAnalysisRun = func(name string, data []byte, mode os.FileMode) error {
		if fault.Load() {
			return errors.New("unavailable metadata storage")
		}
		return storage.WriteFile(name, data, mode)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "request before invalidation failure")
	fault.Store(true)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	waitAnalysisWindow(t, s)
	stale, err := s.currentTestAnalysisRun(context.Background())
	if !errors.Is(err, errAnalysisRunPersistence) || stale == nil || stale.Status != AnalysisRunStale || calls.Load() != 1 {
		t.Fatalf("stale fault=%+v %v", stale, err)
	}
	fault.Store(false)
	recovered, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: stale.Identity, Action: AnalysisRunCancel})
	if err != nil || recovered.Status != AnalysisRunStale || calls.Load() != 1 {
		t.Fatalf("cancel recovery=%+v %v", recovered, err)
	}
	fresh := analysisRunPreviewFor(t, s, preview.Limits, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(fresh)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Identity.ID == stale.Identity.ID || completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 4 {
		t.Fatalf("replacement=%+v calls=%d", completed, calls.Load())
	}
}
