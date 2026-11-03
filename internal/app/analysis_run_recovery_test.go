package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

func recoveryRequestFor(t *testing.T, s *Service, limits AnalysisRunLimits, resume *AnalysisRunIdentity) AnalysisPreviewRequest {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	return AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: limits, RecoverIncomplete: true, ResumeRun: resume}
}

func recoveryPreviewFor(t *testing.T, s *Service, limits AnalysisRunLimits, resume *AnalysisRunIdentity) *AnalysisRunPreview {
	t.Helper()
	preview, err := s.PreviewAnalysisRun(context.Background(), recoveryRequestFor(t, s, limits, resume))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.RecoverIncomplete || preview.Features != nil {
		t.Fatalf("recovery preview lost its scope or attached features: %+v", preview)
	}
	return preview
}

func previewPaths(preview *AnalysisRunPreview) []string {
	paths := []string{}
	for _, file := range scopedFiles(preview) {
		paths = append(paths, file.Path)
	}
	return paths
}

func previewExclusion(preview *AnalysisRunPreview, path string) string {
	for _, file := range preview.Excluded {
		if file.Path == path {
			return file.Reason
		}
	}
	return ""
}

// An empty reason expects no explanation; otherwise the reason must contain it.
func assertRecovery(t *testing.T, got AnalysisRecoverySummary, state AnalysisRecoveryState, files, stages int, reason string) {
	t.Helper()
	reasonMatches := got.Reason == "" && reason == "" || reason != "" && strings.Contains(got.Reason, reason)
	if got.State != state || got.FileCount != files || got.StageCount != stages || !reasonMatches {
		t.Fatalf("recovery=%+v want %s files=%d stages=%d reason~%q", got, state, files, stages, reason)
	}
}

func startAndCompleteAnalysis(t *testing.T, s *Service, preview *AnalysisRunPreview) *AnalysisRun {
	t.Helper()
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	return completedAnalysisRun(t, s)
}

func TestAnalysisRecoverClassifiesStageObservations(t *testing.T) {
	for _, status := range []string{"stale", "failed", "partial", "missing", "canceled", "interrupted", "unavailable"} {
		if got := analysisStageRecoveryFor(status, true); got != analysisStageRecoverable {
			t.Fatalf("%s configured=%d", status, got)
		}
		if got := analysisStageRecoveryFor(status, false); got != analysisStageNeedsModel {
			t.Fatalf("%s unconfigured=%d", status, got)
		}
	}
	for _, status := range []string{"fresh", "pending", "running", "paused", "skipped"} {
		for _, configured := range []bool{true, false} {
			if got := analysisStageRecoveryFor(status, configured); got != analysisStageSettled {
				t.Fatalf("%s configured=%v => %d", status, configured, got)
			}
		}
	}
}

func TestAnalysisRecoverRejectsRefreshAndRetryCombinations(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	ctx := context.Background()
	limits := AnalysisRunLimits{100, 30, 2}
	combinations := map[string]func(refresh, retry *bool){
		"refresh": func(refresh, _ *bool) { *refresh = true },
		"retry":   func(_, retry *bool) { *retry = true },
	}
	for name, combine := range combinations {
		request := recoveryRequestFor(t, s, limits, nil)
		combine(&request.Refresh, &request.RetryStaleFailed)
		if _, err := s.PreviewAnalysisRun(ctx, request); err == nil || errors.Is(err, project.ErrRevisionConflict) {
			t.Fatalf("%s preview accepted: %v", name, err)
		}
	}
	preview := recoveryPreviewFor(t, s, limits, nil)
	for name, combine := range combinations {
		start := analysisStartFor(preview)
		combine(&start.Refresh, &start.RetryStaleFailed)
		if _, err := s.StartAnalysisRun(ctx, start); err == nil || errors.Is(err, project.ErrRevisionConflict) {
			t.Fatalf("%s start accepted: %v", name, err)
		}
	}
	// The option is admission identity: dropping it cannot reuse the recovery preview.
	start := analysisStartFor(preview)
	start.RecoverIncomplete = false
	if _, err := s.StartAnalysisRun(ctx, start); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("broadened admission: %v", err)
	}
	if calls.Load() != 0 || s.analysisRun.run != nil {
		t.Fatalf("rejected recovery dispatched or saved: calls=%d", calls.Load())
	}
}

func TestAnalysisRecoverSelectsIncompleteStagesAndReusesFreshOnes(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	for _, name := range []string{"failed.go", "fresh.go", "stale.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	limits := AnalysisRunLimits{100, 30, 2}
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, limits, nil))
	if calls.Load() != 12 {
		t.Fatalf("initial calls=%d", calls.Load())
	}
	seedRetrySemanticReport(t, s, "failed.go", "failed")
	seedRetrySemanticReport(t, s, "stale.go", "stale")
	_, file, policy := storeCachedPerformanceReport(t, s, root)
	report, err := project.LoadPerformanceFileReport(root, file.Path, file.ContentHash, policy)
	if err != nil || report == nil {
		t.Fatalf("performance report=%+v %v", report, err)
	}
	report.Status = "partial"
	if err := project.StorePerformanceFileReport(root, *report); err != nil {
		t.Fatal(err)
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 3, 3, "")
	// The established stale/failed contract still ignores partial evidence.
	if paths := previewPaths(retryPreviewFor(t, s, nil)); !reflect.DeepEqual(paths, []string{"failed.go", "stale.go"}) {
		t.Fatalf("retry_stale_failed widened: %v", paths)
	}
	preview := recoveryPreviewFor(t, s, limits, nil)
	if paths := previewPaths(preview); !reflect.DeepEqual(paths, []string{"failed.go", "main.go", "stale.go"}) {
		t.Fatalf("recovery paths=%v", paths)
	}
	if !previewRetained(preview, "fresh.go") || previewExclusion(preview, "fresh.go") != "" {
		t.Fatalf("fresh file was not retained: %+v", preview.Files)
	}
	required := map[string]AnalysisStage{"failed.go": AnalysisStageSemantic, "main.go": AnalysisStagePerformance, "stale.go": AnalysisStageSemantic}
	for _, file := range preview.Files {
		for _, stage := range file.Stages {
			if stage.Stage == required[file.Path] {
				if stage.Cached || stage.MaxModelRequests != 2 {
					t.Fatalf("%s %s not reattempted: %+v", file.Path, stage.Stage, stage)
				}
			} else if !stage.Cached || stage.MaxModelRequests != 0 {
				t.Fatalf("%s %s fresh stage not reused: %+v", file.Path, stage.Stage, stage)
			}
		}
	}
	if preview.ExpectedModelRequests != 3 || preview.MaxModelRequests != 6 || preview.SecurityReviewIntentRequired || calls.Load() != 12 {
		t.Fatalf("recovery bounds=%+v calls=%d", preview, calls.Load())
	}
	run := startAndCompleteAnalysis(t, s, preview)
	if run.Status != AnalysisRunCompletedEmpty || !run.Plan.RecoverIncomplete || run.Features != nil || calls.Load() != 15 {
		t.Fatalf("recovery run=%s plan=%v calls=%d", run.Status, run.Plan.RecoverIncomplete, calls.Load())
	}
	if retained := retainedAnalysisFiles(run); !reflect.DeepEqual(retained, []string{"fresh.go"}) {
		t.Fatalf("recovery-omitted evidence not retained: %v", retained)
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryComplete, 0, 0, "")
	if next := recoveryPreviewFor(t, s, limits, nil); len(scopedFiles(next)) != 0 || next.ExpectedModelRequests != 0 || calls.Load() != 15 {
		t.Fatalf("complete recovery=%+v calls=%d", next, calls.Load())
	}
}

func TestAnalysisRecoverIncludesMissingCanceledAndInterruptedStages(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	limits := AnalysisRunLimits{100, 30, 2}
	// Without a saved run every stage is missing, but the client keeps Prepare as the action.
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryNotStarted, 2, 8, "")
	if missing := recoveryPreviewFor(t, s, limits, nil); !reflect.DeepEqual(previewPaths(missing), []string{"helper.go", "main.go"}) || missing.ExpectedModelRequests != 6 {
		t.Fatalf("missing recovery=%+v", missing)
	}
	paused := startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, AnalysisRunLimits{1, 30, 2}, nil))
	if paused.Status != AnalysisRunPaused || calls.Load() != 3 {
		t.Fatalf("paused=%s calls=%d", paused.Status, calls.Load())
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryContinuation, 0, 0, "Continue or cancel")
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "canceled", "canceled")
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 1, 4, "")
	canceled := recoveryPreviewFor(t, s, limits, nil)
	if !reflect.DeepEqual(previewPaths(canceled), []string{"main.go"}) || !previewRetained(canceled, "helper.go") || canceled.ExpectedModelRequests != 3 {
		t.Fatalf("canceled recovery=%+v", canceled)
	}
	// Restore a canceled run whose remaining stages were interrupted by a lost window.
	run := completedAnalysisRun(t, s)
	for j := range run.Files[1].Stages {
		run.Files[1].Stages[j].Status = AnalysisStageInterrupted
	}
	refreshAnalysisSections(run)
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
		t.Fatal(err)
	}
	s.analysisRun = &analysisRunController{}
	assertSelectionStages(t, readSelectionFor(t, s), "main.go", "interrupted", "")
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 1, 4, "")
	interrupted := recoveryPreviewFor(t, s, limits, nil)
	if !reflect.DeepEqual(previewPaths(interrupted), []string{"main.go"}) {
		t.Fatalf("interrupted recovery=%v", previewPaths(interrupted))
	}
	if completed := startAndCompleteAnalysis(t, s, interrupted); completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 6 {
		t.Fatalf("recovered=%s calls=%d", completed.Status, calls.Load())
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryComplete, 0, 0, "")
}

func TestAnalysisRecoverSeparatesConfigurationBlockersFromRecoverableStages(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	limits := AnalysisRunLimits{100, 30, 2}
	client := s.conf().runtimes.bug.client
	s.conf().runtimes.bug.client = nil
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, limits, nil))
	if calls.Load() != 2 {
		t.Fatalf("unconfigured semantic dispatched: calls=%d", calls.Load())
	}
	selection := readSelectionFor(t, s)
	if stage := selection.Files[0].Stages[0]; stage.Status != "unavailable" {
		t.Fatalf("semantic observation=%+v", stage)
	}
	assertRecovery(t, selection.Recovery, AnalysisRecoveryBlocked, 0, 0, "bug model profile used for Code analysis")
	blocked := recoveryPreviewFor(t, s, limits, nil)
	if len(scopedFiles(blocked)) != 0 || blocked.ExpectedModelRequests != 0 || !previewRetained(blocked, "main.go") {
		t.Fatalf("configuration-only work admitted: %+v", blocked)
	}
	// A recoverable stage admits the file; the unconfigured stage keeps its reason and gets no requests.
	storeCachedPerformanceReport(t, s, root)
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 1, 1, "")
	mixed := recoveryPreviewFor(t, s, limits, nil)
	if len(scopedFiles(mixed)) != 1 || mixed.ExpectedModelRequests != 1 {
		t.Fatalf("mixed recovery=%+v", mixed)
	}
	stages := mixed.Files[0].Stages
	if stages[0].Cached || stages[0].MaxModelRequests != 0 || stages[0].Reason != "The model for this stage is not configured." {
		t.Fatalf("blocked stage=%+v", stages[0])
	}
	if stages[1].Cached || stages[1].MaxModelRequests != 2 || !stages[2].Cached || !stages[3].Cached || stages[3].MaxModelRequests != 0 {
		t.Fatalf("mixed stages=%+v", stages)
	}
	// Configuring the model turns the configured-unavailable stage into recoverable work.
	s.conf().runtimes.bug.client = client
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryAvailable, 1, 2, "")
	configured := recoveryPreviewFor(t, s, limits, nil)
	if configured.ExpectedModelRequests != 2 || configured.Files[0].Stages[0].MaxModelRequests != 2 {
		t.Fatalf("configured recovery=%+v", configured)
	}
	if run := startAndCompleteAnalysis(t, s, configured); run.Status != AnalysisRunCompletedEmpty || calls.Load() != 4 {
		t.Fatalf("configured run=%s calls=%d", run.Status, calls.Load())
	}
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryComplete, 0, 0, "")
}

func TestAnalysisRecoverIgnoresExcludedAndUnsupportedFiles(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0, 1, 2, 3}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	limits := AnalysisRunLimits{100, 30, 2}
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, limits, nil))
	seedRetrySemanticReport(t, s, "helper.go", "failed")
	selection := readSelectionFor(t, s)
	for _, file := range selection.Files {
		if file.Path == "data.bin" && (file.Reason == "" || len(file.Stages) != 0) {
			t.Fatalf("unsupported file is selectable: %+v", file)
		}
	}
	assertRecovery(t, selection.Recovery, AnalysisRecoveryAvailable, 1, 1, "")
	saved, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{"helper.go"}})
	if err != nil {
		t.Fatal(err)
	}
	assertRecovery(t, saved.Recovery, AnalysisRecoveryComplete, 0, 0, "")
	assertRecovery(t, readSelectionFor(t, s).Recovery, AnalysisRecoveryComplete, 0, 0, "")
	before := calls.Load()
	preview := recoveryPreviewFor(t, s, limits, nil)
	if len(scopedFiles(preview)) != 0 || preview.ExpectedModelRequests != 0 || calls.Load() != before {
		t.Fatalf("excluded work admitted: %+v", preview)
	}
	if !previewRetained(preview, "main.go") {
		t.Fatalf("main.go was not retained: %+v", preview.Files)
	}
	for path, reason := range map[string]string{"helper.go": analysisSelectionExclusion, "data.bin": "Not a supported text source file."} {
		if got := previewExclusion(preview, path); got != reason {
			t.Fatalf("%s exclusion=%q want %q", path, got, reason)
		}
	}
}

func TestAnalysisRecoverResumeAdoptsCapturedOptionAndRejectsMismatch(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	ctx := context.Background()
	limits := AnalysisRunLimits{1, 30, 2}
	preview := recoveryPreviewFor(t, s, limits, nil)
	paused := startAndCompleteAnalysis(t, s, preview)
	if paused.Status != AnalysisRunPaused || !paused.Plan.RecoverIncomplete || calls.Load() != 3 {
		t.Fatalf("paused=%s recovery=%v calls=%d", paused.Status, paused.Plan.RecoverIncomplete, calls.Load())
	}
	stored, err := loadAnalysisRun(root)
	if err != nil || !stored.Plan.RecoverIncomplete {
		t.Fatalf("stored recovery run=%+v %v", stored, err)
	}
	// Existing clients omit the option on resume; the captured plan stays authoritative.
	s.analysisRun = &analysisRunController{}
	request := recoveryRequestFor(t, s, limits, &paused.Identity)
	request.RecoverIncomplete = false
	resume, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !resume.RecoverIncomplete || resume.Identity != preview.Identity || len(scopedFiles(resume)) != 2 || resume.ExpectedModelRequests != 3 || resume.Features != nil {
		t.Fatalf("resume=%+v", resume)
	}
	if explicit := recoveryPreviewFor(t, s, limits, &paused.Identity); explicit.PreviewID != resume.PreviewID {
		t.Fatal("explicit recovery resume differs from adopted resume")
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	if completed := completedAnalysisRun(t, s); completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 6 {
		t.Fatalf("completed=%s calls=%d", completed.Status, calls.Load())
	}

	other, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	normal := startAndCompleteAnalysis(t, other, analysisRunPreviewFor(t, other, limits, nil))
	before := calls.Load()
	if normal.Status != AnalysisRunPaused {
		t.Fatalf("normal=%s", normal.Status)
	}
	if _, err := other.PreviewAnalysisRun(ctx, recoveryRequestFor(t, other, limits, &normal.Identity)); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != before {
		t.Fatalf("recovery resume of a full run: %v", err)
	}
}

func TestAnalysisRecoverSingleTargetAndRefreshesDocsAfterReindex(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "failed.go"), []byte("package main\nfunc Fail() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	limits := AnalysisRunLimits{100, 30, 2}
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, limits, nil))
	seedRetrySemanticReport(t, s, "failed.go", "failed")
	seedRetrySemanticReport(t, s, "helper.go", "failed")

	req := recoveryRequestFor(t, s, limits, nil)
	req.StalePath = "failed.go"
	preview, err := s.PreviewAnalysisRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previewPaths(preview), []string{"failed.go"}) || preview.ExpectedModelRequests != 1 {
		t.Fatalf("single-target recovery preview=%+v", preview)
	}
	before := calls.Load()
	run := startAndCompleteAnalysis(t, s, preview)
	if run.Status != AnalysisRunPartial || calls.Load() != before+1 || analysisRunStage(t, run, "helper.go", AnalysisStageSemantic).Status != AnalysisStageFailed {
		t.Fatalf("single-target recovery run=%s calls=%d", run.Status, calls.Load()-before)
	}
	overview, err := s.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	// The run ends by writing the project overview for the current index, so
	// the docs are fresh for this revision.
	if overview.Docs == nil || overview.Docs.Status != project.ProjectAnalysisStatusFresh || overview.Docs.ProjectRevision != overview.ProjectRevision {
		t.Fatalf("expected the run to leave fresh docs, got %+v", overview.Docs)
	}
}
