package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// An analysis runs files side by side, up to the agent runtime's concurrent
// calls, each file's stages in order.
func TestAnalysisRunAnalyzesFilesSideBySide(t *testing.T) {
	var mu sync.Mutex
	active, peak := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if reply, ok := docsRefreshReply(request); ok {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: reply}, FinishReason: "stop"}}})
			return
		}
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		stage := AnalysisStageSecurityAI
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			stage = AnalysisStageSemantic
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: emptyAnalysisReply(stage)}, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	writeFixtureFiles(t, root, map[string]string{"extra.go": "package main\n\nfunc Extra() { println(\"extra\") }\n", "more.go": "package main\n\nfunc More() { println(\"more\") }\n"})
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if run.Status != AnalysisRunCompletedEmpty && run.Status != AnalysisRunCompleted {
		t.Fatalf("run = %s %s", run.Status, run.Reason)
	}
	for _, file := range run.Files {
		for _, stage := range file.Stages {
			if analysisStageNeedsWork(stage.Status) {
				t.Fatalf("%s %s left %s", file.Path, stage.Stage, stage.Status)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Fatalf("model calls never overlapped (peak %d)", peak)
	}
}

func TestAnalysisLaneCountFollowsTheConcurrentCalls(t *testing.T) {
	for concurrent, want := range map[int]int{0: 1, 1: 1, 4: 4, 32: 8} {
		if got := analysisLaneCount(concurrent); got != want {
			t.Errorf("analysisLaneCount(%d) = %d, want %d", concurrent, got, want)
		}
	}
	run := &AnalysisRun{Files: []AnalysisRunFile{{Stages: []AnalysisStageProgress{{Status: AnalysisStageCompleted}, {Status: AnalysisStagePending}, {Status: AnalysisStagePending}}}}}
	if !analysisStageReady(run, 0, 1) || analysisStageReady(run, 0, 2) || analysisStageReady(run, 0, 0) || analysisStageReady(run, 1, 0) {
		t.Fatal("a stage is ready only after its file's earlier stages settle")
	}
}

// Retrying an interrupted or canceled run does not redo the stages it never
// reached when their saved reports are still usable; a run that ignored
// saved reports, and stages that failed, are still redone.
func TestAnalysisRetryReusesSavedReportsOfStagesTheRunNeverReached(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil))
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		t.Fatal(err)
	}
	var file project.IndexFile
	for _, indexed := range index.Files {
		if indexed.Path == "main.go" {
			file = indexed
		}
	}
	run := &AnalysisRun{}
	evidence := analysisSelectionEvidence{run: run}
	for _, test := range []struct {
		status  string
		refresh bool
		want    bool
	}{{"canceled", false, true}, {"interrupted", false, true}, {"canceled", true, false}, {"failed", false, false}, {"stale", false, false}} {
		run.Plan.Refresh = test.refresh
		if got := s.unreachedWithSavedReport(*analysis, file, policy, AnalysisStageSemantic, test.status, evidence, nil); got != test.want {
			t.Errorf("%s (refresh %v): reuse = %v, want %v", test.status, test.refresh, got, test.want)
		}
	}
}
