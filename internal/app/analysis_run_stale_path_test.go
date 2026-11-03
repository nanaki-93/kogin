package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

func TestStalePathAnalysisRetainsTargetAcrossAdmissionAndResume(t *testing.T) {
	ctx := context.Background()
	server, calls, started, release := analysisBlockingServer(t)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	for _, path := range []string{"main.go", "helper.go"} {
		seedRetrySemanticReport(t, s, path, "stale")
	}
	retained, err := s.CachedFileAnalysis("helper.go")
	if err != nil {
		t.Fatal(err)
	}
	retained.Status = "fresh"
	retained.Risks = []project.Finding{{Category: project.FindingCategoryBugs, Severity: "low", Summary: "Review correctness."}}
	cache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Store(*retained); err != nil {
		t.Fatal(err)
	}
	selection := readSelectionFor(t, s)
	request := AnalysisPreviewRequest{ProjectID: selection.ProjectID, ProjectRevision: selection.ProjectRevision, Scope: AnalysisRunScopeProject, StaleOnly: true, StalePath: "main.go", Limits: AnalysisRunLimits{100, 30, 2}}
	preview, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.StalePath != "main.go" || len(scopedFiles(preview)) != 1 || scopedFiles(preview)[0].Path != "main.go" || calls.Load() != 0 {
		t.Fatalf("single-file preview = %+v, calls = %d", preview, calls.Load())
	}
	for _, path := range []string{"", "helper.go"} {
		start := analysisStartFor(preview)
		start.StalePath = path
		if _, err := s.StartAnalysisRun(ctx, start); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
			t.Fatalf("changed target %q admitted: %v, calls = %d", path, err, calls.Load())
		}
	}
	run, err := s.StartAnalysisRun(ctx, analysisStartFor(preview))
	if err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "targeted analysis request")
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunPause}); err != nil {
		t.Fatal(err)
	}
	release()
	paused := completedAnalysisRun(t, s)
	s.analysisRun = &analysisRunController{}
	request.ResumeRun, request.StaleOnly, request.StalePath = &paused.Identity, false, ""
	resume, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil || resume.StalePath != "main.go" || !resume.StaleOnly || len(scopedFiles(resume)) != 1 || scopedFiles(resume)[0].Path != "main.go" || resume.Identity != preview.Identity {
		t.Fatalf("restored target = %+v, %v", resume, err)
	}
	request.StaleOnly, request.StalePath = true, "helper.go"
	if _, err := s.PreviewAnalysisRun(ctx, request); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 1 {
		t.Fatalf("resume retargeted: %v, calls = %d", err, calls.Load())
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	// The run covers the project, keeping the other file's stale outcome.
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunPartial || calls.Load() != 3 {
		t.Fatalf("completed = %s, calls = %d", completed.Status, calls.Load())
	}
	if kept := analysisRunStage(t, completed, "helper.go", AnalysisStageSemantic); kept.Status != AnalysisStageStale || kept.Attempts != 0 {
		t.Fatalf("other file's outcome was not kept: %+v", kept)
	}
	other, err := s.CachedFileAnalysis("helper.go")
	if err != nil || other.Status != "stale" {
		t.Fatalf("other stale file was analyzed: %+v, %v", other, err)
	}
	bugs := 0
	for _, file := range readSavedAnalysisResults(t, s, completed) {
		if count := file.findings(project.FindingCategoryBugs); count > 0 {
			if file.Path != "helper.go" || file.Semantic.Status != "stale" {
				t.Fatalf("saved finding moved or became fresh: %+v", file.Semantic)
			}
			bugs += count
		}
	}
	if retained := retainedAnalysisFiles(completed); len(retained) != 1 || retained[0] != "helper.go" || bugs != 1 {
		t.Fatalf("other file's saved findings were lost: retained=%v bugs=%d", retained, bugs)
	}
	if after := readSelectionFor(t, s); after.SelectionID != selection.SelectionID {
		t.Fatal("single-file analysis changed the saved selection")
	}
}

func TestStalePathAnalysisRejectsUnavailableTargetsAndChangedEvidence(t *testing.T) {
	ctx := context.Background()
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	seedRetrySemanticReport(t, s, "main.go", "stale")
	seedRetrySemanticReport(t, s, "helper.go", "stale")
	selection := readSelectionFor(t, s)
	if _, err := s.SaveAnalysisSelection(ctx, AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{"helper.go"}}); err != nil {
		t.Fatal(err)
	}
	request := AnalysisPreviewRequest{ProjectID: selection.ProjectID, ProjectRevision: selection.ProjectRevision, Scope: AnalysisRunScopeProject, StaleOnly: true, Limits: AnalysisRunLimits{100, 30, 2}}
	for _, path := range []string{"helper.go", "unknown.go"} {
		request.StalePath = path
		if _, err := s.PreviewAnalysisRun(ctx, request); !errors.Is(err, project.ErrRevisionConflict) {
			t.Fatalf("unavailable target %q: %v", path, err)
		}
	}
	request.StalePath = "main.go"
	preview, err := s.PreviewAnalysisRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	seedRetrySemanticReport(t, s, "main.go", "fresh")
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(preview)); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("fresh target admitted from a stale preview: %v", err)
	}
	if current, err := s.PreviewAnalysisRun(ctx, request); err != nil || len(scopedFiles(current)) != 0 {
		t.Fatalf("fresh target became stale work: %+v, %v", current, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("rejected or read-only actions made %d provider requests", calls.Load())
	}
}

func TestStalePathAnalysisRejectsStoredScopeChanges(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	seedRetrySemanticReport(t, s, "main.go", "stale")
	selection := readSelectionFor(t, s)
	preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{
		ProjectID: selection.ProjectID, ProjectRevision: selection.ProjectRevision, Scope: AnalysisRunScopeProject,
		StaleOnly: true, StalePath: "main.go", Limits: AnalysisRunLimits{100, 30, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AnalysisRunPreview){
		"different target": func(plan *AnalysisRunPreview) { plan.StalePath = "other.go" },
		"invalid path":     func(plan *AnalysisRunPreview) { plan.StalePath = "../main.go" },
		"missing mode":     func(plan *AnalysisRunPreview) { plan.StaleOnly = false },
	} {
		t.Run(name, func(t *testing.T) {
			run := newAnalysisRun(*preview)
			mutate(&run.Plan)
			// Rebind the fingerprint to exercise stored scope validation itself.
			queue, err := analysisQueueFingerprint(&run.Plan)
			if err != nil {
				t.Fatal(err)
			}
			run.Plan.Identity.QueueID = queue
			run.Identity.AnalysisQueueIdentity = run.Plan.Identity
			data, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAnalysisRun(root); !errors.Is(err, errAnalysisRunCorrupt) {
				t.Fatalf("stored scope change accepted: %v", err)
			}
		})
	}
}
