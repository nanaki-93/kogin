package project

import (
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
)

// SuggestedFindingsForProject adapts fresh structured AI risks without ever
// claiming they came from a verification tool.
func SuggestedFindingsForProject(report ProjectAnalysisReport) []UnifiedFinding {
	if report.Status != ProjectAnalysisStatusFresh {
		return []UnifiedFinding{}
	}
	findings := make([]UnifiedFinding, 0, len(report.Risks))
	for _, risk := range report.Risks {
		eng := risk.EngineeringInsight
		if eng == nil {
			eng = report.EngineeringInsight
		}
		findings = append(findings, UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: risk.Severity, Title: risk.Summary, Message: risk.Summary, OriginatingAnalysis: "project", EngineeringInsight: CloneEngineeringInsight(eng)})
	}
	return findings
}

// SuggestedFindingsForFile adapts fresh file-analysis risks with the precise
// file path and hash that must remain current before a fix is prepared.
func SuggestedFindingsForFile(analysis FileAnalysis) []UnifiedFinding {
	if analysis.Status != AnalysisStatusFresh {
		return []UnifiedFinding{}
	}
	findings := make([]UnifiedFinding, 0, len(analysis.Risks))
	for idx, risk := range analysis.Risks {
		title := strings.TrimSpace(risk.Title)
		if title == "" {
			// Historical reports and providers without structured output may omit titles.
			title = risk.Summary
		}
		location := FindingLocation{Path: analysis.Path, StartLine: risk.StartLine, EndLine: risk.EndLine}
		eng := resolveFileRiskInsight(analysis, risk, idx)
		findings = append(findings, UnifiedFinding{Category: risk.Category, Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: risk.Severity, Title: title, Message: risk.Summary, FileHash: analysis.ContentHash, Location: location, OriginatingAnalysis: "file", TaskSpec: insight.CloneBugTaskSpec(risk.TaskSpec), EngineeringInsight: eng})
	}
	return findings
}

func resolveFileRiskInsight(analysis FileAnalysis, risk Finding, riskIdx int) *EngineeringInsight {
	if risk.EngineeringInsight != nil {
		return CloneEngineeringInsight(risk.EngineeringInsight)
	}
	if sug := matchSuggestionForRisk(analysis.Suggestions, riskIdx, len(analysis.Risks)); sug != nil {
		if sug.EngineeringInsight != nil {
			return CloneEngineeringInsight(sug.EngineeringInsight)
		}
		lesson := strings.TrimSpace(sug.Action)
		if lesson == "" {
			lesson = strings.TrimSpace(sug.Summary)
		}
		why := strings.TrimSpace(sug.Summary)
		if why == "" {
			why = strings.TrimSpace(sug.Title)
		}
		if lesson != "" && why != "" && strings.TrimSpace(risk.Summary) != "" {
			return &EngineeringInsight{
				Mechanism:          strings.TrimSpace(risk.Summary),
				WhyItMattersHere:   why,
				TransferableLesson: lesson,
			}
		}
	}
	return CloneEngineeringInsight(analysis.EngineeringInsight)
}

func matchSuggestionForRisk(suggestions []Suggestion, riskIdx, riskCount int) *Suggestion {
	if len(suggestions) == 0 {
		return nil
	}
	if riskCount == 1 && len(suggestions) == 1 {
		return &suggestions[0]
	}
	if riskIdx >= 0 && riskIdx < len(suggestions) && len(suggestions) == riskCount {
		return &suggestions[riskIdx]
	}
	return nil
}
