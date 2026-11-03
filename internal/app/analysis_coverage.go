package app

import (
	"context"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// The overview uses the same current evidence and exclusions as the file checklist.
// The caller holds jobLifecycleMu, keeping the project stable throughout the read.
func (s *Service) buildAnalysisCoverageSummary(id, revision string) (AnalysisCoverage, error) {
	s.analysisRun.mu.Lock()
	defer s.analysisRun.mu.Unlock()
	selection, err := s.analysisSelectionLocked(context.Background(), id, revision)
	if err != nil {
		return AnalysisCoverage{}, err
	}
	excluded := make(map[string]bool, len(selection.ExcludedPaths))
	for _, path := range selection.ExcludedPaths {
		excluded[path] = true
	}
	coverage := AnalysisCoverage{}
	for _, file := range selection.Files {
		if file.Reason != "" || excluded[file.Path] {
			continue
		}
		status := analysisFileCoverageStatus(file.Stages)
		if status == "excluded" {
			continue
		}
		coverage.Total++
		switch status {
		case "fresh":
			coverage.Fresh++
		case "stale":
			coverage.Stale++
		case "failed":
			coverage.Failed++
		case "running":
			coverage.Running++
		case "partial":
			coverage.Partial++
		case "unavailable":
			coverage.Unavailable++
		default:
			coverage.Missing++
		}
	}
	return coverage, nil
}

func analysisFileCoverageStatus(stages []AnalysisFileStageStatus) string {
	statuses := map[string]bool{}
	for _, stage := range stages {
		if stage.Status != "skipped" {
			statuses[stage.Status] = true
		}
	}
	if len(stages) == 0 {
		return "unavailable"
	}
	if len(statuses) == 0 {
		return "excluded"
	}
	for _, disposition := range []struct{ stage, coverage string }{
		{"running", "running"}, {"failed", "failed"}, {"stale", "stale"},
		{"unavailable", "unavailable"}, {"interrupted", "partial"},
		{"canceled", "partial"}, {"paused", "partial"}, {"pending", "running"}, {"partial", "partial"},
	} {
		if statuses[disposition.stage] {
			return disposition.coverage
		}
	}
	if statuses["fresh"] && len(statuses) == 1 {
		return "fresh"
	}
	if statuses["missing"] && len(statuses) == 1 {
		return "missing"
	}
	if statuses["fresh"] && statuses["missing"] && len(statuses) == 2 {
		return "partial"
	}
	return "unavailable"
}
func (s *Service) analysisInputs() (*project.Analysis, *project.ProjectIndex, *project.ContextPolicy, error) {
	analysis, err := s.manager.Analysis()
	if err != nil {
		return nil, nil, nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, nil, nil, err
	}
	if index.ProjectID != analysis.ProjectID || index.ProjectRevision != analysis.ProjectRevision {
		return nil, nil, nil, project.ErrRevisionConflict
	}
	policy, err := project.NewContextPolicy(s.manager.Root())
	if err != nil {
		return nil, nil, nil, err
	}
	return analysis, index, policy, nil
}
