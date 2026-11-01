package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestPerformanceAndSecurityFindingsAreIssuesWithTheirLines(t *testing.T) {
	server, _ := analysisResponseServer(t, analysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	startAndCompleteAnalysis(t, s, analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil))
	findings, err := s.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byCategory := map[project.FindingCategory][]project.UnifiedFinding{}
	for _, finding := range findings {
		byCategory[finding.Category] = append(byCategory[finding.Category], finding)
	}
	var performance, security *project.UnifiedFinding
	for i, finding := range byCategory[project.FindingCategoryPerformance] {
		if finding.OriginatingAnalysis == "performance" {
			performance = &byCategory[project.FindingCategoryPerformance][i]
		}
	}
	for i, finding := range byCategory[project.FindingCategorySecurity] {
		if finding.OriginatingAnalysis == "security" {
			security = &byCategory[project.FindingCategorySecurity][i]
		}
	}
	if performance == nil || performance.Title != "Avoid repeated formatting" || performance.Location.StartLine != 5 || performance.Location.Symbol != "Run" || performance.Source != project.FindingSourceAI {
		t.Fatalf("performance issue = %+v (all: %+v)", performance, findings)
	}
	if security == nil || security.Location.StartLine != 5 || security.Location.Symbol != "Run" || security.Confidence != project.FindingConfidenceSuggested {
		t.Fatalf("security issue = %+v", security)
	}
	// Triage holds across listings, and a changed file makes the issue stale.
	if err := s.UpdateFindingStatus(performance.ProjectRevision, performance.ID, project.FindingStatusDismissed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	findings, err = s.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.ID == performance.ID && (finding.Status != project.FindingStatusDismissed || finding.Freshness != project.FindingFreshnessStale) {
			t.Fatalf("after the change: %+v", finding)
		}
	}
}
