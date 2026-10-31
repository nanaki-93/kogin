package handbook

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/textutil"
)

func buildDocsScenarios(analysis Analysis) []ProjectDocsScenario {
	scenarios := make([]ProjectDocsScenario, 0, 4)
	for i, flow := range analysis.Report.Flows {
		if len(scenarios) >= 6 {
			break
		}
		scenarios = append(scenarios, scenarioFromFlow(i, flow))
	}
	return scenarios
}

func scenarioFromFlow(index int, flow string) ProjectDocsScenario {
	diagram := ""
	summary := strings.TrimSpace(flow)
	title := fmt.Sprintf("Suggested flow %d", index+1)
	if isMermaidDiagram(flow) {
		diagram = flow
		summary = extractMermaidSummary(flow)
	} else if len(summary) > 72 {
		title = textutil.Truncate(summary, 68) + "..."
	} else if summary != "" {
		title = summary
	}
	return ProjectDocsScenario{
		Title:     title,
		Trigger:   "Not established by indexed evidence",
		Invariant: "Unverified model interpretation",
		Summary:   "Model interpretation: " + summary,
		Diagram:   diagram,
		Steps:     []ProjectDocsScenarioStep{},
	}
}

func extractMermaidSummary(diagram string) string {
	lines := strings.Split(diagram, "\n")
	nodes := make([]string, 0, 4)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "flowchart") || strings.HasPrefix(trimmed, "sequenceDiagram") || strings.HasPrefix(trimmed, "subgraph") || trimmed == "end" {
			continue
		}
		nodes = append(nodes, trimmed)
		if len(nodes) >= 3 {
			break
		}
	}
	if len(nodes) == 0 {
		return "No execution steps were supplied."
	}
	return "Execution path: " + strings.Join(nodes, " · ")
}

func buildDocsDecisions(analysis Analysis) []ProjectDocsDecision {
	decisions := make([]ProjectDocsDecision, 0, 4)
	if insight := analysis.Report.EngineeringInsight; insight != nil {
		decisions = append(decisions, ProjectDocsDecision{
			Title:    "Suggested architectural mechanism",
			Category: "Model interpretation",
			Decision: insight.Mechanism + " — " + insight.WhyItMattersHere,
			Tradeoff: insight.TradeoffOrFailureMode,
		})
	}
	for _, risk := range analysis.Report.Risks {
		if len(decisions) >= 4 {
			break
		}
		if risk.EngineeringInsight != nil {
			decisions = append(decisions, ProjectDocsDecision{
				Title:    fmt.Sprintf("Suggested risk (%s priority)", strings.ToUpper(risk.Severity)),
				Category: "Model interpretation",
				Decision: risk.EngineeringInsight.Mechanism,
				Tradeoff: risk.EngineeringInsight.TradeoffOrFailureMode,
			})
		} else if risk.Summary != "" {
			decisions = append(decisions, ProjectDocsDecision{
				Title:    fmt.Sprintf("Suggested risk (%s)", strings.ToUpper(risk.Severity)),
				Category: "Model interpretation",
				Decision: risk.Summary,
				Tradeoff: "No tradeoff evidence was supplied.",
			})
		}
	}
	return decisions
}

func buildDocsGlossary(analysis Analysis, components []ProjectDocsComponent) []ProjectDocsGlossaryItem {
	glossary := make([]ProjectDocsGlossaryItem, 0, len(components)+2)
	for _, comp := range components {
		if len(glossary) >= 6 {
			break
		}
		glossary = append(glossary, ProjectDocsGlossaryItem{
			Term:       comp.Name,
			Definition: fmt.Sprintf("%s (%s): %s", comp.Layer, comp.Path, comp.Summary),
		})
	}
	if len(analysis.Report.NextSteps) > 0 {
		glossary = append(glossary, ProjectDocsGlossaryItem{
			Term:       "Suggested next step",
			Definition: analysis.Report.NextSteps[0],
		})
	}
	return glossary
}
