package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func analysisModelsPreviewFor(t *testing.T, s *Service, models *AnalysisModels, limits AnalysisRunLimits) *AnalysisRunPreview {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{Models: models,
		ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestAnalysisModelsDispatchChosenProvidersAndRetainDefaults(t *testing.T) {
	analyze, analyzeCalls := analysisResponseServer(t, analysisReply)
	bug, bugCalls := analysisResponseServer(t, analysisReply)
	function, functionCalls := analysisResponseServer(t, analysisReply)
	base, _ := newSemanticAnalysisService(t, analyze.URL, 0)
	cfg := scopedTestConfig(analyze.URL)
	cfg.ModelScopes.Analyze.Model = "review-model"
	cfg.ModelScopes.Bug.APIBaseURL, cfg.ModelScopes.Bug.Model = bug.URL, "code-model"
	cfg.ModelScopes.Code.APIBaseURL, cfg.ModelScopes.Code.Model = function.URL, "function-model"
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	original := s.CurrentModelCatalog()
	models := &AnalysisModels{Code: "code", Performance: "bug", Security: "analyze"}
	preview := analysisModelsPreviewFor(t, s, models, AnalysisRunLimits{100, 30, 2})
	if analyzeCalls.Load()+bugCalls.Load()+functionCalls.Load() != 0 || len(preview.Providers) != 3 {
		t.Fatalf("model preview dispatched or lost a provider: %+v", preview)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	models.Code = "bug" // Caller changes cannot retarget the admitted run.
	run := completedAnalysisRun(t, s)
	if run.Status != AnalysisRunCompleted || functionCalls.Load() != 1 || bugCalls.Load() != 1 || analyzeCalls.Load() != 1 {
		t.Fatalf("chosen providers were not used: status=%s code=%d performance=%d security=%d", run.Status, functionCalls.Load(), bugCalls.Load(), analyzeCalls.Load())
	}
	if !reflect.DeepEqual(original, s.CurrentModelCatalog()) || run.Plan.Models.Code != "code" {
		t.Fatal("run choices mutated configured defaults or captured input")
	}
	selection, err := s.ReadAnalysisSelection(context.Background(), run.Identity.ProjectID, run.Identity.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range selection.Files {
		for _, stage := range file.Stages {
			if stage.Status != "fresh" {
				t.Fatalf("selected-model file evidence lost freshness: %s %+v", file.Path, stage)
			}
		}
	}
	saved := readSavedAnalysisResults(t, s, run)
	for _, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		count := 0
		for _, file := range saved {
			count += file.findings(category)
		}
		if count == 0 {
			t.Fatalf("chosen-model evidence missing for %s: %+v", category, saved)
		}
	}
	if performance := savedPerformanceReport(t, s, run, "main.go"); performance.Profile != "bug" || performance.Scope != "analyze" {
		t.Fatalf("review provenance lost chosen profile: %+v", performance)
	}
	if fileAnalysis, err := s.CachedFileAnalysis("main.go"); err != nil || fileAnalysis.Status != project.AnalysisStatusFresh || fileAnalysis.Profile != "code" {
		t.Fatalf("cached file analysis lost chosen Code model freshness: %+v, %v", fileAnalysis, err)
	}
	reopened, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	if overview, err := reopened.ProjectOverview(); err != nil || overview.Findings.AI == 0 || overview.Coverage.Fresh == 0 {
		t.Fatalf("reopened overview retired chosen Code model findings: %+v, %v", overview, err)
	}
	if findings, err := reopened.ListFindings(FindingFilter{Source: project.FindingSourceAI}); err != nil || len(findings) == 0 {
		t.Fatalf("reopened findings retired chosen Code model findings: %+v, %v", findings, err)
	}
	cached := analysisModelsPreviewFor(t, s, run.Plan.Models, preview.Limits)
	if cached.ExpectedModelRequests != 0 { // All file stages reuse evidence.
		t.Fatalf("selected model caches were not reused: %+v", cached)
	}
	defaults := analysisModelsPreviewFor(t, s, nil, preview.Limits)
	// Security AI already ran on its default Analyze model; Code and Performance did not.
	if defaults.ExpectedModelRequests != 2 || defaults.Identity.ProviderFingerprint == preview.Identity.ProviderFingerprint {
		t.Fatalf("default profiles reused another model's evidence: %+v", defaults)
	}
}

func TestAnalysisModelsUseSelectedTimeouts(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	s.conf().runtimes.analyze.effective.Timeout = "30m"
	s.conf().runtimes.function.effective.Timeout = "20m"
	s.conf().runtimes.bug.effective.Timeout = "1m"
	models := &AnalysisModels{Code: "analyze", Review: "code"}
	prepared, err := s.prepareFileAnalysis("main.go")
	if err != nil {
		t.Fatal(err)
	}
	prepared.runtime = s.analysisModelRuntime(AnalysisStageSemantic, models)
	stopped := errors.New("deadline observed before dispatch")
	attempts := 0
	dispatchFor := func(want time.Duration) *analysisModelDispatch {
		return &analysisModelDispatch{maxAttempts: 1, beforeAttempt: func(ctx context.Context) error {
			attempts++
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining > want || remaining < want-time.Second {
				t.Fatalf("selected model deadline=%s, want %s", remaining, want)
			}
			return stopped
		}}
	}
	if _, err := s.requestFileAnalysis(context.Background(), prepared, dispatchFor(30*time.Minute)); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	performance, err := s.preparePerformanceReview("main.go")
	if err != nil {
		t.Fatal(err)
	}
	performance.runtime = s.analysisModelRuntime(AnalysisStagePerformance, models)
	performance.modelProfile = models.Review
	if _, err := s.requestPerformanceReview(context.Background(), performance, dispatchFor(20*time.Minute)); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	security, err := s.prepareSecurityReview(SecurityReviewRequest{ProjectID: prepared.analysis.ProjectID,
		ProjectRevision: prepared.analysis.ProjectRevision, Path: "main.go", BaseFileHash: prepared.input.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	security.runtime = s.analysisModelRuntime(AnalysisStageSecurityAI, models)
	security.modelProfile = models.Review
	if _, _, err := s.executeSecurityReview(context.Background(), security, dispatchFor(20*time.Minute)); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	// Legacy standalone reviews retain the existing Analysis timeout.
	performance.runtime, performance.modelProfile = s.conf().runtimes.analyze, ""
	if _, err := s.requestPerformanceReview(context.Background(), performance, dispatchFor(s.conf().analysisTimeout)); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	if attempts != 4 || calls.Load() != 0 {
		t.Fatalf("deadline checks=%d provider calls=%d", attempts, calls.Load())
	}
}

func TestAnalysisModelsRejectInvalidRetargetingAndRequireActualProviderConsent(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	index, _ := s.manager.Index()
	request := AnalysisPreviewRequest{Models: &AnalysisModels{Code: "unknown", Review: "analyze"},
		ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{100, 30, 2}}
	if _, err := s.PreviewAnalysisRun(context.Background(), request); err == nil || calls.Load() != 0 {
		t.Fatalf("invalid profile was admitted: %v", err)
	}
	s.conf().runtimes.function.effective.RemoteProvider = true
	models := &AnalysisModels{Code: "bug", Review: "code"}
	preview := analysisModelsPreviewFor(t, s, models, request.Limits)
	start := analysisStartFor(preview)
	start.Models.Review = "analyze"
	if _, err := s.StartAnalysisRun(context.Background(), start); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("retargeted review model was admitted: %v", err)
	}
	start = analysisStartFor(preview)
	start.Confirmations.ProviderIDs = nil
	if _, err := s.StartAnalysisRun(context.Background(), start); err == nil || calls.Load() != 0 {
		t.Fatalf("selected remote provider lacked consent: %v", err)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	if run := completedAnalysisRun(t, s); run.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 {
		t.Fatalf("confirmed selected model failed: %+v calls=%d", run, calls.Load())
	}
}

func TestAnalysisModelsPersistAcrossResumeAndRejectChangedProvider(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	models := &AnalysisModels{Code: "code", Review: "bug"}
	preview := analysisModelsPreviewFor(t, s, models, AnalysisRunLimits{1, 30, 2})
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	restored, err := loadAnalysisRun(root)
	if err != nil || !reflect.DeepEqual(restored.Plan.Models, models) {
		t.Fatalf("model selections were not persisted: %+v, %v", restored, err)
	}
	resume := analysisRunPreviewFor(t, s, preview.Limits, &paused.Identity)
	if !reflect.DeepEqual(resume.Models, models) || resume.ExpectedModelRequests != 3 {
		t.Fatalf("resume lost selected models: %+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	if run := completedAnalysisRun(t, s); run.Status != AnalysisRunCompletedEmpty || calls.Load() != 6 {
		t.Fatalf("resumed selected models did not complete: %+v calls=%d", run, calls.Load())
	}
	latest := analysisModelsPreviewFor(t, s, models, AnalysisRunLimits{100, 30, 2})
	s.conf().runtimes.function = newModelRuntime(s.conf().runtimes.function.profile, s.conf().importTimeout, 4)
	s.conf().runtimes.function.effective.ReasoningEffort = "changed"
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(latest)); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 6 {
		t.Fatalf("changed selected provider was admitted: %v", err)
	}
	if runtime := s.analysisModelRuntime(AnalysisStageSemantic, nil); runtime.profile.Scope != config.BugModelScope {
		t.Fatal("legacy default scope changed")
	}
}

func TestAnalysisIndependentModelsRejectInvalidSelections(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	index, _ := s.manager.Index()
	for _, models := range []AnalysisModels{
		{Code: "bug", Performance: "code"},
		{Code: "bug", Performance: "invalid", Security: "analyze"},
		{Code: "bug", Performance: "code", Security: "configured:missing"},
	} {
		_, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{
			Models: &models, ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision,
			Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{100, 30, 2},
		})
		if err == nil || calls.Load() != 0 {
			t.Fatalf("invalid selection admitted: %+v, %v", models, err)
		}
	}
}
