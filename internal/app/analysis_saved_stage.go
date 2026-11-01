package app

import (
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// savedAnalysisStage is a stage's saved report as admission and the file
// checklist see it. Result carries the report and its progress when it is
// usable for the current file, instructions and models.
type savedAnalysisStage struct {
	usable      bool
	status      string
	generatedAt time.Time
	result      analysisFileStageResult
}

func (s *Service) analysisStageCacheState(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, models *AnalysisModels) (bool, string, time.Time, error) {
	saved, err := s.loadSavedAnalysisStage(analysis, file, policy, stage, models)
	return saved.usable, saved.status, saved.generatedAt, err
}

// loadSavedAnalysisStage reads one stage's saved report without any model access.
func (s *Service) loadSavedAnalysisStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, models *AnalysisModels) (savedAnalysisStage, error) {
	var saved savedAnalysisStage
	var err error
	switch stage {
	case AnalysisStageSemantic:
		saved, err = s.savedSemanticStage(analysis, file, policy, models)
	case AnalysisStagePerformance:
		saved, err = s.savedPerformanceStage(analysis, file, policy, models)
	default:
		saved, err = s.savedSecurityStage(analysis, file, policy, stage, models)
	}
	saved.result.Progress.Stage = stage
	if err == nil && saved.usable {
		saved.result.Progress.ReportID = analysisReportID(analysis.ProjectID, analysis.ProjectRevision, file.Path, file.ContentHash, stage)
	}
	return saved, err
}

func (s *Service) savedSemanticStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, models *AnalysisModels) (savedAnalysisStage, error) {
	var saved savedAnalysisStage
	cache, err := project.NewFileAnalysisCache(s.manager.Root())
	if err != nil {
		return saved, err
	}
	report, err := cache.WithPolicy(policy).Load(semanticCacheInputForRuntime(&analysis, &file, file.ContentHash, policy.Version(), s.analysisModelRuntime(AnalysisStageSemantic, models)))
	if err != nil {
		return saved, err
	}
	saved.usable, saved.status, saved.generatedAt = analysisSemanticCacheUsable(report), report.Status, report.GeneratedAt
	if saved.status == project.AnalysisStatusFresh && !saved.usable {
		saved.status = project.AnalysisStatusStale
	}
	if saved.usable {
		saved.result.Semantic = report
		analysisStageEvidence(&saved.result.Progress, len(report.Risks), false, true)
	}
	return saved, nil
}

func (s *Service) savedPerformanceStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, models *AnalysisModels) (savedAnalysisStage, error) {
	var saved savedAnalysisStage
	report, err := project.LoadPerformanceFileReport(s.manager.Root(), file.Path, file.ContentHash, policy)
	if err != nil || report == nil {
		return saved, err
	}
	saved.usable, saved.status, saved.generatedAt = analysisPerformanceCacheUsable(report, analysis, s.analysisModelRuntime(AnalysisStagePerformance, models)), report.Status, report.GeneratedAt
	switch {
	case saved.status == "completed" && !saved.usable:
		saved.status = "stale"
	case saved.usable && report.Warning != "":
		saved.status = "partial"
	}
	if saved.usable {
		saved.result.Performance = report
		analysisStageEvidence(&saved.result.Progress, len(report.Findings), report.Warning != "", true)
	}
	return saved, nil
}

func (s *Service) savedSecurityStage(analysis project.Analysis, file project.IndexFile, policy *project.ContextPolicy, stage AnalysisStage, models *AnalysisModels) (savedAnalysisStage, error) {
	var saved savedAnalysisStage
	input := analysisSecurityCacheInput(analysis, file, s.analysisModelRuntime(stage, models), policy.Version())
	if stage == AnalysisStageSecurityRules {
		input = securityRulesInput(securityRulesSnapshot{analysis: analysis, file: file, policyVersion: policy.Version()})
	}
	report, err := s.loadSecurityFileReport(s.manager.Root(), input)
	if err != nil || report == nil {
		return saved, err
	}
	saved.usable, saved.status, saved.generatedAt = securityStageCacheUsable(report), report.Status, report.GeneratedAt
	if saved.usable {
		saved.result.Security = report
		analysisStageEvidence(&saved.result.Progress, len(report.Findings), report.Status == project.SecurityStatusPartial, true)
	}
	return saved, nil
}

// recordSavedAnalysisStages settles every stage the plan reuses from a saved
// report when the run is admitted: the dashboard shows those results at once,
// a cancel cannot touch them, and only work that needs a model is queued.
// A report that went missing since the preview leaves the stage queued, except
// in a retained file, which never dispatches: that stage is left unanalyzed.
func (s *Service) recordSavedAnalysisStages(run *AnalysisRun) error {
	analysis, index, policy, err := s.analysisInputs()
	if err != nil {
		return err
	}
	indexed := make(map[string]project.IndexFile, len(index.Files))
	for _, file := range index.Files {
		indexed[file.Path] = file
	}
	for i, planned := range run.Plan.Files {
		for j, stage := range planned.Stages {
			if !stage.Cached || run.Files[i].Stages[j].Status != AnalysisStagePending {
				continue
			}
			saved, err := s.loadSavedAnalysisStage(*analysis, indexed[planned.Path], policy, stage.Stage, run.Plan.Models)
			if err != nil {
				return err
			}
			switch {
			case saved.usable:
				recordAnalysisResult(run, i, j, saved.result)
			case planned.Retained:
				run.Files[i].Stages[j].Status, run.Files[i].Stages[j].Reason = AnalysisStageSkipped, carriedMissingReason
			}
		}
	}
	return nil
}
