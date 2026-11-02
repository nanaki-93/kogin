package app

import (
	"context"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func (s *Service) inProgressFindingTargets(ctx context.Context, root, projectID string) []project.FindingTarget {
	raw := collectSessionFindingTargets(root, false)
	if trees, err := readGitWorktrees(ctx, root); err == nil {
		for _, tree := range trees {
			if !tree.Current && !tree.Prunable {
				if canonical, err := project.CanonicalRoot(tree.Path); err == nil {
					raw = append(raw, collectSessionFindingTargets(canonical, true)...)
				}
			}
		}
	}
	seen := make(map[project.FindingTarget]bool, len(raw))
	var targets []project.FindingTarget
	for _, target := range raw {
		if (projectID == "" || target.ProjectID == projectID) && !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

func collectSessionFindingTargets(root string, includeApplied bool) []project.FindingTarget {
	sessions, err := listChangeSessions(root)
	if err != nil {
		return nil
	}
	var targets []project.FindingTarget
	for _, entry := range sessions {
		if entry.State == "draft" || (includeApplied && entry.State == "applied") {
			targets = append(targets, entry.FindingTargets...)
			targets = append(targets, entry.DismissedTargets...) // Settled when the task merges.
		}
	}
	return targets
}

func (s *Service) syncSemanticFindingsLocked(root string) {
	analysis, index, input, err := s.workspaceState()
	if err != nil {
		return
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return
	}
	cache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		return
	}
	cache = cache.WithPolicy(policy)
	var models *AnalysisModels
	if s.analysisRun.run != nil && s.analysisRun.run.Identity.ProjectID == analysis.ProjectID {
		models = s.analysisRun.run.Plan.Models
	}
	runtime := s.analysisModelRuntime(AnalysisStageSemantic, models)
	reported := project.SuggestedFindingsForProject(analysis.Report)
	for i := range index.Files {
		file := &index.Files[i]
		if file.Binary {
			continue
		}
		cached, err := cache.Load(semanticCacheInputForRuntime(analysis, file, file.ContentHash, policy.Version(), runtime))
		if err == nil && cached != nil {
			reported = append(reported, project.SuggestedFindingsForFile(*cached)...)
		}
	}
	if store, err := project.NewFindingStore(root); err == nil {
		_, _ = store.ReconcileSource(input, project.FindingSourceAI, reported)
	}
}

func (s *Service) clearAnalysisResultsLocked(ctx context.Context, root string, preview *AnalysisRunPreview) error {
	targets := s.inProgressFindingTargets(ctx, root, preview.Identity.ProjectID)
	if len(targets) > 0 {
		s.syncSemanticFindingsLocked(root)
	}
	fileCache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		return err
	}
	if err := fileCache.Clear(); err != nil {
		return err
	}
	if err := project.ClearPerformanceFileReports(root); err != nil {
		return err
	}
	securityCache, err := project.NewSecurityReportCache(root)
	if err != nil {
		return err
	}
	if err := securityCache.Clear(); err != nil {
		return err
	}
	findingStore, err := project.NewFindingStore(root)
	if err != nil {
		return err
	}
	if err := findingStore.ClearExcept(targets); err != nil {
		return err
	}
	if err := s.manager.ResetFileAnalysisStatuses(preview.Identity.ProjectID, preview.Identity.ProjectRevision); err != nil {
		return err
	}
	s.notifyEvent("findings")
	return nil
}
