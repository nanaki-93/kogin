package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func seedRetrySemanticReport(t *testing.T, s *Service, path, status string) {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.manager.IndexedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cache, input, err := s.fileAnalysisCacheInput(analysis, file, file.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	report, err := cache.Load(input)
	if err != nil {
		t.Fatal(err)
	}
	report.Status = status
	report.ContentHash = file.ContentHash
	report.Purpose = "Describes the file."
	if status == "stale" {
		report.Status = "fresh"
		report.ContentHash = "sha256:previous"
	}
	if err := cache.Store(*report); err != nil {
		t.Fatal(err)
	}
}

func retryPreviewFor(t *testing.T, s *Service, resume *AnalysisRunIdentity) *AnalysisRunPreview {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject,
		Limits: AnalysisRunLimits{1, 30, 2}, RetryStaleFailed: true, ResumeRun: resume,
	})
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestStaleOnlyAnalysisSkipsUnrelatedFailuresAndRetainsScopeOnResume(t *testing.T) {
	ctx := context.Background()
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	for _, name := range []string{"fresh.go", "failed.go", "stale.go", "stale_two.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"fresh", "failed", "stale"} {
		seedRetrySemanticReport(t, s, status+".go", status)
	}
	seedRetrySemanticReport(t, s, "stale_two.go", "stale")
	analysis, _ := s.manager.Analysis()
	request := AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject, StaleOnly: true, Limits: AnalysisRunLimits{1, 30, 2}}
	preview, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedFiles(preview)) != 2 || scopedFiles(preview)[0].Path != "stale.go" || scopedFiles(preview)[1].Path != "stale_two.go" || calls.Load() != 0 {
		t.Fatalf("stale-only selection = %+v", preview.Files)
	}
	start := analysisStartFor(preview)
	start.StaleOnly = false
	if _, err := s.StartAnalysisRun(ctx, start); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("widened stale-only admission = %v", err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	s.analysisRun = &analysisRunController{}
	request.ResumeRun, request.StaleOnly = &paused.Identity, false
	resume, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil || !resume.StaleOnly || len(scopedFiles(resume)) != 2 || resume.ExpectedModelRequests != 3 {
		t.Fatalf("resume = %+v, %v", resume, err)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	// The run covers the project: the unrelated failure is kept, so it is partial.
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunPartial || calls.Load() != 6 {
		t.Fatalf("completed = %s, calls = %d", completed.Status, calls.Load())
	}
	if kept := analysisRunStage(t, completed, "failed.go", AnalysisStageSemantic); kept.Status != AnalysisStageFailed {
		t.Fatalf("unrelated failure was not kept: %+v", kept)
	}
	if kept := analysisRunStage(t, completed, "fresh.go", AnalysisStageSemantic); kept.Status != AnalysisStageCompletedEmpty || !kept.Cached {
		t.Fatalf("fresh result was not kept: %+v", kept)
	}
	failed, err := s.CachedFileAnalysis("failed.go")
	if err != nil || failed.Status != "failed" {
		t.Fatalf("stale-only run retried unrelated failure: %+v, %v", failed, err)
	}
}

func TestAnalysisRetrySelectsStaleAndFailedFilesAndPreservesSelectionOnResume(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	for _, name := range []string{"fresh.go", "failed.go", "stale.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"fresh", "failed", "stale"} {
		seedRetrySemanticReport(t, s, status+".go", status)
	}
	preview := retryPreviewFor(t, s, nil)
	if paths := previewPaths(preview); !reflect.DeepEqual(paths, []string{"failed.go", "stale.go"}) || calls.Load() != 0 || preview.ExpectedModelRequests != 6 {
		t.Fatalf("preview paths=%v requests=%d calls=%d", paths, preview.ExpectedModelRequests, calls.Load())
	}
	// Dropping the retry option cannot widen an already reviewed selection.
	start := analysisStartFor(preview)
	start.RetryStaleFailed = false
	if _, err := s.StartAnalysisRun(context.Background(), start); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("broadened admission: %v", err)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || calls.Load() != 3 {
		t.Fatalf("paused=%s calls=%d", paused.Status, calls.Load())
	}
	// Restore durable progress after the first selected file has become fresh.
	s.analysisRun = &analysisRunController{}
	resume := retryPreviewFor(t, s, &paused.Identity)
	if resume.Identity != preview.Identity || len(scopedFiles(resume)) != 2 || resume.ExpectedModelRequests != 3 {
		t.Fatalf("resume=%+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	// main.go was never analyzed, so the whole-project run stays partial.
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunPartial || calls.Load() != 6 {
		t.Fatalf("completed=%s calls=%d", completed.Status, calls.Load())
	}
	if kept := analysisRunStage(t, completed, "main.go", AnalysisStageSemantic); kept.Status != AnalysisStageSkipped || kept.Reason != carriedMissingReason {
		t.Fatalf("unanalyzed file outcome=%+v", kept)
	}
	next := retryPreviewFor(t, s, nil)
	if len(scopedFiles(next)) != 0 || next.ExpectedModelRequests != 0 || next.SecurityReviewIntentRequired || calls.Load() != 6 {
		t.Fatalf("empty retry=%+v calls=%d", next, calls.Load())
	}
	missing, err := s.CachedFileAnalysis("main.go")
	if err != nil || missing.Status != "missing" {
		t.Fatalf("unanalyzed file was included: %+v %v", missing, err)
	}
}

func TestAnalysisRetryRejectsEvidenceChangesAfterPreview(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	seedRetrySemanticReport(t, s, "main.go", "failed")
	preview := retryPreviewFor(t, s, nil)
	seedRetrySemanticReport(t, s, "main.go", "fresh")
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("changed evidence admitted: %v calls=%d", err, calls.Load())
	}
}

func TestAnalysisRetryIncludesDurableFailuresAndReusesFreshStages(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
		if fail.Load() && stage == AnalysisStagePerformance {
			return "invalid model output"
		}
		return emptyAnalysisReply(stage)
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Files[0].Stages[1].Status != AnalysisStageFailed {
		t.Fatalf("failure fixture=%+v", completed)
	}
	fail.Store(false)
	retry := retryPreviewFor(t, s, nil)
	if len(scopedFiles(retry)) != 1 || retry.ExpectedModelRequests != 1 {
		t.Fatalf("retry=%+v", retry)
	}
	before := calls.Load()
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(retry)); err != nil {
		t.Fatal(err)
	}
	completed = completedAnalysisRun(t, s)
	if calls.Load() != before+1 || completed.Status != AnalysisRunCompletedEmpty {
		t.Fatalf("fresh stages re-requested: status=%s calls=%d", completed.Status, calls.Load()-before)
	}
}

func TestAnalysisRetryDoesNotOverrideNewerPartialEvidence(t *testing.T) {
	server, _ := analysisResponseServer(t, func(stage AnalysisStage) string {
		if stage == AnalysisStagePerformance {
			return "invalid model output"
		}
		return emptyAnalysisReply(stage)
	})
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Files[0].Stages[1].Status != AnalysisStageFailed {
		t.Fatalf("failure fixture=%+v", completed)
	}
	_, file, policy := storeCachedPerformanceReport(t, s, root)
	report, err := project.LoadPerformanceFileReport(root, file.Path, file.ContentHash, policy)
	if err != nil || report == nil {
		t.Fatalf("performance report=%+v %v", report, err)
	}
	report.Status = "partial"
	if err := project.StorePerformanceFileReport(root, *report); err != nil {
		t.Fatal(err)
	}
	if retry := retryPreviewFor(t, s, nil); len(scopedFiles(retry)) != 0 {
		t.Fatalf("old failure overrode newer partial evidence: %+v", retry)
	}
	selection := readSelectionFor(t, s)
	if selection.Files[0].Stages[1].Status != "partial" {
		t.Fatalf("partial evidence mislabeled: %+v", selection)
	}
	report.Status = "completed"
	runtime := s.conf().runtimes.analyze
	report.Model = runtime.profile.Model
	report.Profile, report.Scope = runtime.effective.Profile, runtime.effective.Scope
	report.ProviderOrigin, report.ReasoningEffort = runtime.effective.ProviderOrigin, runtime.effective.ReasoningEffort
	report.Findings = []project.PerformanceFinding{}
	if err := project.StorePerformanceFileReport(root, *report); err != nil {
		t.Fatal(err)
	}
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "fresh", "up to date")
	if retry := retryPreviewFor(t, s, nil); len(scopedFiles(retry)) != 0 {
		t.Fatalf("newer success still requires retry: %+v", retry)
	}
}

func TestAnalysisRetryRefreshFailuresOverrideOlderCachesAcrossResume(t *testing.T) {
	var fail atomic.Bool
	server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
		if fail.Load() && (stage == AnalysisStagePerformance || stage == AnalysisStageSecurityAI) {
			return "invalid model output"
		}
		return emptyAnalysisReply(stage)
	})
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	ctx := context.Background()
	initial := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(initial)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	fail.Store(true)
	refresh, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{ProjectID: initial.Identity.ProjectID, ProjectRevision: initial.Identity.ProjectRevision, Scope: AnalysisRunScopeProject, Refresh: true, Limits: initial.Limits})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(refresh)); err != nil {
		t.Fatal(err)
	}
	failed := completedAnalysisRun(t, s)
	if failed.Status != AnalysisRunPartial || calls.Load() != 12 {
		t.Fatalf("refresh=%+v calls=%d", failed, calls.Load())
	}
	s.analysisRun = &analysisRunController{}
	selection := readSelectionFor(t, s)
	for _, file := range selection.Files {
		for _, stage := range file.Stages {
			if (stage.Stage == AnalysisStagePerformance || stage.Stage == AnalysisStageSecurityAI) && stage.Status != "failed" {
				t.Fatalf("older cache hid failed refresh: %+v", stage)
			}
		}
	}
	overview, err := s.ProjectOverview()
	if err != nil || overview.Coverage.Failed != 2 {
		t.Fatalf("summary hid failed refresh: %+v err=%v", overview, err)
	}
	fail.Store(false)
	retry := retryPreviewFor(t, s, nil)
	if len(scopedFiles(retry)) != 2 || retry.ExpectedModelRequests != 4 {
		t.Fatalf("retry=%+v", retry)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(retry)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || calls.Load() != 14 {
		t.Fatalf("paused=%+v calls=%d", paused, calls.Load())
	}
	s.analysisRun = &analysisRunController{}
	resume := retryPreviewFor(t, s, &paused.Identity)
	if resume.ExpectedModelRequests != 2 {
		t.Fatalf("resume=%+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 16 {
		t.Fatalf("completed=%+v calls=%d", completed, calls.Load())
	}
	for _, path := range []string{"main.go", "helper.go"} {
		assertSelectionStages(t, readSelectionFor(t, s), path, "fresh", "up to date")
	}
	if retry := retryPreviewFor(t, s, nil); len(scopedFiles(retry)) != 0 {
		t.Fatalf("successful retries still need work: %+v", retry)
	}
}

// Retrying failed files keeps the rest of the project in the run: every file
// stays listed, saved reports are read back without model requests or batch
// use, and the project's findings are counted once.
func TestAnalysisRetryKeepsTheWholeProjectAnalysis(t *testing.T) {
	ctx := context.Background()
	server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
		if stage == AnalysisStageSemantic {
			return strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"bugs","severity":"low","summary":"Review correctness."}]`, 1)
		}
		return emptyAnalysisReply(stage)
	})
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	full := startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil))
	if len(full.Files) != 2 || sectionFindings(full) != 2 {
		t.Fatalf("full run = %+v", full)
	}
	seedRetrySemanticReport(t, s, "main.go", "failed")
	before := calls.Load()

	retry := retryPreviewFor(t, s, nil)
	if paths := previewPaths(retry); !reflect.DeepEqual(paths, []string{"main.go"}) || !previewRetained(retry, "helper.go") || len(retry.Files) != 2 || retry.ExpectedModelRequests != 1 {
		t.Fatalf("retry preview = %+v", retry)
	}
	// The preview allows one file per batch: the retained file must not use it.
	run := startAndCompleteAnalysis(t, s, retry)
	if run.Status != AnalysisRunCompleted || len(run.Files) != 2 || sectionFindings(run) != 2 || calls.Load() != before+1 {
		t.Fatalf("retry run = %s, %d files, %d findings, %d calls", run.Status, len(run.Files), sectionFindings(run), calls.Load()-before)
	}
	if kept := analysisRunStage(t, run, "helper.go", AnalysisStageSemantic); kept.Status != AnalysisStageCompleted || !kept.Cached || kept.Attempts != 0 {
		t.Fatalf("retained file result = %+v", kept)
	}
	if retried := analysisRunStage(t, run, "main.go", AnalysisStageSemantic); retried.Status != AnalysisStageCompleted || retried.Cached || retried.Attempts != 1 {
		t.Fatalf("retried file result = %+v", retried)
	}
	// The saved run, retained files included, reads back after a restart.
	s.analysisRun = &analysisRunController{}
	restored, err := s.currentTestAnalysisRun(ctx)
	if err != nil || restored == nil || restored.Identity != run.Identity || !restored.Plan.Files[0].Retained {
		t.Fatalf("restored run = %+v, %v", restored, err)
	}
	if err := validateStoredAnalysisRun(cloneAnalysisRun(restored)); err != nil {
		t.Fatalf("saved run invalid: %v", err)
	}
	for name, change := range map[string]func(*AnalysisRun){
		"retained dispatch": func(r *AnalysisRun) { r.Plan.Files[0].Stages[0].MaxModelRequests = 1 },
		"carried in scope":  func(r *AnalysisRun) { r.Plan.Files[1].Stages[0].Carried = AnalysisStageFailed },
		"unknown carry": func(r *AnalysisRun) {
			r.Plan.Files[0].Stages[0].Carried, r.Plan.Files[0].Stages[0].Cached = AnalysisStageCompleted, false
		},
	} {
		corrupt := cloneAnalysisRun(restored)
		change(corrupt)
		queue, err := analysisQueueFingerprint(&corrupt.Plan)
		if err != nil {
			t.Fatal(err)
		}
		corrupt.Identity.QueueID, corrupt.Plan.Identity.QueueID = queue, queue
		if err := validateStoredAnalysisRun(corrupt); !errors.Is(err, errAnalysisRunCorrupt) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

// While a retry runs, every other file already shows its saved results, and
// canceling the retry does not turn them into canceled work.
func TestAnalysisRetryShowsSavedResultsAtOnceAndCancelKeepsThem(t *testing.T) {
	ctx := context.Background()
	var gate atomic.Bool
	started, release := make(chan struct{}, 1), make(chan struct{})
	server, _ := analysisResponseServer(t, func(stage AnalysisStage) string {
		if gate.Load() {
			started <- struct{}{}
			<-release
		}
		return emptyAnalysisReply(stage)
	})
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil))
	seedRetrySemanticReport(t, s, "main.go", "failed")

	gate.Store(true)
	run, err := s.StartAnalysisRun(ctx, analysisStartFor(retryPreviewFor(t, s, nil)))
	if err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "retried request")
	running, err := s.currentTestAnalysisRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range analysisStages {
		if kept := analysisRunStage(t, running, "helper.go", stage); kept.Status != AnalysisStageCompletedEmpty || !kept.Cached {
			t.Fatalf("retained %s not shown as done during the retry: %+v", stage, kept)
		}
	}
	if retried := analysisRunStage(t, running, "main.go", AnalysisStageSemantic); retried.Status != AnalysisStageRunning {
		t.Fatalf("retried stage = %+v", retried)
	}
	if done := analysisRunStage(t, running, "main.go", AnalysisStagePerformance); done.Status != AnalysisStageCompletedEmpty || !done.Cached {
		t.Fatalf("saved stage of the retried file = %+v", done)
	}
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
	gate.Store(false)
	close(release)
	canceled := completedAnalysisRun(t, s)
	if canceled.Status != AnalysisRunCanceled || analysisRunStage(t, canceled, "helper.go", AnalysisStageSemantic).Status != AnalysisStageCompletedEmpty || analysisRunStage(t, canceled, "main.go", AnalysisStageSemantic).Status != AnalysisStageCanceled {
		t.Fatalf("cancel changed saved results: %+v", canceled.Files)
	}
	if again := retryPreviewFor(t, s, nil); !reflect.DeepEqual(previewPaths(again), []string{"main.go"}) || !previewRetained(again, "helper.go") {
		t.Fatalf("after cancel, retry = %v", previewPaths(again))
	}
}

func sectionFindings(run *AnalysisRun) int {
	total := 0
	for _, section := range run.Sections {
		if section.FindingCount != nil {
			total += *section.FindingCount
		}
	}
	return total
}
