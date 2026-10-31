package handbook

import (
	"fmt"
	"sort"
	"strings"
)

func buildDocsContext(analysis Analysis, components []ProjectDocsComponent) ProjectDocsContext {
	purpose := strings.TrimSpace(analysis.Report.Purpose)
	if purpose == "" {
		name := analysis.Name
		if name == "" {
			name = "workspace"
		}
		purpose = fmt.Sprintf("%s software system (%d source files across %d indexed files).", name, analysis.SourceFileCount, analysis.FileCount)
	} else {
		purpose = "Model interpretation: " + purpose
	}
	arch := strings.TrimSpace(analysis.Report.Architecture)
	topology := arch
	// A model's diagram is not shown: the components diagram is drawn from the code.
	if isMermaidDiagram(arch) || topology == "" {
		topology = summarizeLayerTopology(components)
	} else {
		topology = "Model interpretation (unverified): " + topology
	}
	stack := buildDocsStack(analysis)
	goalPursuit := buildDocsGoalPursuit(analysis, components, topology)
	return ProjectDocsContext{Purpose: purpose, GoalPursuit: goalPursuit, Topology: topology, Stack: stack}
}

func buildDocsGoalPursuit(analysis Analysis, components []ProjectDocsComponent, topology string) []string {
	steps := make([]string, 0, 4)
	if insight := analysis.Report.EngineeringInsight; insight != nil && strings.TrimSpace(insight.Mechanism) != "" {
		why := strings.TrimSpace(insight.WhyItMattersHere)
		if why != "" {
			steps = append(steps, fmt.Sprintf("Model interpretation: %s — %s", strings.TrimSpace(insight.Mechanism), why))
		} else {
			steps = append(steps, "Model interpretation: "+strings.TrimSpace(insight.Mechanism))
		}
	}
	if len(analysis.Report.EntryPoints) > 0 {
		steps = append(steps, "Model-reported entry points (unverified): "+strings.Join(analysis.Report.EntryPoints, ", "))
	}
	for _, comp := range components {
		if len(steps) >= 4 {
			break
		}
		if comp.Summary != "" {
			steps = append(steps, fmt.Sprintf("%s (%s): %s", comp.Name, comp.Layer, comp.Summary))
		}
	}
	if len(steps) == 0 && topology != "" {
		steps = append(steps, topology)
	}
	return steps
}

func isMermaidDiagram(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "flowchart ") ||
		strings.HasPrefix(trimmed, "graph ") ||
		strings.HasPrefix(trimmed, "sequenceDiagram")
}

func summarizeLayerTopology(components []ProjectDocsComponent) string {
	if len(components) == 0 {
		return "Architecture relationships are unavailable."
	}
	layers := make([]string, 0, 4)
	seen := make(map[string]bool)
	for _, comp := range components {
		if !seen[comp.Layer] {
			seen[comp.Layer] = true
			layers = append(layers, comp.Layer)
		}
	}
	return fmt.Sprintf("%d components grouped by directory, with their imports resolved from the code: %s.", len(components), strings.Join(layers, ", "))
}

func buildDocsStack(analysis Analysis) []ProjectDocsStackEntry {
	type langCount struct {
		lang  string
		count int
	}
	items := make([]langCount, 0, len(analysis.Languages))
	for lang, count := range analysis.Languages {
		items = append(items, langCount{lang: lang, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].lang < items[j].lang
	})
	stack := make([]string, 0, len(items)+2)
	if analysis.Type != "" && analysis.Type != "unknown" {
		if analysis.BuildFile != "" {
			stack = append(stack, fmt.Sprintf("%s (%s)", strings.ToUpper(analysis.Type[:1])+analysis.Type[1:], analysis.BuildFile))
		} else {
			stack = append(stack, strings.ToUpper(analysis.Type[:1])+analysis.Type[1:])
		}
	}
	for _, item := range items {
		stack = append(stack, fmt.Sprintf("%s (%d files)", item.lang, item.count))
	}
	if len(stack) == 0 {
		stack = append(stack, "Polyglot Source Tree")
	}
	return stack
}

type ProjectDocsStackEntry = string
