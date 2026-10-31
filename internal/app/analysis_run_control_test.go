package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestAnalysisRunPauseWaitsForStageAndCancelInterruptsActiveRequest(t *testing.T) {
	for _, action := range []AnalysisRunAction{AnalysisRunPause, AnalysisRunCancel} {
		t.Run(string(action), func(t *testing.T) {
			server, calls, started, release := analysisBlockingServer(t)
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			run, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
			if err != nil {
				t.Fatal(err)
			}
			waitForTestSignal(t, started, "first analysis request")
			changed, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: action})
			if err != nil {
				t.Fatal(err)
			}
			if action == AnalysisRunPause {
				if changed.Status != AnalysisRunPausing {
					t.Fatalf("pause=%+v", changed)
				}
				release()
			}
			settled := completedAnalysisRun(t, s)
			expected := AnalysisRunPaused
			if action == AnalysisRunCancel {
				expected = AnalysisRunCanceled
			}
			if settled.Status != expected || calls.Load() != 1 || settled.Files[0].Stages[0].Attempts != 1 {
				t.Fatalf("settled=%+v calls=%d", settled, calls.Load())
			}
			if action == AnalysisRunPause && settled.Files[0].Stages[0].FindingCount == nil {
				t.Fatal("pause discarded current stage evidence")
			}
			if action == AnalysisRunCancel && settled.Files[0].Stages[0].FindingCount != nil {
				t.Fatal("canceled request published evidence")
			}
		})
	}
}

func TestAnalysisRunConcurrentStartsAndControlsHaveOneWriter(t *testing.T) {
	server, calls, started, release := analysisBlockingServer(t)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); results <- err }()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, errAnalysisRunBusy) {
			t.Fatal(err)
		}
	}
	waitForTestSignal(t, started, "single admitted request")
	if successes != 1 || calls.Load() != 1 {
		t.Fatalf("starts=%d calls=%d", successes, calls.Load())
	}
	run, err := s.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunPause})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	release()
	if got := completedAnalysisRun(t, s); got.Status != AnalysisRunPaused || calls.Load() != 1 {
		t.Fatalf("concurrent pause=%+v", got)
	}
}
