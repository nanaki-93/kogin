package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// Match the daemon’s supported project inventory ceiling; batches remain independent.
const maxAnalysisInventoryFiles = 20000

var analysisStages = [...]AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityRules, AnalysisStageSecurityAI}

func analysisFingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode analysis identity: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (s *Service) analysisProviders(includeFeatures bool, models *AnalysisModels) ([]AnalysisProviderRequirement, error) {
	providers := make([]AnalysisProviderRequirement, 0, 4)
	type providerEntry struct {
		runtime modelRuntime
		stages  []AnalysisStage
		prompts []string
	}
	entries := []providerEntry{
		{s.analysisModelRuntime(AnalysisStageSemantic, models), []AnalysisStage{AnalysisStageSemantic}, []string{semanticAnalysisPromptVersion}},
		{s.analysisModelRuntime(AnalysisStagePerformance, models), []AnalysisStage{AnalysisStagePerformance}, []string{project.PerformancePromptVersion}},
	}
	stages := []AnalysisStage{AnalysisStageSecurityAI}
	if includeFeatures {
		stages = append(stages, AnalysisStageFeatures)
	}
	for _, stage := range stages {
		runtime := s.analysisModelRuntime(stage, models)
		prompt := project.SecurityPromptVersion
		if stage == AnalysisStageFeatures {
			prompt = featureSuggestionsPromptVersion
		}
		// Keep the legacy grouping and fingerprint when review operations share a model.
		matched := false
		for i := 1; i < len(entries); i++ {
			if runtime.effective == entries[i].runtime.effective {
				entries[i].stages = append(entries[i].stages, stage)
				entries[i].prompts = append(entries[i].prompts, prompt)
				matched = true
				break
			}
		}
		if !matched {
			entries = append(entries, providerEntry{runtime, []AnalysisStage{stage}, []string{prompt}})
		}
	}
	for _, entry := range entries {
		id, err := analysisFingerprint(struct {
			Model      EffectiveModel
			Endpoint   string
			Configured bool
			Prompts    []string
			CLIPath    string `json:",omitempty"`
		}{entry.runtime.effective, entry.runtime.profile.APIBaseURL, entry.runtime.client != nil, entry.prompts, entry.runtime.profile.CLIPath})
		if err != nil {
			return nil, err
		}
		providers = append(providers, AnalysisProviderRequirement{ID: id, Stages: entry.stages, Model: entry.runtime.effective, RemoteConfirmationRequired: entry.runtime.effective.RemoteProvider})
	}
	return providers, nil
}

// Preview is read-only with respect to execution: no requests or passive scans run.
// BatchFiles limits a window; it never truncates the captured inventory.
func (s *Service) PreviewAnalysisRun(ctx context.Context, request AnalysisPreviewRequest) (*AnalysisRunPreview, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateAnalysisModelSelections(ctx, request.Models, request.ResumeRun); err != nil {
		return nil, err
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	c := s.analysisRun
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := s.restoreAnalysisRunLocked(); err != nil && (c.run == nil || c.fault == nil) {
		return nil, err
	}
	return s.analysisPreviewLocked(ctx, request)
}

func (s *Service) analysisPreviewLocked(ctx context.Context, request AnalysisPreviewRequest) (*AnalysisRunPreview, error) {
	if request.ResumeRun != nil {
		if err := captureAnalysisResumeOptions(&request, s.analysisRun.run); err != nil {
			return nil, err
		}
	}
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		return nil, err
	}
	if analysis.ProjectID != request.ProjectID || analysis.ProjectRevision != request.ProjectRevision {
		return nil, project.ErrRevisionConflict
	}
	providers, err := s.analysisProviders(false, request.Models)
	if err != nil {
		return nil, err
	}
	fingerprint, err := analysisFingerprint(providers)
	if err != nil {
		return nil, err
	}
	preview := &AnalysisRunPreview{Models: cloneAnalysisModels(request.Models), RetryStaleFailed: request.RetryStaleFailed, StaleOnly: request.StaleOnly, StalePath: request.StalePath, RecoverIncomplete: request.RecoverIncomplete, ClearResults: request.ClearResults, SchemaVersion: AnalysisRunSchemaVersion, Scope: AnalysisRunScopeProject, Refresh: request.Refresh, Limits: request.Limits,
		Identity: AnalysisQueueIdentity{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, PolicyFingerprint: policy.Version(), ProviderFingerprint: fingerprint},
		Files:    []AnalysisPlannedFile{}, Excluded: []AnalysisExcludedFile{}, Providers: providers}
	root := s.manager.Root()
	if err := s.planAnalysisFiles(ctx, root, *analysis, index.Files, policy, request, preview); err != nil {
		return nil, err
	}
	s.planAnalysisDocs(index, preview)
	return s.completeAnalysisPreviewLocked(ctx, root, preview, request)
}

func captureAnalysisResumeOptions(request *AnalysisPreviewRequest, run *AnalysisRun) error {
	if run == nil || run.Identity != *request.ResumeRun || run.Plan.RetryStaleFailed != request.RetryStaleFailed {
		return project.ErrRevisionConflict
	}
	// Existing clients omit the option on resume, so the captured plan decides it;
	// a request cannot turn a different run into a recovery run.
	if request.RecoverIncomplete && !run.Plan.RecoverIncomplete {
		return project.ErrRevisionConflict
	}
	request.RecoverIncomplete = run.Plan.RecoverIncomplete
	if request.ClearResults && !run.Plan.ClearResults {
		return project.ErrRevisionConflict
	}
	request.ClearResults = run.Plan.ClearResults
	if request.StaleOnly && !run.Plan.StaleOnly {
		return project.ErrRevisionConflict
	}
	request.StaleOnly = run.Plan.StaleOnly
	if request.StalePath != "" && request.StalePath != run.Plan.StalePath {
		return project.ErrRevisionConflict
	}
	request.StalePath = run.Plan.StalePath
	if run.Plan.Features != nil {
		return errAnalysisFeaturesRetired
	}
	request.Models = cloneAnalysisModels(run.Plan.Models)

	return nil
}

func (s *Service) completeAnalysisPreviewLocked(ctx context.Context, root string, preview *AnalysisRunPreview, request AnalysisPreviewRequest) (*AnalysisRunPreview, error) {
	var err error
	if mode := analysisSelectionMode(request.RetryStaleFailed, request.RecoverIncomplete, request.StaleOnly); mode != analysisSelectAll {
		if err := s.scopeAnalysisRetryPreview(ctx, mode, preview, request.ResumeRun); err != nil {
			return nil, err
		}
	}
	sort.Slice(preview.Excluded, func(i, j int) bool { return preview.Excluded[i].Path < preview.Excluded[j].Path })
	preview.Identity.QueueID, err = analysisQueueFingerprint(preview)
	if err != nil {
		return nil, err
	}
	if request.ResumeRun != nil {
		if err := s.applyAnalysisResumeBudget(preview, request); err != nil {
			return nil, err
		}
	}
	countAnalysisPreviewRequests(preview)
	preview.PreviewID, err = analysisFingerprint(struct {
		Preview *AnalysisRunPreview
		Resume  *AnalysisRunIdentity
	}{preview, request.ResumeRun})
	if err != nil {
		return nil, err
	}
	if err := s.validateAnalysisQueue(ctx, root, preview, false); err != nil {
		return nil, err
	}
	return preview, nil
}

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

// The run's own report writes cannot alter its immutable queue identity.
func analysisQueueFingerprint(preview *AnalysisRunPreview) (string, error) {
	stable := cloneAnalysisPreview(*preview)
	stable.Identity.QueueID = ""
	stable.PreviewID = ""
	stable.ExpectedModelRequests = 0
	stable.MaxModelRequests = 0
	stable.SecurityReviewIntentRequired = false
	// The docs plan follows from the revision and providers already in the
	// identity; leaving it out keeps runs saved before it resumable.
	stable.Docs = nil
	if stable.Features != nil {
		stable.Features.ExpectedHash = ""
		stable.Features.MaxModelRequests = 0
	}
	for i := range stable.Files {
		for j := range stable.Files[i].Stages {
			stable.Files[i].Stages[j].Cached = false
			stable.Files[i].Stages[j].MaxModelRequests = 0
		}
	}
	return analysisFingerprint(stable)
}

func (s *Service) planAnalysisFiles(ctx context.Context, root string, analysis project.Analysis, indexedFiles []project.IndexFile, policy *project.ContextPolicy, request AnalysisPreviewRequest, preview *AnalysisRunPreview) error {
	excludedPaths, err := loadAnalysisSelection(root)
	if err != nil {
		return err
	}
	ignored := make(map[string]bool, len(excludedPaths))
	for _, path := range excludedPaths {
		ignored[path] = true
	}
	files := append([]project.IndexFile(nil), indexedFiles...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if err := collectAnalysisInventoryExclusions(ctx, root, files, policy, preview); err != nil {
		return err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		reason := analysisFileExclusion(file, policy)
		if reason == "" && ignored[file.Path] {
			reason = analysisSelectionExclusion
		}
		if reason == "Excluded by source policy." {
			continue
		}
		if reason != "" {
			preview.Excluded = append(preview.Excluded, AnalysisExcludedFile{Path: file.Path, Reason: reason})
			continue
		}
		info, err := project.GetFileInfo(root, file.Path)
		if err != nil {
			return err
		}
		if info.ContentHash != file.ContentHash || info.Binary {
			return project.ErrRevisionConflict
		}
		planned := AnalysisPlannedFile{AnalysisFileIdentity: AnalysisFileIdentity{Path: file.Path, ContentHash: file.ContentHash, Language: file.Language}, SizeBytes: file.SizeBytes, Stages: []AnalysisStagePlan{}}
		for _, stage := range analysisStages {
			plan, err := s.planAnalysisStage(analysis, file, policy, stage, request, preview.Providers)
			if err != nil {
				return err
			}
			planned.Stages = append(planned.Stages, plan)
		}
		preview.Files = append(preview.Files, planned)
	}
	return nil
}

func collectAnalysisInventoryExclusions(ctx context.Context, root string, files []project.IndexFile, policy *project.ContextPolicy, preview *AnalysisRunPreview) error {
	paths, err := project.WalkProjectFiles(ctx, project.ProjectWalkOptions{Root: root, IncludeSymlinkFiles: true, MaxFiles: maxAnalysisInventoryFiles})
	if err != nil {
		return err
	}
	indexed := make(map[string]bool, len(files))
	for _, file := range files {
		indexed[file.Path] = true
	}
	for _, path := range paths {
		if !policy.Decide(path).Include {
			preview.Excluded = append(preview.Excluded, AnalysisExcludedFile{Path: path, Reason: "Excluded by source policy."})
		} else if !indexed[path] {
			return project.ErrRevisionConflict
		}
	}
	return nil
}

func analysisFileExclusion(file project.IndexFile, policy *project.ContextPolicy) string {
	reason := ""
	switch {
	case !policy.Decide(file.Path).Include:
		reason = "Excluded by source policy."
	case file.Binary || file.Language == "":
		reason = "Not a supported text source file."
	case file.SizeBytes > maxSemanticAnalysisBytes && file.SizeBytes > project.PerformanceMaxSourceBytes && file.SizeBytes > project.SecurityMaxSourceBytes:
		reason = "Source exceeds analyzer size limits."
	}
	return reason
}

func analysisStageExclusion(file project.IndexFile, stage AnalysisStage) string {
	switch {
	case stage == AnalysisStageSemantic && (!isSemanticAnalysisCandidate(file) || file.SizeBytes > maxSemanticAnalysisBytes):
		return "Not eligible for semantic source analysis."
	case stage == AnalysisStagePerformance && file.SizeBytes > project.PerformanceMaxSourceBytes:
		return "Source exceeds analyzer size limits."
	case (stage == AnalysisStageSecurityRules || stage == AnalysisStageSecurityAI) && file.SizeBytes > project.SecurityMaxSourceBytes:
		return "Source exceeds analyzer size limits."
	case stage == AnalysisStageSecurityRules && file.Language != "Go":
		return "Passive security rules require a Go source file."
	}
	return ""
}

func (s *Service) planAnalysisStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, request AnalysisPreviewRequest, providers []AnalysisProviderRequirement) (AnalysisStagePlan, error) {
	var err error
	reason := analysisStageExclusion(file, stage)
	plan := AnalysisStagePlan{Stage: stage, Eligible: reason == "", Reason: reason}
	if stage != AnalysisStageSecurityRules {
		provider := providers[1]
		runtime := s.analysisModelRuntime(stage, request.Models)
		if stage == AnalysisStageSemantic {
			provider = providers[0]
		}
		plan.ProviderID = provider.ID
		if plan.Eligible && runtime.client == nil {
			plan.Reason = "The model for this stage is not configured."
		}
		plan.MaxModelRequests = min(request.Limits.MaxAttemptsPerStage, runtime.effective.MaxRetries+1)
	}
	if plan.Eligible {
		plan.Cached, _, _, err = s.analysisStageCacheState(analysis, file, policy, stage, request.Models)
		if err != nil {
			return AnalysisStagePlan{}, err
		}
		if request.Refresh && (stage != AnalysisStageSecurityRules || request.ClearResults) {
			plan.Cached = false
		}
	}
	if !plan.Eligible || plan.Cached || plan.Reason == "The model for this stage is not configured." {
		plan.MaxModelRequests = 0
	}
	return plan, nil
}

func (s *Service) applyAnalysisResumeBudget(preview *AnalysisRunPreview, request AnalysisPreviewRequest) error {
	run := s.analysisRun.run
	if run == nil || run.Identity != *request.ResumeRun || run.Identity.AnalysisQueueIdentity != preview.Identity || run.Plan.Limits != request.Limits || run.Plan.Refresh != request.Refresh {
		return project.ErrRevisionConflict
	}
	for i := range preview.Files {
		for j := range preview.Files[i].Stages {
			plan := &preview.Files[i].Stages[j]
			progress := run.Files[i].Stages[j]
			if !analysisStageNeedsWork(progress.Status) {
				plan.MaxModelRequests = 0
				continue
			}
			plan.MaxModelRequests = min(plan.MaxModelRequests, max(0, request.Limits.MaxAttemptsPerStage-progress.Attempts))
		}
	}
	if preview.Features != nil {
		progress := run.Features
		if !analysisStageNeedsWork(progress.Status) {
			preview.Features.MaxModelRequests = 0
		} else {
			preview.Features.MaxModelRequests = min(preview.Features.MaxModelRequests, max(0, request.Limits.MaxAttemptsPerStage-progress.Attempts))
		}
	}
	return nil
}

func countAnalysisPreviewRequests(preview *AnalysisRunPreview) {
	if preview.Features != nil && preview.Features.MaxModelRequests > 0 {
		preview.ExpectedModelRequests++
		preview.MaxModelRequests += preview.Features.MaxModelRequests
	}
	for _, file := range preview.Files {
		for _, stage := range file.Stages {
			if stage.MaxModelRequests > 0 {
				preview.ExpectedModelRequests++
				preview.MaxModelRequests += stage.MaxModelRequests
				if stage.Stage == AnalysisStageSecurityAI {
					preview.SecurityReviewIntentRequired = true
				}
			}
		}
	}
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
