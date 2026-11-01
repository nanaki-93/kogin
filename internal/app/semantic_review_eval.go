package app

import (
	"encoding/json"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// FileAnalysisEvaluationProvider returns only the non-secret provider metadata
// needed for prompt binding and evaluation authorization. Endpoint credentials,
// paths, queries, and fragments never cross this boundary.
func FileAnalysisEvaluationProvider(profile config.ModelProfile) EffectiveModel {
	return EffectiveModel{
		Scope: string(profile.Scope), Model: profile.Model,
		ProviderOrigin:   modelProviderOrigin(profile),
		RemoteProvider:   profile.IsCLI() || !isLoopbackURL(profile.APIBaseURL),
		ContextMaxTokens: profile.ContextMaxTokens,
	}
}

// PrepareFileAnalysisEvaluation uses the production prompt and context binding.
// Callers use FileAnalysisResponseSchema and EngineeringInsightPromptVersion for
// the corresponding contract; fixture preparation and transport stay outside app.
func PrepareFileAnalysisEvaluation(source string, analysis project.Analysis, index *project.ProjectIndex, target project.IndexFile, profile config.ModelProfile) (string, error) {
	model := FileAnalysisEvaluationProvider(profile)
	return semanticPrompt(source, analysis, index, target, bindContextManifestModel(semanticManifest(target), model))
}

// FileAnalysisAssessment contains no response text or parser errors that could
// reveal it. Diagnostics remain available for decoded but unusable parents.
type FileAnalysisAssessment struct {
	UsableSummary bool
	Diagnostics   FileAnalysisDiagnostics
}

// AssessFileAnalysisEvaluation shares the production semantic validator without
// invoking a provider, modifying project state, or retaining response prose.
// Missing/invalid risk categories reject the parent, while optional-field
// diagnostics remain available under the same rules as production parsing.
func AssessFileAnalysisEvaluation(content string, target project.IndexFile, source string) FileAnalysisAssessment {
	parsed, err := validateSemanticAnalysisResponse(content, target, source, true)
	return FileAnalysisAssessment{UsableSummary: err == nil, Diagnostics: parsed.Diagnostics}
}

// FileAnalysisDiagnostics records only locations, classifications, and bounded
// per-field metadata, never field text. It describes supplied optional sections
// before display retention limits; accepted insights can therefore be omitted
// from the displayed summary without being classified as rejected.
type FileAnalysisDiagnostics struct {
	Insights                    []FileAnalysisInsightDiagnostic
	TaskSpecDegradedRiskIndices []int
}

// FileAnalysisInsightDiagnostic is source-free metadata for one optional insight.
type FileAnalysisInsightDiagnostic struct {
	Location              string                                            `json:"location"`
	Index                 *int                                              `json:"index,omitempty"`
	Reason                project.OptionalEngineeringInsightReason          `json:"reason"`
	Presence              project.OptionalEngineeringInsightPresence        `json:"presence"`
	Mechanism             project.OptionalEngineeringInsightFieldDiagnostic `json:"mechanism"`
	WhyItMattersHere      project.OptionalEngineeringInsightFieldDiagnostic `json:"why_it_matters_here"`
	TradeoffOrFailureMode project.OptionalEngineeringInsightFieldDiagnostic `json:"tradeoff_or_failure_mode"`
	TransferableLesson    project.OptionalEngineeringInsightFieldDiagnostic `json:"transferable_lesson"`
}

// OptionalInsight preserves rejection precedence across every supplied location.
func (diagnostics FileAnalysisDiagnostics) OptionalInsight() string {
	state := "omitted"
	for _, insight := range diagnostics.Insights {
		switch insight.Reason {
		case project.OptionalEngineeringInsightAbsent, project.OptionalEngineeringInsightNull:
		case project.OptionalEngineeringInsightAccepted:
			state = "present"
		default:
			return "rejected"
		}
	}
	return state
}

// Degraded includes independent task-spec failures as well as insights.
func (diagnostics FileAnalysisDiagnostics) Degraded() bool {
	return diagnostics.OptionalInsight() == "rejected" || len(diagnostics.TaskSpecDegradedRiskIndices) > 0
}

func (diagnostics *FileAnalysisDiagnostics) parseInsight(location string, index *int, raw json.RawMessage) *project.EngineeringInsight {
	insight, diagnostic := parseFileAnalysisEngineeringInsightDiagnostic(raw)
	diagnostics.Insights = append(diagnostics.Insights, FileAnalysisInsightDiagnostic{
		Location: location, Index: index, Reason: diagnostic.Reason,
		Presence: diagnostic.Presence, Mechanism: diagnostic.Mechanism,
		WhyItMattersHere:      diagnostic.WhyItMattersHere,
		TradeoffOrFailureMode: diagnostic.TradeoffOrFailureMode,
		TransferableLesson:    diagnostic.TransferableLesson,
	})
	return insight
}
