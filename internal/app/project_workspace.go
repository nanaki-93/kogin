package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// ProjectOverview is the source-free contract shared by project workspaces.
// It combines deterministic facts with optional model interpretation so it
// remains useful when no model analysis or scan has been performed.
type ProjectOverview struct {
	Run             *AnalysisRun                  `json:"analysis_run,omitempty"`
	Docs            *project.ProjectDocsReport    `json:"docs,omitempty"`
	ProjectID       string                        `json:"project_id"`
	ProjectRevision string                        `json:"project_revision"`
	Metrics         ProjectMetrics                `json:"metrics"`
	Analysis        project.ProjectAnalysisReport `json:"analysis"`
	Coverage        AnalysisCoverage              `json:"analysis_coverage"`
	Findings        FindingCounts                 `json:"finding_counts"`
}

type ProjectMetrics struct {
	Type            string         `json:"type"`
	BuildFile       string         `json:"build_file,omitempty"`
	FileCount       int            `json:"file_count"`
	SourceFileCount int            `json:"source_file_count"`
	TotalLines      int            `json:"total_lines"`
	Languages       map[string]int `json:"languages"`
}

type AnalysisCoverage struct {
	Total       int `json:"total"`
	Fresh       int `json:"fresh"`
	Stale       int `json:"stale"`
	Missing     int `json:"missing"`
	Failed      int `json:"failed"`
	Running     int `json:"running"`
	Partial     int `json:"partial"`
	Unavailable int `json:"unavailable"`
}

type FindingCounts struct {
	Verified int `json:"verified"`
	AI       int `json:"ai_suggestions"`
}

// FindingFilter limits returned source-free findings to an explicit lifecycle
// slice. Empty fields leave that dimension unfiltered.
type FindingFilter struct {
	Source     string
	Confidence string
	Severity   string
	Status     string
	Freshness  string
}

// ProjectOverview returns current deterministic metrics even when all optional
// interpretation and verification data is missing.
func (s *Service) ProjectOverview() (*ProjectOverview, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	// Reading a workspace restores progress without starting requests. A retained
	// persistence fault is visible in the run's status/reason; deterministic facts
	// remain available while the user recovers through explicit run controls,
	// or upgrades to read progress a newer Mini-Orca saved.
	run, runErr := s.currentAnalysisRunLocked(context.Background())
	if runErr != nil && !errors.Is(runErr, errAnalysisRunPersistence) && !errors.Is(runErr, ErrNewerAnalysisRun) {
		return nil, runErr
	}
	analysis, index, input, err := s.workspaceState()
	if err != nil {
		return nil, err
	}
	coverage, err := s.buildAnalysisCoverageSummary(analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		return nil, err
	}
	findings, err := s.listFindings(analysis, index, input, FindingFilter{})
	if err != nil {
		return nil, err
	}
	counts := FindingCounts{}
	for _, finding := range findings {
		switch {
		case finding.Source == project.FindingSourceAI && finding.Confidence == project.FindingConfidenceSuggested:
			counts.AI++
		case finding.Source != project.FindingSourceAI && finding.Confidence == project.FindingConfidenceToolReported:
			counts.Verified++
		}
	}
	docs, err := s.projectDocs(analysis, index)
	if err != nil {
		return nil, err
	}
	return &ProjectOverview{
		Run:       run,
		Docs:      docs,
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Metrics:  ProjectMetrics{Type: analysis.Type, BuildFile: analysis.BuildFile, FileCount: analysis.FileCount, SourceFileCount: analysis.SourceFileCount, TotalLines: analysis.TotalLines, Languages: cloneLanguageCounts(analysis.Languages)},
		Analysis: analysis.Report, Coverage: coverage, Findings: counts,
	}, nil
}

// projectDocs builds the docs from the current files on every read, so they
// follow the code; only the model-written parts can be stale.
func (s *Service) projectDocs(analysis *project.Analysis, index *project.ProjectIndex) (*project.ProjectDocsReport, error) {
	if index == nil {
		return nil, fmt.Errorf("project docs need the project index")
	}
	docs := project.BuildProjectDocs(project.DocsInputs{
		Analysis: *analysis,
		Map:      s.projectMap(index),
		Profiles: s.projectProfiles(s.manager.Root()),
	})
	return &docs, nil
}

// ListFindings returns sanitized persisted tool reports and fresh cached AI
// suggestions. AI suggestions are refreshed independently of explicit scans.
func (s *Service) ListFindings(filter FindingFilter) ([]project.UnifiedFinding, error) {
	analysis, index, input, err := s.workspaceState()
	if err != nil {
		return nil, err
	}
	return s.listFindings(analysis, index, input, filter)
}

// UpdateFindingStatus records an explicit triage decision only for the active
// project revision.
func (s *Service) UpdateFindingStatus(revision, id, status string) error {
	return s.UpdateFindingTargetStatus(revision, project.FindingTarget{ID: id}, status)
}

func (s *Service) UpdateFindingTargetStatus(revision string, target project.FindingTarget, status string) error {
	_, _, input, err := s.workspaceState()
	if err != nil {
		return err
	}
	if revision == "" || revision != input.ProjectRevision {
		return project.ErrRevisionConflict
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		return err
	}
	if target.Kind == "" {
		return store.SetStatus(target.ID, status)
	}
	if target.ProjectID == "" {
		target.ProjectID = input.ProjectID
	}
	if target.ProjectID != input.ProjectID {
		return project.ErrRevisionConflict
	}
	return store.SetTargetStatus(target, status, "manual")
}

func (s *Service) workspaceState() (*project.Analysis, *project.ProjectIndex, project.FindingInput, error) {
	analysis, err := s.manager.Analysis()
	if err != nil {
		return nil, nil, project.FindingInput{}, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, nil, project.FindingInput{}, err
	}
	if index.ProjectID != analysis.ProjectID || index.ProjectRevision != analysis.ProjectRevision {
		return nil, nil, project.FindingInput{}, project.ErrRevisionConflict
	}
	input := project.FindingInput{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, FileHashes: make(map[string]string, len(index.Files))}
	for _, file := range index.Files {
		input.FileHashes[file.Path] = file.ContentHash
	}
	return analysis, index, input, nil
}

func (s *Service) listFindings(analysis *project.Analysis, index *project.ProjectIndex, input project.FindingInput, filter FindingFilter) ([]project.UnifiedFinding, error) {
	if err := validateFindingFilter(filter); err != nil {
		return nil, err
	}
	if err := s.refreshAISuggestions(analysis, index, input); err != nil {
		return nil, err
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		return nil, err
	}
	findings, err := store.Load(input)
	if err != nil {
		return nil, err
	}
	result := make([]project.UnifiedFinding, 0, len(findings))
	for _, finding := range findings {
		if matchesFindingFilter(finding, filter) {
			result = append(result, finding)
		}
	}
	return result, nil
}

func (s *Service) refreshAISuggestions(analysis *project.Analysis, index *project.ProjectIndex, input project.FindingInput) error {
	reported := project.SuggestedFindingsForProject(analysis.Report)
	policy, err := project.NewContextPolicy(s.manager.Root())
	if err != nil {
		return err
	}
	cache, err := project.NewFileAnalysisCache(s.manager.Root())
	if err != nil {
		return err
	}
	cache = cache.WithPolicy(policy)
	for i := range index.Files {
		file := &index.Files[i]
		if file.Binary {
			continue
		}
		cacheInput := s.semanticCacheInput(analysis, file, file.ContentHash, policy.Version())
		cached, err := cache.Load(cacheInput)
		if err != nil {
			return err
		}
		if err := s.syncFileAnalysisStatus(cacheInput, cached.Status); err != nil {
			return err
		}
		reported = append(reported, project.SuggestedFindingsForFile(*cached)...)
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		return err
	}
	_, err = store.ReconcileSource(input, project.FindingSourceAI, reported)
	return err
}

func validateFindingFilter(filter FindingFilter) error {
	for _, value := range []string{filter.Source, filter.Confidence, filter.Severity, filter.Status, filter.Freshness} {
		if len(value) > 64 {
			return fmt.Errorf("finding filter is invalid")
		}
	}
	return nil
}

func matchesFindingFilter(finding project.UnifiedFinding, filter FindingFilter) bool {
	return (filter.Source == "" || finding.Source == filter.Source) &&
		(filter.Confidence == "" || finding.Confidence == filter.Confidence) &&
		(filter.Severity == "" || finding.Severity == filter.Severity) &&
		(filter.Status == "" || finding.Status == filter.Status) &&
		(filter.Freshness == "" || finding.Freshness == filter.Freshness)
}

func cloneLanguageCounts(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for language, count := range source {
		result[language] = count
	}
	return result
}
