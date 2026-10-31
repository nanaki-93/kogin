package app

import (
	"context"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// currentTestAnalysisRun can restore metadata but never starts a worker or reuses consent.
// A recoverable persistence fault returns the retained progress alongside its error.
func (s *Service) currentTestAnalysisRun(ctx context.Context) (*AnalysisRun, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	return s.currentAnalysisRunLocked(ctx)
}

// scopedFiles are the files a scoped preview selects; the rest are retained.
func scopedFiles(preview *AnalysisRunPreview) []AnalysisPlannedFile {
	selected := []AnalysisPlannedFile{}
	for _, file := range preview.Files {
		if !file.Retained {
			selected = append(selected, file)
		}
	}
	return selected
}

func previewRetained(preview *AnalysisRunPreview, path string) bool {
	for _, file := range preview.Files {
		if file.Path == path {
			return file.Retained
		}
	}
	return false
}

func analysisRunStage(t *testing.T, run *AnalysisRun, path string, stage AnalysisStage) AnalysisStageProgress {
	t.Helper()
	for _, file := range run.Files {
		if file.Path != path {
			continue
		}
		for _, progress := range file.Stages {
			if progress.Stage == stage {
				return progress
			}
		}
	}
	t.Fatalf("%s %s is not in the run", path, stage)
	return AnalysisStageProgress{}
}

// excludeAnalysisFiles saves an analysis selection that leaves paths out.
func excludeAnalysisFiles(t *testing.T, s *Service, paths []string) {
	t.Helper()
	selection := readSelectionFor(t, s)
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{ProjectID: selection.ProjectID, ProjectRevision: selection.ProjectRevision,
		SelectionID: selection.SelectionID, ExcludedPaths: paths}); err != nil {
		t.Fatal(err)
	}
}

// semanticStageFor runs the analysis run's semantic stage for one file. It
// reuses a usable saved summary unless refresh is set.
func semanticStageFor(t *testing.T, s *Service, path string, refresh bool) (analysisFileStageResult, error) {
	t.Helper()
	request := analysisStageRequestFor(t, s, path, AnalysisStageSemantic)
	request.Refresh = refresh
	return s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
}

// savedFileResults is what the analysis stores hold for one file of a run,
// loaded for the file's current source: its semantic summary, its
// Performance report and the reports of both Security stages. Each store
// marks a report whose source, policy or model changed as stale.
type savedFileResults struct {
	Path        string
	Semantic    *project.FileAnalysis
	Performance *project.PerformanceFileReport
	Security    []*project.SecurityFileReport
}

// findings counts the saved findings of one category: the semantic risks of
// that category plus the findings of the category's own reports.
func (r savedFileResults) findings(category project.FindingCategory) int {
	count := 0
	if r.Semantic != nil {
		for _, risk := range r.Semantic.Risks {
			if risk.Category == category {
				count++
			}
		}
	}
	switch category {
	case project.FindingCategoryPerformance:
		if r.Performance != nil {
			count += len(r.Performance.Findings)
		}
	case project.FindingCategorySecurity:
		for _, report := range r.Security {
			count += len(report.Findings)
		}
	}
	return count
}

// readSavedAnalysisResults reads the saved stage reports of the files a run
// covers, for the models it was admitted with. It never dispatches a stage.
func readSavedAnalysisResults(t *testing.T, s *Service, run *AnalysisRun) []savedFileResults {
	t.Helper()
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		t.Fatal(err)
	}
	root := s.manager.Root()
	cache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	cache = cache.WithPolicy(policy)
	covered := map[string]bool{}
	for _, file := range run.Files {
		covered[file.Path] = true
	}
	results := []savedFileResults{}
	for _, file := range index.Files {
		if !covered[file.Path] {
			continue
		}
		info, err := project.GetFileInfo(root, file.Path)
		if err != nil {
			t.Fatal(err)
		}
		file.ContentHash = info.ContentHash
		result := savedFileResults{Path: file.Path}
		result.Semantic, err = cache.Load(semanticCacheInputForRuntime(analysis, &file, file.ContentHash, policy.Version(), s.analysisModelRuntime(AnalysisStageSemantic, run.Plan.Models)))
		if err != nil {
			t.Fatal(err)
		}
		result.Performance, err = project.LoadPerformanceFileReport(root, file.Path, file.ContentHash, policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []project.SecurityReportInput{
			securityRulesInput(securityRulesSnapshot{analysis: *analysis, file: file, policyVersion: policy.Version()}),
			analysisSecurityCacheInput(*analysis, file, s.analysisModelRuntime(AnalysisStageSecurityAI, run.Plan.Models), policy.Version()),
		} {
			report, err := s.loadSecurityFileReport(root, input)
			if err != nil {
				t.Fatal(err)
			}
			if report != nil {
				result.Security = append(result.Security, report)
			}
		}
		results = append(results, result)
	}
	return results
}

// assertSavedAnalysisCounts checks the saved Bugs, Performance and Security
// findings of the files a run covers.
func assertSavedAnalysisCounts(t *testing.T, s *Service, run *AnalysisRun, want ...int) {
	t.Helper()
	results := readSavedAnalysisResults(t, s, run)
	for i, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		count := 0
		for _, file := range results {
			count += file.findings(category)
		}
		if count != want[i] {
			t.Fatalf("%s saved count: %d, want %d", category, count, want[i])
		}
	}
}

// retainedAnalysisFiles lists the files a run keeps without analyzing them.
func retainedAnalysisFiles(run *AnalysisRun) []string {
	retained := []string{}
	for _, file := range run.Plan.Files {
		if file.Retained {
			retained = append(retained, file.Path)
		}
	}
	return retained
}
