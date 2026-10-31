package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

var errAnalysisAttemptBudget = errors.New("analysis stage attempt budget is exhausted")
var errAnalysisModelUnavailable = errors.New("analysis model is not configured")

// A coordinator executes one stage at a time, retaining each result independently.
// RemainingAttempts is the unspent cumulative allowance, including transport retries.
type analysisFileStageRequest struct {
	Models                *AnalysisModels
	Run                   AnalysisRunIdentity
	File                  AnalysisFileIdentity
	Stage                 AnalysisStage
	Refresh               bool
	RemainingAttempts     int
	ConfirmRemoteProvider bool
	SecurityReview        bool
}

// All callbacks are required. The owner validates the complete run/queue identity;
// BeforeAttempt durably reserves work, and Publish holds ownership through write.
// Callbacks must run synchronously. Check is never called inside Publish's lock.
type analysisFileStageAuthority struct {
	Check         func(context.Context, AnalysisRunIdentity) error
	BeforeAttempt func(context.Context, AnalysisRunIdentity, AnalysisFileIdentity, AnalysisStage) error
	Publish       func(AnalysisRunIdentity, AnalysisFileIdentity, AnalysisStage, func() error) error
}

type analysisFileStageResult struct {
	Progress    AnalysisStageProgress
	Semantic    *project.FileAnalysis
	Performance *project.PerformanceFileReport
	Security    *project.SecurityFileReport
}

type analysisStageAuthorityError struct{ cause error }

func (err *analysisStageAuthorityError) Error() string {
	return "analysis stage authority: " + err.cause.Error()
}
func (err *analysisStageAuthorityError) Unwrap() error { return err.cause }

type analysisModelError struct{ cause error }

func (err *analysisModelError) Error() string { return err.cause.Error() }
func (err *analysisModelError) Unwrap() error { return err.cause }

type analysisModelDispatch struct {
	maxAttempts   int
	beforeAttempt func(context.Context) error
}

func (s *Service) requestAnalysisModel(ctx context.Context, runtime modelRuntime, messages []llm.ChatMessage, schema *llm.JSONSchema, dispatch *analysisModelDispatch) (modelOutput, error) {
	if dispatch == nil {
		return s.retryRequest(ctx, runtime, messages, schema)
	}
	if runtime.client == nil {
		return modelOutput{}, errAnalysisModelUnavailable
	}
	if dispatch.maxAttempts == 0 {
		return modelOutput{}, &analysisStageAuthorityError{errAnalysisAttemptBudget}
	}
	if runtime.effective.MaxRetries >= dispatch.maxAttempts {
		runtime.effective.MaxRetries = dispatch.maxAttempts - 1
	}
	return s.retryRequestAuthorized(ctx, runtime, messages, schema, dispatch.beforeAttempt)
}

// Reuse the same one-shot publication rule for standalone and coordinated reports.
func publishAnalysisReport(store func() error, authorize func(func() error) error) error {
	if authorize == nil {
		return store()
	}
	called, completed := false, false
	var writeErr error
	err := authorize(func() error {
		if called {
			writeErr = fmt.Errorf("analysis report publication was already attempted")
			return writeErr
		}
		called = true
		writeErr = store()
		completed = writeErr == nil
		return writeErr
	})
	if err != nil {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	if !completed {
		return fmt.Errorf("analysis report publication was not completed")
	}
	return nil
}

type analysisFileStageExecution struct {
	service   *Service
	request   analysisFileStageRequest
	authority analysisFileStageAuthority
	root      string
	analysis  project.Analysis
	file      project.IndexFile
	policy    *project.ContextPolicy
	runtime   modelRuntime
	models    [3]EffectiveModel
	attempts  int
}

// Provider/parser failures produce a failed stage and allow other stages to run.
// Identity, cancellation, reservation and persistence errors return an error so
// the coordinator stops dispatch rather than presenting missing work as success.
func (s *Service) analyzeFileStage(ctx context.Context, request analysisFileStageRequest, authority analysisFileStageAuthority) (analysisFileStageResult, error) {
	result := analysisFileStageResult{Progress: AnalysisStageProgress{Stage: request.Stage, Status: AnalysisStagePending}}
	if err := validateAnalysisStageRequest(request, authority); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		result.Progress.Status = AnalysisStageCanceled
		return result, err
	}
	if err := authority.Check(ctx, request.Run); err != nil {
		return result, err
	}
	execution, err := s.prepareAnalysisFileStage(request, authority)
	if err == nil {
		err = execution.check(ctx)
	}
	if err == nil {
		err = execution.execute(ctx, &result)
	}
	if execution != nil {
		result.Progress.Attempts = execution.attempts
		if err == nil {
			err = execution.check(ctx)
		}
	}
	if err != nil {
		return rejectedAnalysisStage(ctx, request.Stage, result.Progress.Attempts, err)
	}
	if result.Progress.FindingCount != nil {
		result.Progress.ReportID = analysisReportID(request.Run.ProjectID, request.Run.ProjectRevision, request.File.Path, request.File.ContentHash, request.Stage)
	}
	return result, nil
}

// analysisReportID names the report a stage's evidence came from.
func analysisReportID(projectID, revision, path, contentHash string, stage AnalysisStage) string {
	identity := strings.Join([]string{projectID, revision, path, contentHash, string(stage)}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func (s *Service) prepareAnalysisFileStage(request analysisFileStageRequest, authority analysisFileStageAuthority) (*analysisFileStageExecution, error) {
	root := s.manager.Root()
	analysis, err := s.manager.Analysis()
	if err != nil {
		return nil, err
	}
	if analysis.ProjectID != request.Run.ProjectID || analysis.ProjectRevision != request.Run.ProjectRevision {
		return nil, project.ErrRevisionConflict
	}
	file, err := s.manager.IndexedFile(request.File.Path)
	if err != nil {
		return nil, err
	}
	if file.Path != request.File.Path || file.ContentHash != request.File.ContentHash || file.Language != request.File.Language {
		return nil, project.ErrRevisionConflict
	}
	if file.Binary || file.SizeBytes > maxSemanticAnalysisBytes {
		return nil, project.ErrUnsupportedFile
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	if !policy.Decide(file.Path).Include {
		return nil, project.ErrExcludedFile
	}
	return &analysisFileStageExecution{service: s, request: request, authority: authority, root: root, analysis: *analysis, file: *file, policy: policy, runtime: s.analysisModelRuntime(request.Stage, request.Models), models: s.analysisFileModelIdentity(request.Models)}, nil
}

func (execution *analysisFileStageExecution) validateSnapshot(ctx context.Context) error {
	s := execution.service
	if s.analysisFileModelIdentity(execution.request.Models) != execution.models {
		return project.ErrRevisionConflict
	}
	return s.validateSourceFileSnapshot(ctx, execution.root, execution.analysis, execution.file, execution.policy.Version(), true)
}

func (execution *analysisFileStageExecution) validatePrepared(root string, analysis project.Analysis, file project.IndexFile, policyVersion string) error {
	if root != execution.root || analysis.ProjectID != execution.analysis.ProjectID || analysis.ProjectRevision != execution.analysis.ProjectRevision || file.Path != execution.file.Path || file.ContentHash != execution.file.ContentHash || file.Language != execution.file.Language || policyVersion != execution.policy.Version() {
		return project.ErrRevisionConflict
	}
	return nil
}

func (execution *analysisFileStageExecution) bindPreparedRuntime(root string, analysis project.Analysis, file project.IndexFile, policyVersion string, runtime *modelRuntime, modelProfile *string) error {
	if err := execution.validatePrepared(root, analysis, file, policyVersion); err != nil {
		return err
	}
	*runtime = execution.runtime
	*modelProfile = execution.request.Models.profile(execution.request.Stage)
	return nil
}

func (execution *analysisFileStageExecution) check(ctx context.Context) error {
	if err := execution.authority.Check(ctx, execution.request.Run); err != nil {
		return &analysisStageAuthorityError{err}
	}
	return execution.validateSnapshot(ctx)
}

func (execution *analysisFileStageExecution) consent() error {
	if execution.request.Stage == AnalysisStageSecurityRules {
		return nil
	}
	if execution.request.Stage == AnalysisStageSecurityAI && !execution.request.SecurityReview {
		return fmt.Errorf("security review requires explicit intent")
	}
	return requireModelRuntimeConfirmation(execution.runtime, execution.request.ConfirmRemoteProvider)
}

func (execution *analysisFileStageExecution) dispatch() *analysisModelDispatch {
	return &analysisModelDispatch{maxAttempts: execution.request.RemainingAttempts, beforeAttempt: func(ctx context.Context) error {
		if err := execution.check(ctx); err != nil {
			return &analysisStageAuthorityError{err}
		}
		if err := execution.consent(); err != nil {
			return &analysisStageAuthorityError{err}
		}
		if execution.attempts >= execution.request.RemainingAttempts {
			return &analysisStageAuthorityError{errAnalysisAttemptBudget}
		}
		if err := execution.authority.BeforeAttempt(ctx, execution.request.Run, execution.request.File, execution.request.Stage); err != nil {
			return &analysisStageAuthorityError{err}
		}
		execution.attempts++
		if err := execution.check(ctx); err != nil {
			return &analysisStageAuthorityError{err}
		}
		return nil
	}}
}

func (execution *analysisFileStageExecution) publication(ctx context.Context) func(func() error) error {
	return func(store func() error) error {
		err := execution.authority.Publish(execution.request.Run, execution.request.File, execution.request.Stage, func() error {
			if err := execution.validateSnapshot(ctx); err != nil {
				return err
			}
			if err := execution.consent(); err != nil {
				return err
			}
			return store()
		})
		if err != nil {
			return &analysisStageAuthorityError{err}
		}
		return nil
	}
}

func (execution *analysisFileStageExecution) execute(ctx context.Context, result *analysisFileStageResult) error {
	switch execution.request.Stage {
	case AnalysisStageSemantic:
		return execution.semantic(ctx, result)
	case AnalysisStagePerformance:
		return execution.performance(ctx, result)
	case AnalysisStageSecurityRules:
		return execution.securityRules(ctx, result)
	case AnalysisStageSecurityAI:
		return execution.securityAI(ctx, result)
	}
	return fmt.Errorf("unknown analysis stage")
}

func analysisStageEvidence(progress *AnalysisStageProgress, count int, partial, cached bool) {
	progress.FindingCount = &count
	progress.Cached = cached
	progress.Status = AnalysisStageCompleted
	if count == 0 {
		progress.Status = AnalysisStageCompletedEmpty
	}
	if partial {
		progress.Status = AnalysisStagePartial
		progress.Reason = "The report contains incomplete evidence; review its details."
	}
}

func analysisStageModelFailure(ctx context.Context, result *analysisFileStageResult, err error) error {
	var authorityErr *analysisStageAuthorityError
	if ctx.Err() != nil || errors.Is(err, project.ErrRevisionConflict) || errors.As(err, &authorityErr) {
		return err
	}
	if errors.Is(err, errAnalysisModelUnavailable) {
		result.Progress.Status = AnalysisStageUnavailable
		result.Progress.Reason = "The model for this stage is not configured."
		return nil
	}
	result.Progress.Status = AnalysisStageFailed
	result.Progress.Reason = modelFailureReason(err)
	var failure project.PerformanceReviewFailure
	if errors.As(err, &failure) && failure.Valid() {
		result.Progress.Reason = failure.Error()
	}
	if result.Progress.Stage == AnalysisStagePerformance && errors.Is(err, llm.ErrStructuredRequestRejected) {
		result.Progress.Reason = "The provider rejected the Performance response format."
	}
	return nil
}

func (execution *analysisFileStageExecution) semantic(ctx context.Context, result *analysisFileStageResult) error {
	s := execution.service
	prepared, err := s.prepareFileAnalysis(execution.file.Path)
	if err != nil {
		return err
	}
	if err := execution.validatePrepared(prepared.root, *prepared.analysis, *prepared.indexedFile, prepared.input.ContextPolicyVersion); err != nil {
		return err
	}
	prepared.runtime = execution.runtime
	prepared.input = semanticCacheInputForRuntime(prepared.analysis, prepared.indexedFile, prepared.input.ContentHash, prepared.input.ContextPolicyVersion, execution.runtime)
	if !execution.request.Refresh {
		cached, err := prepared.cache.Load(prepared.input)
		if err != nil {
			return err
		}
		if analysisSemanticCacheUsable(cached) {
			result.Semantic = cached
			analysisStageEvidence(&result.Progress, len(cached.Risks), false, true)
			return nil
		}
	}
	fresh, err := s.requestFileAnalysis(ctx, prepared, execution.dispatch())
	if err != nil {
		var modelErr *analysisModelError
		if errors.As(err, &modelErr) || errors.Is(err, context.DeadlineExceeded) {
			return analysisStageModelFailure(ctx, result, err)
		}
		return err
	}
	if err := publishAnalysisReport(func() error {
		if err := project.ValidateInstructions(prepared.root, prepared.indexedFile.Path, prepared.instructions.Fingerprint); err != nil {
			return err
		}
		if err := prepared.cache.Store(*fresh); err != nil {
			return err
		}
		return s.syncFileAnalysisStatus(prepared.input, fresh.Status)
	}, execution.publication(ctx)); err != nil {
		return err
	}
	result.Semantic = fresh
	analysisStageEvidence(&result.Progress, len(fresh.Risks), false, false)
	return nil
}

func (execution *analysisFileStageExecution) performance(ctx context.Context, result *analysisFileStageResult) error {
	s := execution.service
	snapshot, err := s.preparePerformanceReview(execution.file.Path)
	if err != nil {
		return err
	}
	if err := execution.bindPreparedRuntime(snapshot.root, snapshot.analysis, snapshot.file, snapshot.policyVersion, &snapshot.runtime, &snapshot.modelProfile); err != nil {
		return err
	}
	if !execution.request.Refresh {
		cached, err := project.LoadPerformanceFileReport(execution.root, execution.file.Path, execution.file.ContentHash, execution.policy)
		if err != nil {
			return err
		}
		if analysisPerformanceCacheUsable(cached, execution.analysis, snapshot.runtime) {
			result.Performance = cached
			analysisStageEvidence(&result.Progress, len(cached.Findings), cached.Warning != "", true)
			return nil
		}
	}
	output, err := s.requestPerformanceReview(ctx, snapshot, execution.dispatch())
	if err != nil {
		return analysisStageModelFailure(ctx, result, err)
	}
	findings, warning, err := project.ParsePerformanceFindings(output.Content, snapshot.file.Path, snapshot.source)
	if err != nil {
		return analysisStageModelFailure(ctx, result, err)
	}
	report, err := s.publishPerformanceReview(ctx, snapshot, output, findings, warning, execution.publication(ctx))
	if err != nil {
		return err
	}
	result.Performance = report
	analysisStageEvidence(&result.Progress, len(report.Findings), report.Warning != "", false)
	return nil
}

func (execution *analysisFileStageExecution) securityRules(ctx context.Context, result *analysisFileStageResult) error {
	s := execution.service
	snapshot, err := s.prepareSecurityRulesScan(execution.file.Path, execution.analysis.ProjectRevision)
	if err != nil {
		return err
	}
	if err := execution.validatePrepared(snapshot.root, snapshot.analysis, snapshot.file, snapshot.policyVersion); err != nil {
		return err
	}
	// Refresh bypasses model caches only; current deterministic evidence is reusable.
	cached, err := s.loadSecurityFileReport(execution.root, securityRulesInput(snapshot))
	if err != nil {
		return err
	}
	if securityStageCacheUsable(cached) {
		result.Security = cached
		analysisStageEvidence(&result.Progress, len(cached.Findings), cached.Status == project.SecurityStatusPartial, true)
		return nil
	}
	report, err := s.executeSecurityRulesScan(ctx, snapshot, execution.publication(ctx))
	if err != nil {
		return err
	}
	result.Security = report
	analysisStageEvidence(&result.Progress, len(report.Findings), report.Status == project.SecurityStatusPartial, false)
	return nil
}

func securityStageCacheUsable(report *project.SecurityFileReport) bool {
	return report != nil && (report.Status == project.SecurityStatusCompleted || report.Status == project.SecurityStatusCompletedEmpty || report.Status == project.SecurityStatusPartial)
}

func (execution *analysisFileStageExecution) securityAI(ctx context.Context, result *analysisFileStageResult) error {
	s := execution.service
	request := SecurityReviewRequest{ProjectID: execution.analysis.ProjectID, ProjectRevision: execution.analysis.ProjectRevision, Path: execution.file.Path, BaseFileHash: execution.file.ContentHash, ConfirmRemoteProvider: execution.request.ConfirmRemoteProvider}
	snapshot, err := s.prepareSecurityReview(request)
	if err != nil {
		return err
	}
	if err := execution.bindPreparedRuntime(snapshot.root, snapshot.analysis, snapshot.file, snapshot.policyVersion, &snapshot.runtime, &snapshot.modelProfile); err != nil {
		return err
	}
	if !execution.request.Refresh {
		input := analysisSecurityCacheInput(execution.analysis, execution.file, snapshot.runtime, execution.policy.Version())
		cached, err := s.loadSecurityFileReport(execution.root, input)
		if err != nil {
			return err
		}
		if securityStageCacheUsable(cached) {
			result.Security = cached
			analysisStageEvidence(&result.Progress, len(cached.Findings), cached.Status == project.SecurityStatusPartial, true)
			return nil
		}
	}
	if err := execution.consent(); err != nil {
		return &analysisStageAuthorityError{err}
	}
	output, findings, err := s.executeSecurityReview(ctx, snapshot, execution.dispatch())
	if err != nil {
		var modelErr *analysisModelError
		if errors.As(err, &modelErr) {
			return analysisStageModelFailure(ctx, result, err)
		}
		return err
	}
	report, err := s.publishSecurityReview(ctx, snapshot, output, findings, execution.publication(ctx))
	if err != nil {
		var responseErr *analysisModelError
		if errors.As(err, &responseErr) {
			return analysisStageModelFailure(ctx, result, err)
		}
		return err
	}
	result.Security = report
	analysisStageEvidence(&result.Progress, len(report.Findings), report.Status == project.SecurityStatusPartial, false)
	return nil
}

func analysisSemanticCacheUsable(report *project.FileAnalysis) bool {
	if report == nil || report.Status != project.AnalysisStatusFresh {
		return false
	}
	for _, risk := range report.Risks {
		if !risk.Category.Valid() {
			return false
		}
	}
	return true
}

func analysisPerformanceCacheUsable(report *project.PerformanceFileReport, analysis project.Analysis, runtime modelRuntime) bool {
	if report == nil {
		return false
	}
	configuredModel := report.ConfiguredModel
	if configuredModel == "" {
		// Older reports only stored the returned model. Reuse them only when
		// that name exactly matches the current selection.
		configuredModel = report.Model
	}
	model := runtime.effective
	return report.Status == "completed" && report.ProjectID == analysis.ProjectID && configuredModel == runtime.profile.Model && report.Profile == model.Profile && report.Scope == model.Scope && report.ProviderOrigin == model.ProviderOrigin && report.ReasoningEffort == model.ReasoningEffort
}

func analysisSecurityCacheInput(analysis project.Analysis, file project.IndexFile, runtime modelRuntime, policyVersion string) project.SecurityReportInput {
	model := runtime.effective
	return project.SecurityReportInput{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Path: file.Path, ContentHash: file.ContentHash, Source: project.SecuritySourceAI, Model: runtime.profile.Model, ConfiguredModel: runtime.profile.Model, Profile: model.Profile, Scope: model.Scope, ProviderOrigin: model.ProviderOrigin, ReasoningEffort: securityReasoningEffort(model.ReasoningEffort), PromptVersion: project.SecurityPromptVersion, ContextPolicyVersion: policyVersion}
}

func validateAnalysisStageRequest(request analysisFileStageRequest, authority analysisFileStageAuthority) error {
	if err := request.Models.Validate(); err != nil {
		return err
	}
	if err := request.Run.Validate(); err != nil {
		return err
	}
	if len(request.Stage.Categories()) == 0 || request.File.Path == "" || request.File.ContentHash == "" || request.File.Language == "" || request.RemainingAttempts < 0 || request.RemainingAttempts > 4 || authority.Check == nil || authority.BeforeAttempt == nil || authority.Publish == nil {
		return fmt.Errorf("analysis stage request or authority is incomplete")
	}
	return nil
}

func rejectedAnalysisStage(ctx context.Context, stage AnalysisStage, attempts int, err error) (analysisFileStageResult, error) {
	// Never return a newly rejected result body or a known-zero count.
	result := analysisFileStageResult{Progress: AnalysisStageProgress{Stage: stage, Status: AnalysisStageFailed, Attempts: attempts, Reason: "Analysis stage could not complete."}}
	switch {
	case errors.Is(err, errAnalysisAttemptBudget):
		result.Progress.Status = AnalysisStagePending
		result.Progress.Reason = "The stage needs an additional attempt allowance."
	case ctx.Err() != nil:
		result.Progress.Status = AnalysisStageCanceled
	case errors.Is(err, project.ErrRevisionConflict):
		result.Progress.Status = AnalysisStageStale
	case errors.Is(err, project.ErrSecurityRulesUnavailable):
		result.Progress.Status = AnalysisStageUnavailable
		result.Progress.Reason = noRulePackReason
		return result, nil
	case errors.Is(err, project.ErrExcludedFile), errors.Is(err, project.ErrUnsupportedFile):
		result.Progress.Status = AnalysisStageSkipped
		result.Progress.Reason = "The file is not eligible for this source analysis."
		return result, nil
	}
	return result, err
}
