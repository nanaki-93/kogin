package app

import (
	"context"
	"errors"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

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
