package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestAnalysisRefreshReanalyzesIncludedFilesAcrossWindows(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	ctx := context.Background()
	initial := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(initial)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	if calls.Load() != 6 {
		t.Fatalf("initial requests=%d", calls.Load())
	}

	selectFiles := func(excluded []string) {
		t.Helper()
		selection := readSelectionFor(t, s)
		if _, err := s.SaveAnalysisSelection(ctx, AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, excluded}); err != nil {
			t.Fatal(err)
		}
	}
	previewRefresh := func(resume *AnalysisRunIdentity) *AnalysisRunPreview {
		t.Helper()
		analysis, _ := s.manager.Analysis()
		preview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
			ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
			Scope: AnalysisRunScopeProject, Refresh: true, Limits: AnalysisRunLimits{1, 900, 2}, ResumeRun: resume,
		})
		if err != nil {
			t.Fatal(err)
		}
		return preview
	}
	selectFiles([]string{"helper.go"})
	single := previewRefresh(nil)
	if len(single.Files) != 1 || single.Files[0].Path != "main.go" || single.ExpectedModelRequests != 3 || calls.Load() != 6 {
		t.Fatalf("refresh ignored selection or reused model results: %+v, calls=%d", single, calls.Load())
	}
	for _, stage := range single.Files[0].Stages {
		if stage.Cached != (stage.Stage == AnalysisStageSecurityRules) {
			t.Fatalf("refresh cache plan: %+v", stage)
		}
	}
	changed := analysisStartFor(single)
	changed.Refresh = false
	if _, err := s.StartAnalysisRun(ctx, changed); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 6 {
		t.Fatalf("refresh consent changed: %v, calls=%d", err, calls.Load())
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(single)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	if calls.Load() != 9 {
		t.Fatalf("included file was not refreshed: requests=%d", calls.Load())
	}

	selectFiles([]string{})
	all := previewRefresh(nil)
	if len(all.Files) != 2 || all.ExpectedModelRequests != 6 || calls.Load() != 9 {
		t.Fatalf("whole refresh scope: %+v", all)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(all)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || calls.Load() != 12 {
		t.Fatalf("window=%s requests=%d", paused.Status, calls.Load())
	}
	resume := previewRefresh(&paused.Identity)
	if resume.ExpectedModelRequests != 3 {
		t.Fatalf("resume did not retain completed work: %+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 15 {
		t.Fatalf("whole refresh=%s requests=%d", completed.Status, calls.Load())
	}
	for _, file := range completed.Files {
		for _, stage := range file.Stages {
			if stage.Stage != AnalysisStageSecurityRules && (stage.Cached || stage.Attempts != 1) {
				t.Fatalf("model stage was reused or repeated on resume: %s %+v", file.Path, stage)
			}
		}
	}
}

func TestAnalysisClearResultsReplacesPausedRunAndRestartsFromZero(t *testing.T) {
	var emptyReply atomic.Bool
	server, _ := analysisResponseServer(t, func(stage AnalysisStage) string {
		if emptyReply.Load() {
			return emptyAnalysisReply(stage)
		}
		return analysisReply(stage)
	})
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "z.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	firstPreview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{1, 30, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(firstPreview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || paused.WindowFilesCompleted != 1 {
		t.Fatalf("expected paused run after 1 file, got %+v", paused)
	}
	assertSavedAnalysisCounts(t, s, paused, 1, 1, 2)

	clearPreview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: AnalysisRunScopeProject, Refresh: true, ClearResults: true,
		Limits: AnalysisRunLimits{1, 30, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range clearPreview.Files {
		for _, stage := range file.Stages {
			if stage.Cached {
				t.Fatalf("clear_results preview left stage cached: %s %+v", file.Path, stage)
			}
		}
	}
	// Preview is strictly read-only: old findings remain until StartAnalysisRun.
	assertSavedAnalysisCounts(t, s, paused, 1, 1, 2)

	emptyReply.Store(true)
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(clearPreview)); err != nil {
		t.Fatalf("StartAnalysisRun with ClearResults failed to replace paused run: %v", err)
	}
	restartedPaused := completedAnalysisRun(t, s)
	if restartedPaused.Status != AnalysisRunPaused || restartedPaused.Identity.ID == paused.Identity.ID {
		t.Fatalf("restarted run did not replace paused run: %+v", restartedPaused)
	}
	assertSavedAnalysisCounts(t, s, restartedPaused, 0, 0, 0)

	resumePreview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: AnalysisRunScopeProject, Refresh: true, Limits: AnalysisRunLimits{1, 30, 1},
		ResumeRun: &restartedPaused.Identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmations := analysisStartFor(resumePreview).Confirmations
	if _, err := s.ControlAnalysisRun(ctx, AnalysisRunControlRequest{
		Identity: restartedPaused.Identity, Action: AnalysisRunResume,
		PreviewID: resumePreview.PreviewID, Confirmations: &confirmations,
	}); err != nil {
		t.Fatal(err)
	}
	finished := completedAnalysisRun(t, s)
	if finished.Status != AnalysisRunCompletedEmpty {
		t.Fatalf("expected completed_empty run after resume, got %+v", finished)
	}
	assertSavedAnalysisCounts(t, s, finished, 0, 0, 0)
}

func TestAnalysisClearResultsPreservesInProgressIssues(t *testing.T) {
	var emptyReply atomic.Bool
	server, _ := analysisResponseServer(t, func(stage AnalysisStage) string {
		if emptyReply.Load() {
			return emptyAnalysisReply(stage)
		}
		if stage == AnalysisStageSemantic {
			return strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"bugs","severity":"high","summary":"In-progress bug to keep."},{"category":"bugs","severity":"low","summary":"Idle bug to remove."}]`, 1)
		}
		return emptyAnalysisReply(stage)
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	ctx := context.Background()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	firstPreview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{10, 30, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(firstPreview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	findings, err := s.ListFindings(FindingFilter{Source: project.FindingSourceAI})
	if err != nil {
		t.Fatal(err)
	}
	var bugsBefore []project.UnifiedFinding
	for _, finding := range findings {
		if finding.Category == project.FindingCategoryBugs {
			bugsBefore = append(bugsBefore, finding)
		}
	}
	if len(bugsBefore) != 2 {
		t.Fatalf("expected 2 bugs before clear, got %+v", bugsBefore)
	}
	var inProgressID string
	for _, f := range bugsBefore {
		if f.Message == "In-progress bug to keep." {
			inProgressID = f.ID
		}
	}
	if inProgressID == "" {
		t.Fatal("missing target finding ID")
	}
	if _, err := s.OpenChangeSession(ctx, ChangeCreateRequest{
		ProjectID:       analysis.ProjectID,
		ProjectRevision: analysis.ProjectRevision,
		Kind:            "fix",
		Title:           "Fix in-progress bug",
		Paths:           []string{"main.go"},
		FindingTargets: []project.FindingTarget{{
			ProjectID: analysis.ProjectID,
			Kind:      "semantic",
			ID:        inProgressID,
			Path:      "main.go",
		}},
	}); err != nil {
		t.Fatal(err)
	}

	emptyReply.Store(true)
	clearPreview, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: AnalysisRunScopeProject, Refresh: true, ClearResults: true,
		Limits: AnalysisRunLimits{10, 30, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(clearPreview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	bugsAfter := storedFindings(t, s, project.FindingCategoryBugs)
	if len(bugsAfter) != 1 || bugsAfter[0].ID != inProgressID || bugsAfter[0].Status != project.FindingStatusInProgress {
		t.Fatalf("expected only the in-progress bug to remain after ClearResults, got %+v", bugsAfter)
	}
}

// storedFindings lists the active project's saved findings of one category.
func storedFindings(t *testing.T, s *Service, category project.FindingCategory) []project.UnifiedFinding {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		t.Fatal(err)
	}
	findings, err := store.Load(project.FindingInput{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	result := []project.UnifiedFinding{}
	for _, finding := range findings {
		if finding.Category == category {
			result = append(result, finding)
		}
	}
	return result
}
