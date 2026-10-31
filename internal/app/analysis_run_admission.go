package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) validateAnalysisQueue(ctx context.Context, root string, plan *AnalysisRunPreview, sources bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		return err
	}
	providers, err := s.analysisProviders(plan.Features != nil, plan.Models)
	if err != nil {
		return err
	}
	fingerprint, err := analysisFingerprint(providers)
	if err != nil {
		return err
	}
	if root != s.manager.Root() || plan.Identity.ProjectID != analysis.ProjectID || plan.Identity.ProjectRevision != analysis.ProjectRevision || plan.Identity.PolicyFingerprint != policy.Version() || plan.Identity.ProviderFingerprint != fingerprint {
		return project.ErrRevisionConflict
	}
	if sources {
		if err := validateAnalysisInventory(ctx, root, plan); err != nil {
			return err
		}
	}
	return validateAnalysisFiles(ctx, root, plan, index, policy, sources)
}

func validateAnalysisFiles(ctx context.Context, root string, plan *AnalysisRunPreview, index *project.ProjectIndex, policy *project.ContextPolicy, sources bool) error {
	indexed := make(map[string]project.IndexFile, len(index.Files))
	for _, file := range index.Files {
		indexed[file.Path] = file
	}
	var sourceFiles []project.IndexFile
	if sources {
		sourceFiles = make([]project.IndexFile, 0, len(plan.Files))
	}
	for _, file := range plan.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, ok := indexed[file.Path]
		if !ok || current.ContentHash != file.ContentHash || current.Language != file.Language || current.SizeBytes != file.SizeBytes || !policy.Decide(file.Path).Include {
			return project.ErrRevisionConflict
		}
		if sources {
			sourceFiles = append(sourceFiles, current)
		}
	}
	if sources {
		return project.VerifyIndexedSources(ctx, root, sourceFiles)
	}
	return nil
}

func validateAnalysisConfirmations(preview *AnalysisRunPreview, confirmations AnalysisRunConfirmations) error {
	if preview.SecurityReviewIntentRequired && !confirmations.SecurityReview {
		return fmt.Errorf("analysis requires fresh Security review intent")
	}
	confirmed := make(map[string]bool, len(confirmations.ProviderIDs))
	providers := make(map[string]AnalysisProviderRequirement, len(preview.Providers))
	for _, provider := range preview.Providers {
		providers[provider.ID] = provider
	}
	for _, id := range confirmations.ProviderIDs {
		if confirmed[id] {
			return fmt.Errorf("duplicate analysis provider confirmation")
		}
		confirmed[id] = true
		if _, known := providers[id]; !known {
			return fmt.Errorf("analysis provider confirmation does not match preview")
		}
	}
	for id := range analysisRequestedProviders(preview) {
		if providers[id].RemoteConfirmationRequired && !confirmed[id] {
			return fmt.Errorf("analysis requires confirmation for each remote provider in the preview")
		}
	}
	return nil
}

func analysisRequestedProviders(preview *AnalysisRunPreview) map[string]bool {
	requested := make(map[string]bool)
	for _, file := range preview.Files {
		for _, stage := range file.Stages {
			if stage.MaxModelRequests > 0 {
				requested[stage.ProviderID] = true
			}
		}
	}
	if preview.Features != nil && preview.Features.MaxModelRequests > 0 {
		requested[preview.Features.ProviderID] = true
	}
	if preview.Docs != nil && preview.Docs.MaxModelRequests > 0 {
		requested[preview.Docs.ProviderID] = true
	}
	return requested
}

func validateAnalysisInventory(ctx context.Context, root string, plan *AnalysisRunPreview) error {
	paths, err := project.WalkProjectFiles(ctx, project.ProjectWalkOptions{Root: root, IncludeSymlinkFiles: true, MaxFiles: maxAnalysisInventoryFiles})
	if err != nil {
		return err
	}
	captured := make(map[string]bool, len(plan.Files)+len(plan.Excluded))
	for _, file := range plan.Files {
		captured[file.Path] = true
	}
	for _, file := range plan.Excluded {
		captured[file.Path] = true
	}
	if len(paths) != len(captured) {
		return project.ErrRevisionConflict
	}
	for _, path := range paths {
		if !captured[path] {
			return project.ErrRevisionConflict
		}
	}
	return nil
}
