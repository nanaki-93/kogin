package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nanaki-93/kasuri/v2/internal/project"
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
	s.planAnalysisChecks(ctx, root, analysis.ProjectRevision, preview)
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
	stable.Checks = nil
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
