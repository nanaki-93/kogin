package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func readSelectionFor(t *testing.T, s *Service) *AnalysisFileSelection {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReadAnalysisSelection(context.Background(), analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSelectionStages(t *testing.T, selection *AnalysisFileSelection, path, status, reason string) {
	t.Helper()
	for _, file := range selection.Files {
		if file.Path != path {
			continue
		}
		if len(file.Stages) != 4 {
			t.Fatalf("stage coverage missing: %+v", file)
		}
		for _, stage := range file.Stages {
			if stage.Status != status || !strings.Contains(stage.Reason, reason) {
				t.Fatalf("%s: %+v expected %s / %s", path, stage, status, reason)
			}
		}
		return
	}
	t.Fatalf("file not in checklist: %s", path)
}

func TestAnalysisSelectionReportsMissingFreshAndUnindexedChanges(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "missing", "No saved analysis")
	if calls.Load() != 0 {
		t.Fatal("status read dispatched")
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "fresh", "up to date")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "stale", "changed since indexing")
	if calls.Load() != 3 {
		t.Fatalf("unexpected calls %d", calls.Load())
	}
}

func TestAnalysisSelectionExplainsPausedCanceledAndIgnoredFiles(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{1, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	assertSelectionStages(t, readSelectionFor(t, s), "helper.go", "fresh", "up to date")
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "paused", "Resume")
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "canceled", "canceled")
	selection := readSelectionFor(t, s)
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{"main.go"}}); err != nil {
		t.Fatal(err)
	}
	next := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(next)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "missing", "Excluded from the previous run")
	if calls.Load() != 3 {
		t.Fatalf("status reads dispatched: %d", calls.Load())
	}
}

func TestChangedFileDoesNotMakeUnfinishedUnchangedFilesStale(t *testing.T) {
	ctx := context.Background()
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{1, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	if err := os.WriteFile(filepath.Join(root, "helper.go"), []byte("package main\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	selection := readSelectionFor(t, s)
	assertSelectionStages(t, selection, "helper.go", "stale", "this file")
	assertSelectionStages(t, selection, "main.go", "interrupted", "before this stage")
	request := AnalysisPreviewRequest{ProjectID: selection.ProjectID, ProjectRevision: selection.ProjectRevision, Scope: AnalysisRunScopeProject, StaleOnly: true, Limits: preview.Limits}
	retry, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil || len(scopedFiles(retry)) != 1 || scopedFiles(retry)[0].Path != "helper.go" || calls.Load() != 3 {
		t.Fatalf("stale-only scope includes unchanged unfinished files: %+v, %v", retry, err)
	}
}

func TestAnalysisSelectionExplainsStageFailuresAndUnavailableProviders(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	s.conf().runtimes.bug.client = nil
	selection := readSelectionFor(t, s)
	for _, file := range selection.Files {
		if file.Path == "main.go" {
			if file.Stages[0].Status != "unavailable" || !strings.Contains(file.Stages[0].Reason, "not configured") {
				t.Fatalf("no provider explanation: %+v", file.Stages[0])
			}
		}
	}
	for _, test := range []struct {
		stage AnalysisStageStatus
		run   AnalysisRunStatus
		want  string
	}{
		{AnalysisStageFailed, AnalysisRunFailed, "failed"},
		{AnalysisStagePartial, AnalysisRunPartial, "incomplete"},
		{AnalysisStageInterrupted, AnalysisRunInterrupted, "interrupted"},
		{AnalysisStageCanceled, AnalysisRunCanceled, "canceled"},
		{AnalysisStageUnavailable, AnalysisRunUnavailable, "unavailable"},
		{AnalysisStageRunning, AnalysisRunRunning, "processing"},
	} {
		status, reason := selectionRunStage(AnalysisStageProgress{Status: test.stage}, &AnalysisRun{Status: test.run})
		if status != string(test.stage) || !strings.Contains(reason, test.want) {
			t.Fatalf("%s: %s %s", test.stage, status, reason)
		}
	}
	status, reason := selectionRunStage(AnalysisStageProgress{Status: AnalysisStageFailed, Reason: "The stage needs an additional attempt allowance."}, &AnalysisRun{Status: AnalysisRunFailed})
	if status != "failed" || reason != "The stage needs an additional attempt allowance." {
		t.Fatal("lost saved failure reason")
	}
}

func TestAnalysisSelectionExplainsNewFilesAndUnreadableStageEvidence(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := readSelectionFor(t, s)
	found := false
	for _, file := range selection.Files {
		if file.Path == "new.go" {
			found = true
			if !strings.Contains(file.Reason, "Not indexed yet") {
				t.Fatalf("new file: %+v", file)
			}
		}
	}
	if !found {
		t.Fatal("new file omitted")
	}
	s.loadSecurityFileReport = func(string, project.SecurityReportInput) (*project.SecurityFileReport, error) {
		return nil, errors.New("unreadable evidence")
	}
	selection = readSelectionFor(t, s)
	for _, file := range selection.Files {
		if file.Path == "main.go" {
			for _, stage := range file.Stages[2:] {
				if stage.Status != "unavailable" || !strings.Contains(stage.Reason, "could not be read") {
					t.Fatalf("read error hidden: %+v", stage)
				}
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("inspection dispatched analysis")
	}
}

func TestAnalysisSelectionLimitedRunSkipsDoNotClaimCompleteCoverage(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		t.Fatal(err)
	}
	file := index.Files[0]
	evidence := analysisSelectionEvidence{files: map[string]AnalysisRunFile{file.Path: {AnalysisFileIdentity: AnalysisFileIdentity{Path: file.Path, ContentHash: file.ContentHash}, Stages: []AnalysisStageProgress{{Stage: AnalysisStagePerformance, Status: AnalysisStageSkipped, Reason: "Stage was skipped before analysis."}}}}}
	stage := s.analysisSelectionStage(*analysis, file, policy, AnalysisStagePerformance, evidence)
	if stage.Status != "missing" || !strings.Contains(stage.Reason, "Stage was skipped") {
		t.Fatalf("omitted stage hidden: %+v", stage)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	stage = s.analysisSelectionStage(*analysis, file, policy, AnalysisStagePerformance, evidence)
	if stage.Status != "fresh" {
		t.Fatalf("current cache hidden by old skipped stage: %+v", stage)
	}
}

func TestAnalysisSelectionRecoveryFollowsRunLifecycleAndSourceChanges(t *testing.T) {
	server, calls, started, release := analysisBlockingServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	ctx := context.Background()
	limits := AnalysisRunLimits{100, 30, 2}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryNotStarted, 1, 4, "")
	run, err := s.StartAnalysisRun(ctx, analysisStartFor(analysisRunPreviewFor(t, s, limits, nil)))
	if err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "first analysis request")
	if summary := readSelectionFor(t, s).Recovery; summary.State != AnalysisRecoveryRunning || summary.Reason == "" {
		t.Fatalf("active run recovery=%+v", summary)
	}
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunPause}); err != nil {
		t.Fatal(err)
	}
	release()
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused {
		t.Fatalf("paused=%s", paused.Status)
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryContinuation, 0, 0, "run page")
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 1, 3, "")
	repair := recoveryPreviewFor(t, s, limits, nil)
	if repair.ExpectedModelRequests != 2 || !repair.Files[0].Stages[0].Cached {
		t.Fatalf("repair after cancel=%+v", repair)
	}
	if completed := startAndCompleteAnalysis(t, s, repair); completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 {
		t.Fatalf("repaired=%s calls=%d", completed.Status, calls.Load())
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryComplete, 0, 0, "")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryBlocked, 0, 0, "Refresh project files")
	if calls.Load() != 3 {
		t.Fatalf("selection reads dispatched: %d", calls.Load())
	}
}

func TestAnalysisSelectionRecoveryStateOrderAndCounts(t *testing.T) {
	s := &Service{analysisRun: &analysisRunController{}}
	files := map[string]analysisFileRecovery{
		"recoverable.go": {recoverable: 2},
		"model.go":       {codeModelMissing: true},
		"changed.go":     {sourceBlocked: true},
		"ignored.go":     {recoverable: 5, sourceBlocked: true, performanceModelMissing: true},
	}
	evidence := func(paths ...string) analysisRecoveryEvidence {
		selected := map[string]analysisFileRecovery{}
		for _, path := range paths {
			selected[path] = files[path]
		}
		return analysisRecoveryEvidence{projectID: "project", files: selected}
	}
	ignored := []string{"ignored.go"}
	all := evidence("recoverable.go", "model.go", "changed.go", "ignored.go")
	assertRecovery(t, s.analysisRecoverySummaryLocked(all, ignored), AnalysisRecoveryNotStarted, 1, 2, "")
	s.analysisRun.run = &AnalysisRun{Identity: AnalysisRunIdentity{AnalysisQueueIdentity: AnalysisQueueIdentity{ProjectID: "other"}}, Status: AnalysisRunPaused}
	assertRecovery(t, s.analysisRecoverySummaryLocked(all, ignored), AnalysisRecoveryNotStarted, 1, 2, "")
	s.analysisRun.run.Identity.ProjectID = "project"
	s.analysisRun.run.Status = AnalysisRunCompleted
	assertRecovery(t, s.analysisRecoverySummaryLocked(all, ignored), AnalysisRecoveryBlocked, 1, 2, "Refresh project files")
	assertRecovery(t, s.analysisRecoverySummaryLocked(evidence("recoverable.go", "model.go"), nil), AnalysisRecoveryAvailable, 1, 2, "")
	assertRecovery(t, s.analysisRecoverySummaryLocked(evidence("model.go"), nil), AnalysisRecoveryBlocked, 0, 0, "the bug model profile used for Code analysis")
	review := evidence("ignored.go")
	review.files["ignored.go"] = analysisFileRecovery{performanceModelMissing: true}
	review.models = &AnalysisModels{Code: "bug", Review: "code", Features: "analyze"}
	assertRecovery(t, s.analysisRecoverySummaryLocked(review, nil), AnalysisRecoveryBlocked, 0, 0, "the code model profile used for Performance")
	assertRecovery(t, s.analysisRecoverySummaryLocked(evidence(), nil), AnalysisRecoveryComplete, 0, 0, "")
	// Persistence faults and active workers supersede saved-selection work, as at admission.
	s.analysisRun.fault = errAnalysisRunPersistence
	assertRecovery(t, s.analysisRecoverySummaryLocked(all, ignored), AnalysisRecoveryContinuation, 1, 2, "Continue or cancel")
	s.analysisRun.done = make(chan struct{})
	assertRecovery(t, s.analysisRecoverySummaryLocked(all, ignored), AnalysisRecoveryRunning, 1, 2, "in progress")
}
