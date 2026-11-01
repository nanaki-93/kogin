package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestProjectOverviewSeparatesFindingSourcesAndKeepsMissingAnalysisUseful(t *testing.T) {
	service, root, revision := newTrustedService(t, map[string]string{"go.mod": "module fixture\n\ngo 1.22\n", "main.go": "package fixture\nfunc Run() {}\n"})
	manager := service.manager
	analysis, err := manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	analysis.Report = project.ProjectAnalysisReport{
		SchemaVersion: "1", ProjectID: analysis.ProjectID, ProjectRevision: revision,
		Purpose: "Runs the fixture.", Architecture: "Single package.", Components: []string{},
		Risks:  []project.ProjectAnalysisRisk{{Severity: "low", Summary: "Add an integration test."}},
		Status: project.ProjectAnalysisStatusFresh, Profile: "analysis", PromptVersion: "project-analysis-v1", GeneratedAt: time.Now().UTC(),
	}
	if err := manager.Set(root, analysis); err != nil {
		t.Fatal(err)
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileSource(project.FindingInput{ProjectID: analysis.ProjectID, ProjectRevision: revision}, project.FindingSourceVet, []project.UnifiedFinding{{Source: project.FindingSourceVet, Confidence: project.FindingConfidenceToolReported, Severity: "high", Title: "Vet", Message: "bad call", Evidence: "password: must-not-leak"}}); err != nil {
		t.Fatal(err)
	}

	overview, err := service.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.Coverage.Total != 2 || overview.Coverage.Missing != 2 || overview.Findings.Verified != 1 || overview.Findings.AI != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	findings, err := service.ListFindings(FindingFilter{Confidence: project.FindingConfidenceToolReported})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Source != project.FindingSourceVet || findings[0].Evidence != "[redacted]" {
		t.Fatalf("findings = %+v", findings)
	}
	if err := service.UpdateFindingStatus(revision, findings[0].ID, project.FindingStatusDismissed); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateFindingStatus("stale", findings[0].ID, project.FindingStatusOpen); err != project.ErrRevisionConflict {
		t.Fatalf("stale update = %v", err)
	}
}

func TestOverviewCoverageMatchesSelectedFilesAndRetainsDescriptionStatus(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	// Include a file eligible for Performance/Security but not semantic analysis.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	analysis, _ := s.manager.Analysis()
	analysis.Report.Status = project.ProjectAnalysisStatusStale
	if err := s.manager.Set(root, analysis); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completedAnalysisRun(t, s)
	if err := os.WriteFile(filepath.Join(root, "helper.go"), []byte("package main\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := readSelectionFor(t, s)
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{"helper.go"}}); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	overview, err := s.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.Coverage != (AnalysisCoverage{Total: 2, Fresh: 2}) {
		t.Fatalf("excluded or ineligible stages counted: %+v", overview.Coverage)
	}
	// The run ended by writing the description, and reading the coverage
	// does not change it.
	if overview.Analysis.Status != project.ProjectAnalysisStatusFresh {
		t.Fatalf("description status = %s, want the run's fresh one", overview.Analysis.Status)
	}
	selection = readSelectionFor(t, s)
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{}}); err != nil {
		t.Fatal(err)
	}
	overview, err = s.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	if overview.Coverage != (AnalysisCoverage{Total: 3, Fresh: 2, Stale: 1}) {
		t.Fatalf("real source change hidden: %+v", overview.Coverage)
	}
	if calls.Load() != before {
		t.Fatal("coverage read dispatched model work")
	}
}

func TestOverviewCoverageDoesNotHidePartialOrUnavailableAnalysis(t *testing.T) {
	for _, test := range []struct {
		statuses []string
		want     string
	}{
		{[]string{"fresh", "skipped"}, "fresh"},
		{[]string{"skipped"}, "excluded"},
		{[]string{"fresh", "missing"}, "partial"},
		{[]string{"fresh", "partial"}, "partial"},
		{[]string{"fresh", "unavailable"}, "unavailable"},
		{[]string{"fresh", "failed"}, "failed"},
		{[]string{"fresh", "stale"}, "stale"},
		{[]string{"pending"}, "running"},
		{[]string{"paused"}, "partial"},
		{[]string{"unrecognized"}, "unavailable"},
	} {
		stages := []AnalysisFileStageStatus{}
		for _, status := range test.statuses {
			stages = append(stages, AnalysisFileStageStatus{Status: status})
		}
		if got := analysisFileCoverageStatus(stages); got != test.want {
			t.Fatalf("%v: %s, want %s", test.statuses, got, test.want)
		}
	}
}
