package app

import (
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func analysisStageNeedsWork(status AnalysisStageStatus) bool {
	return status == AnalysisStagePending || status == AnalysisStageRunning || status == AnalysisStageInterrupted
}

func newAnalysisRun(preview AnalysisRunPreview) *AnalysisRun {
	now := time.Now().UTC()
	run := &AnalysisRun{SchemaVersion: AnalysisRunSchemaVersion, Identity: AnalysisRunIdentity{AnalysisQueueIdentity: preview.Identity, ID: newOpaqueID("analysis"), Generation: newOpaqueID("generation")},
		Plan: preview, Status: AnalysisRunQueued, Files: []AnalysisRunFile{}, Sections: []AnalysisSectionProgress{}, CreatedAt: now, UpdatedAt: now}
	for _, file := range preview.Files {
		progress := AnalysisRunFile{AnalysisFileIdentity: file.AnalysisFileIdentity, Stages: []AnalysisStageProgress{}}
		for _, stage := range file.Stages {
			status := AnalysisStagePending
			if !stage.Eligible {
				status = AnalysisStageSkipped
			}
			if stage.Reason == "The model for this stage is not configured." && !stage.Cached {
				status = AnalysisStageUnavailable
			}
			if stage.Carried != "" {
				status = stage.Carried
			}
			progress.Stages = append(progress.Stages, AnalysisStageProgress{Stage: stage.Stage, Status: status, Reason: stage.Reason})
		}
		run.Files = append(run.Files, progress)
	}
	refreshAnalysisSections(run)
	return run
}

func refreshAnalysisSections(run *AnalysisRun) {
	refreshAnalysisSectionsWithEligibility(run, false)
}

// includeIneligible validates the original schema-1 aggregate before upgrading it.
func refreshAnalysisSectionsWithEligibility(run *AnalysisRun, includeIneligible bool) {
	counts := make(map[project.FindingCategory]int, len(run.Sections))
	for _, section := range run.Sections {
		if section.FindingCount != nil {
			counts[section.Category] = *section.FindingCount
		}
	}
	run.Sections = make([]AnalysisSectionProgress, 0, 3)
	for _, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		section := AnalysisSectionProgress{Category: category}
		for i, file := range run.Files {
			for j, stage := range file.Stages {
				plan := run.Plan.Files[i].Stages[j]
				if !includeIneligible && !plan.Eligible {
					continue
				}
				for _, consumer := range stage.Stage.Categories() {
					if consumer != category {
						continue
					}
					countAnalysisStage(&section.Coverage, stage.Status)
				}
			}
		}
		if section.Coverage.Succeeded+section.Coverage.Partial > 0 {
			count := counts[category]
			section.FindingCount = &count
		}
		section.Status = analysisCoverageStatus(section.Coverage, section.FindingCount, run.Status)
		run.Sections = append(run.Sections, section)
	}
}

func countAnalysisStage(coverage *AnalysisRunCoverage, status AnalysisStageStatus) {
	coverage.Total++
	switch status {
	case AnalysisStagePending, AnalysisStageInterrupted:
		coverage.Pending++
	case AnalysisStageRunning:
		coverage.Running++
	case AnalysisStageCompleted, AnalysisStageCompletedEmpty:
		coverage.Succeeded++
	case AnalysisStagePartial:
		coverage.Partial++
	case AnalysisStageFailed:
		coverage.Failed++
	case AnalysisStageUnavailable:
		coverage.Unavailable++
	default:
		coverage.Skipped++
	}
}

func analysisCoverageStatus(coverage AnalysisRunCoverage, count *int, lifecycle AnalysisRunStatus) AnalysisRunStatus {
	switch lifecycle {
	case AnalysisRunStale, AnalysisRunCanceled, AnalysisRunCanceling, AnalysisRunInterrupted:
		return lifecycle
	}
	if coverage.Pending+coverage.Running > 0 {
		switch lifecycle {
		case AnalysisRunQueued, AnalysisRunPausing, AnalysisRunPaused:
			return lifecycle
		}
		return AnalysisRunRunning
	}
	if coverage.Total > 0 && coverage.Succeeded == coverage.Total {
		if count != nil && *count == 0 {
			return AnalysisRunCompletedEmpty
		}
		return AnalysisRunCompleted
	}
	if coverage.Succeeded+coverage.Partial > 0 {
		return AnalysisRunPartial
	}
	if coverage.Failed > 0 {
		return AnalysisRunFailed
	}
	return AnalysisRunUnavailable
}

func analysisFinishedStatus(run *AnalysisRun) AnalysisRunStatus {
	coverage := AnalysisRunCoverage{}
	count := 0
	if run.Features != nil {
		countAnalysisStage(&coverage, run.Features.Status)
		if run.Features.SuggestionCount != nil {
			count += *run.Features.SuggestionCount
		}
	}
	for _, section := range run.Sections {
		c := section.Coverage
		coverage.Total += c.Total
		coverage.Pending += c.Pending
		coverage.Running += c.Running
		coverage.Succeeded += c.Succeeded
		coverage.Partial += c.Partial
		coverage.Failed += c.Failed
		coverage.Skipped += c.Skipped
		coverage.Unavailable += c.Unavailable
		if section.FindingCount != nil {
			count += *section.FindingCount
		}
	}
	return analysisCoverageStatus(coverage, &count, AnalysisRunRunning)
}

// Only a first accepted result contributes counts. Completed/partial stages are
// never redispatched on resume, so evidence cannot be counted twice.
func recordAnalysisResult(run *AnalysisRun, fileIndex, stageIndex int, result analysisFileStageResult) {
	progress := result.Progress
	progress.Attempts = run.Files[fileIndex].Stages[stageIndex].Attempts
	run.Files[fileIndex].Stages[stageIndex] = progress
	if progress.FindingCount != nil {
		for i := range run.Sections {
			section := &run.Sections[i]
			added := 0
			switch {
			case result.Semantic != nil:
				for _, risk := range result.Semantic.Risks {
					if risk.Category == section.Category {
						added++
					}
				}
			case result.Performance != nil && section.Category == project.FindingCategoryPerformance:
				added = len(result.Performance.Findings)
			case result.Security != nil && section.Category == project.FindingCategorySecurity:
				added = len(result.Security.Findings)
			}
			count := added
			if section.FindingCount != nil {
				count += *section.FindingCount
			}
			section.FindingCount = &count
		}
	}
	refreshAnalysisSections(run)
}

func nextAnalysisStage(run *AnalysisRun) (int, int, bool) {
	for i, file := range run.Files {
		for j, stage := range file.Stages {
			if analysisStageNeedsWork(stage.Status) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func analysisFileFinished(file AnalysisRunFile) bool {
	for _, stage := range file.Stages {
		if analysisStageNeedsWork(stage.Status) {
			return false
		}
	}
	return true
}

func interruptAnalysisStages(run *AnalysisRun, status AnalysisStageStatus) {
	if run.Features != nil && run.Features.Status == AnalysisStageRunning {
		run.Features.Status = status
		run.Features.SuggestionCount = nil
		run.Features.ReportHash = ""
	}
	for i := range run.Files {
		for j := range run.Files[i].Stages {
			stage := &run.Files[i].Stages[j]
			if stage.Status == AnalysisStageRunning {
				stage.Status = status
				stage.FindingCount = nil
				stage.ReportID = ""
				stage.Cached = false
			}
		}
	}
	refreshAnalysisSections(run)
}
