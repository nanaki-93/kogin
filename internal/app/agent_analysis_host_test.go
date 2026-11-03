package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestAnalysisNativeWindowOwnsModelHistoryWithoutChangingEvidence(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	store, _, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.History(t.Context())
	if err != nil || len(before.Runs) != 0 || calls.Load() != 0 {
		t.Fatalf("passive history dispatched work: %+v %v", before, err)
	}
	run := startAndCompleteAnalysis(t, s, preview)
	if run.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 || run.Files[0].Stages[2].Attempts != 0 {
		t.Fatalf("native graph changed evidence or deterministic rules: %+v calls=%d", run, calls.Load())
	}
	history, err := store.History(t.Context())
	// Three more runs are the docs refresh after the window: a component
	// summary and the overview's knowledge run with its request.
	if err != nil || len(history.Runs) != 7 {
		t.Fatalf("history=%+v %v", history, err)
	}
	parent := ""
	for _, invocation := range history.Runs {
		if invocation.SchemaVersion == "analysis-graph-v1" {
			parent = invocation.ID
			session, err := store.Session(t.Context(), invocation.Session)
			if err != nil {
				t.Fatal(err)
			}
			var input agentruntime.AnalysisGraphInput
			if err := json.Unmarshal(session.State["mini_orca.analysis_graph"], &input); err != nil || input.Snapshot.RunID != run.Identity.ID || input.Snapshot.Generation != run.Identity.Generation || input.Snapshot.QueueID != run.Identity.QueueID || len(input.Work) != 4 {
				t.Fatalf("wrong captured admission: %+v %v", input, err)
			}
		}
	}
	if parent == "" {
		t.Fatal("missing native analysis root")
	}
	window, knowledge := 0, ""
	var windowRoot agentstate.RunSnapshot
	for _, invocation := range history.Runs {
		if invocation.ID == parent {
			windowRoot = invocation
		}
		switch {
		case invocation.ID == parent || invocation.ParentID == parent:
			window++
		case invocation.SchemaVersion == "knowledge-graph-v1":
			knowledge = invocation.ID
		}
	}
	for _, invocation := range history.Runs {
		docs := invocation.ID == knowledge || invocation.ParentID == knowledge && knowledge != "" || invocation.ParentID == "" && invocation.SchemaVersion == "completion-v1"
		if invocation.ID != parent && invocation.ParentID != parent && !docs {
			t.Fatalf("detached provider attempt: %+v", invocation)
		}
	}
	if window != 4 {
		t.Fatalf("window runs = %d, want 4", window)
	}
	if windowRoot.Status != "completed" || windowRoot.Usage.ModelCalls != 5 { // three stages, then the docs refresh's two requests
		t.Fatalf("native window receipt=%+v", windowRoot)
	}
	var events []agentstate.ProgressEvent
	for _, event := range history.Events {
		if event.RootID == parent {
			events = append(events, event)
		}
	}
	data, err := json.Marshal(events)
	if err != nil || strings.Contains(string(data), "main.go") || strings.Contains(string(data), "package main") || !strings.Contains(string(data), "analysis_0002_security_rules") {
		t.Fatalf("incorrect public progress: %s %v", data, err)
	}
}

func TestAnalysisNativeCancellationPreservesCanceledDomainState(t *testing.T) {
	server, calls, started, _ := analysisBlockingServer(t)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(t.Context(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "native analysis model request")
	history, _ := agentJournalHistory(t, s)
	id := ""
	for _, run := range history.Runs {
		if run.SchemaVersion == "analysis-graph-v1" && run.ParentID == "" && run.Status == "running" {
			id = run.ID
		}
	}
	if id == "" {
		t.Fatal("active analysis has no running native root")
	}
	admitted, err := s.currentTestAnalysisRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlAnalysisRun(t.Context(), AnalysisRunControlRequest{Identity: admitted.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
	settled := completedAnalysisRun(t, s)
	stage := settled.Files[0].Stages[0]
	if settled.Status != AnalysisRunCanceled || stage.Status != AnalysisStageCanceled || stage.Attempts != 1 || stage.FindingCount != nil || calls.Load() != 1 {
		t.Fatalf("cancel became paused or published evidence: %+v calls=%d", settled, calls.Load())
	}
	history, _ = agentJournalHistory(t, s)
	var root agentstate.RunSnapshot
	for _, run := range history.Runs {
		if run.ID == id {
			root = run
		}
	}
	if root.Status != "canceled" || root.Usage.ActiveCalls != 0 {
		t.Fatalf("unsettled native cancellation: %+v", root)
	}
}

func TestAnalysisGraphCaptureBoundsInventoryAndReusesSettledStages(t *testing.T) {
	run := &AnalysisRun{Plan: AnalysisRunPreview{Limits: AnalysisRunLimits{500, 30, 2}}}
	for i := 0; i < 1200; i++ {
		file := AnalysisRunFile{AnalysisFileIdentity: AnalysisFileIdentity{ContentHash: "hash"}}
		for _, stage := range analysisStages {
			file.Stages = append(file.Stages, AnalysisStageProgress{Stage: stage, Status: AnalysisStagePending})
		}
		run.Files = append(run.Files, file)
	}
	input := captureAnalysisGraph(run)
	if len(input.Work) != 2000 || input.Work[1999].FileIndex != 499 {
		t.Fatalf("window truncated inventory or exceeded batch: %+v", input.Work[len(input.Work)-1])
	}
	run.Plan.Limits.BatchFiles, run.WindowFilesCompleted = 3, 2
	for j := range run.Files[0].Stages {
		run.Files[0].Stages[j].Status = AnalysisStageCompleted
	}
	run.Files[1].Stages[0].Status = AnalysisStagePartial
	run.Files[1].Stages[1].Status = AnalysisStageUnavailable
	input = captureAnalysisGraph(run)
	if len(input.Work) != 2 || input.Work[0].FileIndex != 1 || input.Work[0].Stage != "security_rules" || input.Work[0].Ordinal != 0 {
		t.Fatalf("capture replayed settled evidence: %+v", input.Work)
	}
}

func TestAnalysisGraphHostRejectsChangedTicketsBeforeDispatch(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	c := s.analysisRun
	c.run, c.root, c.admission = newAnalysisRun(*preview), root, preview
	c.run.Status = AnalysisRunRunning
	input := captureAnalysisGraph(c.run)
	for _, change := range []func(*AnalysisRunIdentity, *agentruntime.AnalysisWork){
		func(id *AnalysisRunIdentity, _ *agentruntime.AnalysisWork) { id.Generation = "old" },
		func(_ *AnalysisRunIdentity, work *agentruntime.AnalysisWork) { work.FileHash = "old" },
		func(_ *AnalysisRunIdentity, work *agentruntime.AnalysisWork) { work.StageIndex++ },
		func(_ *AnalysisRunIdentity, work *agentruntime.AnalysisWork) { work.FileIndex++ },
	} {
		identity, work := c.run.Identity, input.Work[0]
		change(&identity, &work)
		if _, err := s.runAnalysisGraphNode(t.Context(), identity, work); !errors.Is(err, project.ErrRevisionConflict) {
			t.Fatalf("changed ticket accepted: %v", err)
		}
	}
	if calls.Load() != 0 || c.run.Files[0].Stages[0].Attempts != 0 {
		t.Fatal("rejected native ticket consumed provider work")
	}
}
