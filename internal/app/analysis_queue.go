package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

type AnalysisSelectableFile struct {
	Path   string                    `json:"path"`
	Reason string                    `json:"reason"`
	Stages []AnalysisFileStageStatus `json:"stages"`
}

type AnalysisFileSelection struct {
	ProjectID       string                   `json:"project_id"`
	ProjectRevision string                   `json:"project_revision"`
	SelectionID     string                   `json:"selection_id"`
	ExcludedPaths   []string                 `json:"excluded_paths"`
	Files           []AnalysisSelectableFile `json:"files"`
	Editable        bool                     `json:"editable"`
	Recovery        AnalysisRecoverySummary  `json:"recovery"`
}

type AnalysisSelectionRequest struct {
	ProjectID       string   `json:"project_id"`
	ProjectRevision string   `json:"project_revision"`
	SelectionID     string   `json:"selection_id"`
	ExcludedPaths   []string `json:"excluded_paths"`
}

func (r AnalysisSelectionRequest) Validate() error {
	if r.ProjectID == "" || r.ProjectRevision == "" || r.SelectionID == "" || r.ExcludedPaths == nil {
		return fmt.Errorf("selection requires project identity, selection identity and excluded_paths")
	}
	return validateAnalysisExcludedPaths(r.ExcludedPaths)
}

func (s *Service) ReadAnalysisSelection(ctx context.Context, id, revision string) (*AnalysisFileSelection, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.analysisRun.mu.Lock()
	defer s.analysisRun.mu.Unlock()
	if err := s.restoreAnalysisRunLocked(); err != nil {
		return nil, err
	}
	return s.analysisSelectionLocked(ctx, id, revision)
}

// The caller holds lifecycle/run locks and has restored the current run.
func (s *Service) analysisSelectionLocked(ctx context.Context, id, revision string) (*AnalysisFileSelection, error) {
	selection, _, err := s.analysisSelectionWithRecoveryLocked(ctx, id, revision)
	return selection, err
}

// The returned recovery evidence lets a selection save summarize its new exclusions
// without observing every file again.
func (s *Service) analysisSelectionWithRecoveryLocked(ctx context.Context, id, revision string) (*AnalysisFileSelection, analysisRecoveryEvidence, error) {
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		return nil, analysisRecoveryEvidence{}, err
	}
	if analysis.ProjectID != id || analysis.ProjectRevision != revision {
		return nil, analysisRecoveryEvidence{}, project.ErrRevisionConflict
	}
	excluded, err := loadAnalysisSelection(s.manager.Root())
	if err != nil {
		return nil, analysisRecoveryEvidence{}, err
	}
	selectionID, err := analysisFingerprint(excluded)
	if err != nil {
		return nil, analysisRecoveryEvidence{}, err
	}
	result := &AnalysisFileSelection{ProjectID: id, ProjectRevision: revision, SelectionID: selectionID, ExcludedPaths: excluded, Files: []AnalysisSelectableFile{}, Editable: s.analysisSelectionEditableLocked()}
	paths, err := project.WalkProjectFiles(ctx, project.ProjectWalkOptions{Root: s.manager.Root(), IncludeSymlinkFiles: true, MaxFiles: maxAnalysisInventoryFiles})
	if err != nil {
		return nil, analysisRecoveryEvidence{}, err
	}
	indexed := make(map[string]project.IndexFile, len(index.Files))
	for _, file := range index.Files {
		indexed[file.Path] = file
	}
	evidence := selectionEvidence(s.analysisRun.run, *analysis)
	recovery := analysisRecoveryEvidence{projectID: analysis.ProjectID, models: evidence.models(), files: map[string]analysisFileRecovery{}}
	for _, path := range paths {
		decision := policy.Decide(path)
		reason := decision.Reason
		if decision.Include {
			file, ok := indexed[path]
			if !ok {
				reason = "Not indexed yet. Refresh project facts before selecting this file."
			} else {
				reason = analysisFileExclusion(file, policy)
			}
		}
		entry := AnalysisSelectableFile{Path: path, Reason: reason, Stages: []AnalysisFileStageStatus{}}
		if reason == "" {
			var sourceCurrent bool
			entry.Stages, sourceCurrent, err = s.analysisSelectionStages(ctx, *analysis, indexed[path], policy, evidence)
			if err != nil {
				return nil, analysisRecoveryEvidence{}, err
			}
			recovery.files[path] = s.classifyAnalysisFileRecovery(entry.Stages, sourceCurrent, recovery.models)
		}
		result.Files = append(result.Files, entry)
	}
	result.Recovery = s.analysisRecoverySummaryLocked(recovery, excluded)
	return result, recovery, nil
}

func (s *Service) analysisSelectionEditableLocked() bool {
	c := s.analysisRun
	if c.done != nil || c.fault != nil {
		return false
	}
	if c.run != nil {
		switch c.run.Status {
		case AnalysisRunRunning, AnalysisRunQueued, AnalysisRunPausing, AnalysisRunPaused, AnalysisRunInterrupted:
			return false
		}
	}
	return true
}

func (s *Service) SaveAnalysisSelection(ctx context.Context, request AnalysisSelectionRequest) (*AnalysisFileSelection, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.analysisRun.mu.Lock()
	defer s.analysisRun.mu.Unlock()
	if err := s.restoreAnalysisRunLocked(); err != nil {
		return nil, err
	}
	current, recovery, err := s.analysisSelectionWithRecoveryLocked(ctx, request.ProjectID, request.ProjectRevision)
	if err != nil {
		return nil, err
	}
	if !current.Editable {
		return nil, errAnalysisRunBusy
	}
	if current.SelectionID != request.SelectionID {
		return nil, project.ErrRevisionConflict
	}
	// Retain exclusions for temporarily absent files, but reject invented new paths.
	known := make(map[string]bool)
	for _, file := range current.Files {
		known[file.Path] = true
	}
	for _, path := range current.ExcludedPaths {
		known[path] = true
	}
	for _, path := range request.ExcludedPaths {
		if !known[path] {
			return nil, project.ErrRevisionConflict
		}
	}
	paths := append([]string{}, request.ExcludedPaths...)
	sort.Strings(paths)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := saveAnalysisSelection(s.manager.Root(), paths); err != nil {
		return nil, err
	}
	current.ExcludedPaths = paths
	current.Recovery = s.analysisRecoverySummaryLocked(recovery, paths)
	current.SelectionID, err = analysisFingerprint(paths)
	return current, err
}
