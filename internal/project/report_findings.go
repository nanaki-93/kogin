package project

import (
	"os"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/syntax"
)

// The Performance and Security stages keep their own per-file reports. These
// turn a report's findings into issues, so they are listed, triaged and fixed
// like the rest; each keeps its lines.

// FindingsForPerformanceReport lists a Performance report's findings as AI
// suggestions. A report that did not complete has none.
func FindingsForPerformanceReport(report PerformanceFileReport) []UnifiedFinding {
	if report.Status != "completed" && report.Status != "stale" {
		return []UnifiedFinding{}
	}
	findings := make([]UnifiedFinding, 0, len(report.Findings))
	for _, item := range report.Findings {
		evidence := labeledLines([2]string{"Workload", item.WorkloadConditions}, [2]string{"Recommendation", item.Recommendation}, [2]string{"Tradeoff", item.Tradeoff}, [2]string{"How to verify", item.VerificationPlan})
		findings = append(findings, UnifiedFinding{Category: FindingCategoryPerformance, Source: FindingSourceAI, Confidence: FindingConfidenceSuggested,
			Severity: performanceSeverity(item.PotentialImpact), Title: item.Title, Message: item.ObservedPattern, Rule: "performance." + item.Category,
			FileHash: report.ContentHash, Location: FindingLocation{Path: report.Path, StartLine: item.StartLine, EndLine: item.EndLine},
			Evidence: evidence, OriginatingAnalysis: "performance", EngineeringInsight: CloneEngineeringInsight(item.EngineeringInsight), DetectedAt: report.GeneratedAt})
	}
	return findings
}

// performanceSeverity ranks a potential impact; an unknown one ranks low.
func performanceSeverity(impact string) string {
	if impact == "high" || impact == "medium" {
		return impact
	}
	return "low"
}

// FindingsForSecurityReport lists a Security report's findings: the rules'
// matches as reported findings, the AI review's as suggestions.
func FindingsForSecurityReport(report SecurityFileReport) []UnifiedFinding {
	if report.Status != SecurityStatusCompleted && report.Status != SecurityStatusPartial && report.Status != SecurityStatusStale {
		return []UnifiedFinding{}
	}
	source, confidence := FindingSourceAI, FindingConfidenceSuggested
	if report.Source == SecuritySourceDeterministic {
		source, confidence = FindingSourceSecurityRules, FindingConfidenceToolReported
	}
	findings := make([]UnifiedFinding, 0, len(report.Findings))
	for _, item := range report.Findings {
		rule := item.Rule
		if item.CWE != "" {
			rule += " (" + item.CWE + ")"
		}
		evidence := labeledLines([2]string{"Preconditions", item.Preconditions}, [2]string{"Remediation", item.Remediation}, [2]string{"How to verify", item.VerificationIdea}, [2]string{"Reference", item.Reference})
		findings = append(findings, UnifiedFinding{Category: FindingCategorySecurity, Source: source, Confidence: confidence, Severity: securitySeverity(item.Severity),
			Title: item.Title, Message: item.ObservedCondition, Rule: rule, FileHash: report.ContentHash,
			Location: FindingLocation{Path: item.Anchor.Path, StartLine: item.Anchor.StartLine, EndLine: item.Anchor.EndLine},
			Evidence: evidence, OriginatingAnalysis: "security", EngineeringInsight: CloneEngineeringInsight(item.EngineeringInsight), DetectedAt: report.GeneratedAt})
	}
	return findings
}

// securitySeverity folds the security scale into the issues' three levels.
func securitySeverity(severity string) string {
	switch severity {
	case "critical", "high":
		return "high"
	case "medium":
		return "medium"
	}
	return "low"
}

func labeledLines(items ...[2]string) string {
	var lines []string
	for _, item := range items {
		if text := strings.TrimSpace(item[1]); text != "" {
			lines = append(lines, item[0]+": "+text)
		}
	}
	return strings.Join(lines, "\n")
}

// AttachSymbols names the function around each finding's first line when the
// file still has the content the finding was made from.
func AttachSymbols(root string, findings []UnifiedFinding) {
	parsed := map[string][]syntax.Function{}
	for index := range findings {
		location := &findings[index].Location
		if location.Path == "" || location.StartLine == 0 || location.Symbol != "" {
			continue
		}
		functions, ok := parsed[location.Path]
		if !ok {
			functions = currentFunctions(root, location.Path, findings[index].FileHash)
			parsed[location.Path] = functions
		}
		location.Symbol = syntax.Enclosing(functions, location.StartLine)
	}
}

// currentFunctions parses a file's functions, or none when the file is gone,
// too large or no longer has the given content hash.
func currentFunctions(root, path, hash string) []syntax.Function {
	full, err := ResolveFile(root, path)
	if err != nil {
		return nil
	}
	info, err := os.Stat(full)
	if err != nil || info.Size() > 2<<20 {
		return nil
	}
	data, err := os.ReadFile(full)
	if err != nil || hash != "" && hash != contentHash(data) {
		return nil
	}
	return syntax.Analyze(path, data).Functions
}
