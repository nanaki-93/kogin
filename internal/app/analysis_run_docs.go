package app

import (
	"context"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// AnalysisDocsPlan is a run's last step: the docs' component summaries,
// rolled up from the files' summaries, and the project overview, written with
// the analyze model. Only components whose files' summaries changed are
// summarized again, so the requests are a bound, not an estimate.
type AnalysisDocsPlan struct {
	ProviderID       string `json:"provider_id"`
	Components       int    `json:"components"`
	MaxModelRequests int    `json:"max_model_requests"`
	Reason           string `json:"reason,omitempty"`
}

// maxDocsPlanComponents bounds a stored plan: the docs show at most 16
// components.
const maxDocsPlanComponents = 16

// planAnalysisDocs plans the docs refresh with the model of the analyze
// stages: a summary per component and the overview, each with its retries.
// A run without files, such as feature discovery alone, has none.
func (s *Service) planAnalysisDocs(index *project.ProjectIndex, preview *AnalysisRunPreview) {
	if len(preview.Files) == 0 {
		return
	}
	runtime := s.analysisModelRuntime(AnalysisStagePerformance, preview.Models)
	components, _ := project.DocsComponents(s.projectMap(index))
	plan := &AnalysisDocsPlan{Components: len(components), MaxModelRequests: (len(components) + 1) * (runtime.effective.MaxRetries + 1)}
	for _, provider := range preview.Providers {
		for _, stage := range provider.Stages {
			if stage == AnalysisStagePerformance {
				plan.ProviderID = provider.ID
			}
		}
	}
	if runtime.client == nil {
		plan.MaxModelRequests, plan.Reason = 0, "The model for this stage is not configured."
	}
	preview.Docs = plan
}

func validateStoredAnalysisDocs(run *AnalysisRun) error {
	if plan := run.Plan.Docs; plan != nil {
		if len(plan.ProviderID) != 64 || plan.Components < 0 || plan.Components > maxDocsPlanComponents || plan.MaxModelRequests < 0 || !analysisMetadataReason(plan.Reason) {
			return errAnalysisRunCorrupt
		}
	}
	if docs := run.Docs; docs != nil {
		switch docs.Status {
		case "completed", "skipped", "failed":
		default:
			return errAnalysisRunCorrupt
		}
		if run.Plan.Docs == nil || docs.Components < 0 || docs.Summarized < 0 || docs.Kept < 0 || docs.Summarized+docs.Kept > docs.Components || !analysisMetadataReason(docs.Reason) {
			return errAnalysisRunCorrupt
		}
	}
	return nil
}

// refreshDocsAfterRun refreshes the docs at the end of a run that went
// through its files, as its plan allows.
func (s *Service) refreshDocsAfterRun(ctx context.Context, identity AnalysisRunIdentity) {
	c := s.analysisRun
	c.mu.Lock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil || c.run.Status != AnalysisRunRunning || ctx.Err() != nil || c.fileStopReason != "" || c.run.Plan.Docs == nil {
		c.mu.Unlock()
		return
	}
	plan := *c.run.Plan.Docs
	models := cloneAnalysisModels(c.run.Plan.Models)
	confirmed := analysisProviderConfirmed(c.confirmations, plan.ProviderID)
	c.mu.Unlock()
	runtime := s.analysisModelRuntime(AnalysisStagePerformance, models)
	refresh := DocsRefresh{Status: "skipped", Overview: "skipped", At: time.Now().UTC(), Reason: plan.Reason}
	switch {
	case plan.MaxModelRequests == 0 || runtime.client == nil:
		refresh.Reason = "The model for this stage is not configured."
	case runtime.effective.RemoteProvider && !confirmed:
		refresh.Reason = docsRefreshUnconfirmedReason
	default:
		refresh, _ = s.refreshDocs(ctx, runtime, false)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil && c.run.Identity == identity {
		c.run.Docs = &refresh
	}
}
