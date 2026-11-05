package app

import (
	"context"
	"sort"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
)

// analysisTestFileExclusion: tests are judged by running them, not reviewed;
// the project profile can turn their review on.
const analysisTestFileExclusion = "Test file: judged by running it. Turn on test file analysis in the project profile to review it."

func (s *Service) planAnalysisFiles(ctx context.Context, root string, analysis project.Analysis, indexedFiles []project.IndexFile, policy *project.ContextPolicy, request AnalysisPreviewRequest, preview *AnalysisRunPreview) error {
	exclusions, err := newAnalysisExclusions(root, policy)
	if err != nil {
		return err
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
		reason := exclusions.reason(file)
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

// analysisExclusions decide which indexed files a run leaves out: by source
// policy, by the user's file selection, and test files unless the project
// profile has them analyzed.
type analysisExclusions struct {
	policy    *project.ContextPolicy
	ignored   map[string]bool
	skipTests bool
	resolver  *rules.Resolver
}

func newAnalysisExclusions(root string, policy *project.ContextPolicy) (*analysisExclusions, error) {
	excludedPaths, err := loadAnalysisSelection(root)
	if err != nil {
		return nil, err
	}
	ignored := make(map[string]bool, len(excludedPaths))
	for _, path := range excludedPaths {
		ignored[path] = true
	}
	return &analysisExclusions{policy: policy, ignored: ignored, skipTests: !profileFor(root).AnalyzeTests, resolver: rules.NewResolver(root)}, nil
}

// reason says why the run leaves file out; empty when it analyzes it.
func (e *analysisExclusions) reason(file project.IndexFile) string {
	switch reason := analysisFileExclusion(file, e.policy); {
	case reason != "":
		return reason
	case e.ignored[file.Path]:
		return analysisSelectionExclusion
	case e.skipTests && e.resolver.IsTestFile(file.Path):
		return analysisTestFileExclusion
	}
	return ""
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
	case stage == AnalysisStageSecurityRules && !project.SecurityRulesCover(file.Path):
		return noRulePackReason
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
