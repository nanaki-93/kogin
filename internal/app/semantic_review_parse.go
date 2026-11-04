package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

type semanticAnalysisResponse struct {
	Diagnostics        FileAnalysisDiagnostics     `json:"-"`
	Purpose            string                      `json:"purpose"`
	Responsibilities   []string                    `json:"responsibilities"`
	Dependencies       []string                    `json:"dependencies"`
	SideEffects        []string                    `json:"side_effects"`
	Risks              []project.Finding           `json:"risks"`
	Suggestions        []project.Suggestion        `json:"suggestions"`
	EngineeringInsight *project.EngineeringInsight `json:"engineering_insight,omitempty"`
}

// semanticAnalysisWireResponse keeps the optional task_spec untrusted until its
// target has been checked against the indexed file. A malformed suggestion must
// not discard the rest of an otherwise usable file summary.
type semanticAnalysisWireResponse struct {
	Purpose            string                       `json:"purpose"`
	Responsibilities   []string                     `json:"responsibilities"`
	Dependencies       []string                     `json:"dependencies"`
	SideEffects        []string                     `json:"side_effects"`
	Risks              []semanticAnalysisFinding    `json:"risks"`
	Suggestions        []semanticAnalysisSuggestion `json:"suggestions"`
	EngineeringInsight json.RawMessage              `json:"engineering_insight"`
}

type semanticAnalysisFinding struct {
	Category project.FindingCategory `json:"category"`
	Severity string                  `json:"severity"`
	Title    string                  `json:"title"`
	Summary  string                  `json:"summary"`
	Impact   string                  `json:"impact"`
	Fix      string                  `json:"fix"`
	Verify   string                  `json:"verify"`
	// Anchor copies the line of TARGET_SOURCE the risk is at; Kogin
	// finds its line number.
	Anchor   string          `json:"anchor"`
	TaskSpec json.RawMessage `json:"task_spec"`
	Insight  json.RawMessage `json:"engineering_insight"`
}

type semanticAnalysisSuggestion struct {
	Title   string          `json:"title"`
	Summary string          `json:"summary"`
	Action  string          `json:"action"`
	Insight json.RawMessage `json:"engineering_insight"`
}

func parseSemanticReviewResponse(output string, target project.IndexFile, source string) (semanticAnalysisResponse, error) {
	return validateSemanticAnalysisResponse(output, target, source, false)
}

func validateSemanticAnalysisResponse(output string, target project.IndexFile, source string, collectRejectedParentDiagnostics bool) (semanticAnalysisResponse, error) {
	var sizeErr error
	if len(output) == 0 || len(output) > maxSemanticAnalysisBytes {
		sizeErr = fmt.Errorf("semantic analysis response is empty or too large")
		if !collectRejectedParentDiagnostics {
			return semanticAnalysisResponse{}, sizeErr
		}
	}
	var wire semanticAnalysisWireResponse
	decodeErr := decodeSemanticAnalysis(output, &wire)
	if decodeErr != nil {
		if sizeErr != nil {
			return semanticAnalysisResponse{}, sizeErr
		}
		return semanticAnalysisResponse{}, decodeErr
	}
	parentErr := sizeErr
	if parentErr == nil {
		parentErr = validateSemanticAnalysisLimits(wire)
	}
	if parentErr != nil && !collectRejectedParentDiagnostics {
		return semanticAnalysisResponse{}, parentErr
	}
	// Evaluation historically records bounded field metadata for any decoded
	// parent, including parents rejected by size or collection limits. Normal
	// production requests retain their cheap rejection before section parsing.
	parsed := parseSemanticSections(wire, target, source)
	if parentErr != nil {
		return semanticAnalysisResponse{Diagnostics: parsed.Diagnostics}, parentErr
	}

	if err := validateSemanticRisks(parsed.Risks); err != nil {
		return semanticAnalysisResponse{Diagnostics: parsed.Diagnostics}, err
	}
	limitFileAnalysisInsights(&parsed.EngineeringInsight, parsed.Risks, parsed.Suggestions)
	return parsed, nil
}

func parseSemanticSections(wire semanticAnalysisWireResponse, target project.IndexFile, source string) semanticAnalysisResponse {
	parsed := semanticAnalysisResponse{
		Purpose: wire.Purpose, Responsibilities: wire.Responsibilities,
		Dependencies: wire.Dependencies, SideEffects: wire.SideEffects,
		Diagnostics: FileAnalysisDiagnostics{Insights: make([]FileAnalysisInsightDiagnostic, 0, 1+len(wire.Risks)+len(wire.Suggestions))},
	}
	parsed.EngineeringInsight = parsed.Diagnostics.parseInsight("top_level", nil, wire.EngineeringInsight)
	parsed.Risks = parseSemanticRisks(wire.Risks, target, source, &parsed.Diagnostics)
	parsed.Suggestions = parseSemanticSuggestions(wire.Suggestions, &parsed.Diagnostics)
	return parsed
}

func decodeSemanticAnalysis(output string, parsed *semanticAnalysisWireResponse) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(parsed); err != nil {
		return fmt.Errorf("parse semantic analysis JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("semantic analysis JSON must contain one object")
	}
	return nil
}

func validateSemanticAnalysisLimits(parsed semanticAnalysisWireResponse) error {
	if strings.TrimSpace(parsed.Purpose) == "" || len(parsed.Responsibilities) > 32 || len(parsed.Dependencies) > 64 || len(parsed.SideEffects) > 32 || len(parsed.Risks) > 32 || len(parsed.Suggestions) > 32 {
		return fmt.Errorf("semantic analysis JSON is incomplete or exceeds limits")
	}
	return nil
}

func parseSemanticRisks(risks []semanticAnalysisFinding, target project.IndexFile, source string, diagnostics *FileAnalysisDiagnostics) []project.Finding {
	parsed := make([]project.Finding, 0, len(risks))
	for index, risk := range risks {
		finding := project.Finding{
			Category:           risk.Category,
			Severity:           strings.ToLower(strings.TrimSpace(risk.Severity)),
			Title:              strings.Join(strings.Fields(risk.Title), " "),
			Summary:            strings.TrimSpace(risk.Summary),
			Impact:             briefField(risk.Impact),
			Fix:                briefField(risk.Fix),
			Verify:             briefField(risk.Verify),
			TaskSpec:           parseOptionalBugTaskSpec(risk.TaskSpec, target, source),
			EngineeringInsight: diagnostics.parseInsight("risk", &index, risk.Insight),
		}
		finding.StartLine, finding.EndLine = anchoredLines(source, risk.Anchor)
		if len(risk.TaskSpec) > 0 && strings.TrimSpace(string(risk.TaskSpec)) != "null" && finding.TaskSpec == nil {
			diagnostics.TaskSpecDegradedRiskIndices = append(diagnostics.TaskSpecDegradedRiskIndices, index)
		}
		parsed = append(parsed, finding)
	}
	return parsed
}

func parseSemanticSuggestions(suggestions []semanticAnalysisSuggestion, diagnostics *FileAnalysisDiagnostics) []project.Suggestion {
	parsed := make([]project.Suggestion, 0, len(suggestions))
	for index, suggestion := range suggestions {
		insight := diagnostics.parseInsight("suggestion", &index, suggestion.Insight)
		parsed = append(parsed, project.Suggestion{Title: suggestion.Title, Summary: suggestion.Summary, Action: suggestion.Action, EngineeringInsight: insight})
	}
	return parsed
}

// parseFileAnalysisEngineeringInsightDiagnostic applies the selected-file
// requirement that all four insight fields be present and non-empty. The
// shared project parser remains the single source for JSON shape,
// normalization, and aggregate-size validation.
func parseFileAnalysisEngineeringInsightDiagnostic(raw json.RawMessage) (*project.EngineeringInsight, project.OptionalEngineeringInsightDiagnostic) {
	insight, diagnostic := project.ParseOptionalEngineeringInsightDiagnostic(raw)
	if diagnostic.Reason != project.OptionalEngineeringInsightAccepted {
		return nil, diagnostic
	}
	if !diagnostic.TradeoffOrFailureMode.Present || !diagnostic.TradeoffOrFailureMode.RuneCountKnown || diagnostic.TradeoffOrFailureMode.Runes == 0 || !diagnostic.TransferableLesson.Present || !diagnostic.TransferableLesson.RuneCountKnown || diagnostic.TransferableLesson.Runes == 0 {
		diagnostic.Reason = project.OptionalEngineeringInsightEmptyRequiredField
		return nil, diagnostic
	}
	return insight, diagnostic
}

func limitFileAnalysisInsights(report **project.EngineeringInsight, risks []project.Finding, suggestions []project.Suggestion) {
	remaining := 3
	keep := func(insight **project.EngineeringInsight) {
		if *insight == nil {
			return
		}
		if remaining == 0 {
			*insight = nil
			return
		}
		remaining--
	}
	keep(report)
	for index := range risks {
		keep(&risks[index].EngineeringInsight)
	}
	for index := range suggestions {
		keep(&suggestions[index].EngineeringInsight)
	}
}

func validateSemanticRisks(risks []project.Finding) error {
	for _, risk := range risks {
		if !risk.Category.Valid() || risk.Severity != "low" && risk.Severity != "medium" && risk.Severity != "high" || risk.Summary == "" {
			return fmt.Errorf("semantic analysis contains an invalid risk")
		}
	}
	return nil
}

// briefField keeps one line of a risk's brief, at most 300 characters.
func briefField(text string) string {
	return textutil.Clip(strings.Join(strings.Fields(text), " "), 300)
}
