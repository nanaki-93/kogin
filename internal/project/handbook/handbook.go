package handbook

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
	"github.com/nanaki-93/mini-orca/v2/internal/project/projectmap"
	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
	"github.com/nanaki-93/mini-orca/v2/internal/project/toolchain"
	"github.com/nanaki-93/mini-orca/v2/internal/textutil"
)

const (
	ProjectAnalysisStatusFresh       = "fresh"
	ProjectAnalysisStatusFailed      = "failed"
	ProjectAnalysisStatusUnavailable = "unavailable"
	ProjectAnalysisStatusStale       = "stale"
)

type EngineeringInsight = insight.EngineeringInsight
type ProjectIndex = symbols.ProjectIndex
type IndexFile = symbols.IndexFile

// ProjectAnalysisRisk is a bounded model interpretation, not a verified issue.
type ProjectAnalysisRisk struct {
	Severity           string              `json:"severity"`
	Summary            string              `json:"summary"`
	EngineeringInsight *EngineeringInsight `json:"engineering_insight,omitempty"`
}

// ProjectAnalysisReport is the authoritative, persisted interpretation of one
// project revision.
type ProjectAnalysisReport struct {
	SchemaVersion      string                `json:"schema_version"`
	ProjectID          string                `json:"project_id"`
	ProjectRevision    string                `json:"project_revision"`
	Purpose            string                `json:"purpose,omitempty"`
	Architecture       string                `json:"architecture,omitempty"`
	Components         []string              `json:"components"`
	EntryPoints        []string              `json:"entry_points"`
	Flows              []string              `json:"flows"`
	Risks              []ProjectAnalysisRisk `json:"risks"`
	NextSteps          []string              `json:"next_steps"`
	EngineeringInsight *EngineeringInsight   `json:"engineering_insight,omitempty"`
	Status             string                `json:"status"`
	Failure            string                `json:"failure,omitempty"`
	Model              string                `json:"model"`
	ConfiguredModel    string                `json:"configured_model,omitempty"`
	Profile            string                `json:"profile"`
	Scope              string                `json:"scope,omitempty"`
	ProviderOrigin     string                `json:"provider_origin,omitempty"`
	ReasoningEffort    string                `json:"reasoning_effort,omitempty"`
	PromptVersion      string                `json:"prompt_version"`
	GeneratedAt        time.Time             `json:"generated_at"`
}

// Analysis is the persisted and API-facing summary for an imported project.
type Analysis struct {
	ProjectID       string                `json:"project_id"`
	ProjectRevision string                `json:"project_revision"`
	Name            string                `json:"name"`
	Path            string                `json:"path"`
	Type            string                `json:"type"`
	BuildFile       string                `json:"build_file,omitempty"`
	FileCount       int                   `json:"file_count"`
	SourceFileCount int                   `json:"source_file_count"`
	TotalLines      int                   `json:"total_lines"`
	Languages       map[string]int        `json:"languages"`
	Files           []string              `json:"files"`
	Summary         string                `json:"summary"`
	AIStatus        string                `json:"ai_status"`
	AnalyzedAt      time.Time             `json:"analyzed_at"`
	Report          ProjectAnalysisReport `json:"report"`
}

// projectDocsSchemaVersion 3: built from the project map on every read, no
// longer stored.
const projectDocsSchemaVersion = "3"

// ProjectDocsContext describes C4 Level 1-2 system context, goal pursuit, and technology stack.
type ProjectDocsContext struct {
	Purpose     string   `json:"purpose"`
	GoalPursuit []string `json:"goal_pursuit,omitempty"`
	Topology    string   `json:"topology"`
	Stack       []string `json:"stack"`
}

// ProjectDocsDependency describes a primary runtime, library, or module dependency.
type ProjectDocsDependency struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	Summary string `json:"summary"`
}

// ProjectDocsContract describes an exposed interface or boundary contract of a component.
type ProjectDocsContract struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// ProjectDocsComponent is one part of the project: a directory of code and
// what is below it, as the project map groups it.
type ProjectDocsComponent struct {
	Name      string   `json:"name"`
	Layer     string   `json:"layer"`
	Path      string   `json:"path"`
	Languages []string `json:"languages"`
	Files     int      `json:"files"`
	Lines     int      `json:"lines"`
	// Summary and Responsibilities are model-written; SummarySource says
	// whether they come from the module's own summary ("module") or the
	// project overview ("overview"), and is empty when there are none.
	Summary          string   `json:"summary"`
	SummarySource    string   `json:"summary_source,omitempty"`
	Responsibilities []string `json:"responsibilities"`
	// Contracts are the component's entry points.
	Contracts []ProjectDocsContract `json:"contracts"`
	// Dependencies and UsedBy are the components this one imports and those
	// that import it, resolved from the code.
	Dependencies []string `json:"dependencies"`
	UsedBy       []string `json:"used_by"`
}

// ProjectDocsScenarioStep describes one step in a behavioral runtime scenario.
type ProjectDocsScenarioStep struct {
	Phase  string `json:"phase"`
	Layer  string `json:"layer"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Path   string `json:"path,omitempty"`
}

// ProjectDocsScenario describes an end-to-end runtime data flow and its invariant.
type ProjectDocsScenario struct {
	Title     string                    `json:"title"`
	Trigger   string                    `json:"trigger"`
	Invariant string                    `json:"invariant"`
	Summary   string                    `json:"summary"`
	Diagram   string                    `json:"diagram,omitempty"`
	Steps     []ProjectDocsScenarioStep `json:"steps"`
}

// ProjectDocsDecision records an Architectural Decision Record (ADR) and its tradeoff.
type ProjectDocsDecision struct {
	Title    string `json:"title"`
	Category string `json:"category"`
	Decision string `json:"decision"`
	Tradeoff string `json:"tradeoff"`
}

// ProjectDocsRunbookItem describes a standard operational command for the repository.
type ProjectDocsRunbookItem struct {
	Label       string `json:"label"`
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ProjectDocsGlossaryItem defines one ubiquitous domain term in the system.
type ProjectDocsGlossaryItem struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
}

// ProjectDocsReport is the language-agnostic C4/Arc42/ADR engineering specification
// generated during project analysis.
type ProjectDocsReport struct {
	SchemaVersion   string                    `json:"schema_version"`
	ProjectID       string                    `json:"project_id"`
	ProjectRevision string                    `json:"project_revision"`
	Status          string                    `json:"status"`
	Context         ProjectDocsContext        `json:"context"`
	Dependencies    []ProjectDocsDependency   `json:"dependencies,omitempty"`
	Components      []ProjectDocsComponent    `json:"components"`
	Scenarios       []ProjectDocsScenario     `json:"scenarios"`
	Decisions       []ProjectDocsDecision     `json:"decisions"`
	Runbook         []ProjectDocsRunbookItem  `json:"runbook"`
	Glossary        []ProjectDocsGlossaryItem `json:"glossary"`
	Graph           ProjectDocsGraph          `json:"graph"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

// ProjectDocsGraph is what the components diagram is drawn from: the
// imports between components and where the programs start.
type ProjectDocsGraph struct {
	Edges       []projectmap.Edge       `json:"edges"`
	EntryPoints []projectmap.EntryPoint `json:"entry_points"`
	Modules     int                     `json:"modules"`
	Truncated   bool                    `json:"truncated,omitempty"`
}

// DocsInputs are what the docs are built from.
type DocsInputs struct {
	Analysis Analysis
	Map      projectmap.Map
	// Profiles are how each subproject is tested and linted.
	Profiles []toolchain.Profile
	// Summaries are the model's module summaries, by component path.
	Summaries map[string]ModuleSummary
}

// ModuleSummary is a model's summary of one component, written from the
// summaries of its files.
type ModuleSummary struct {
	Path             string   `json:"path"`
	Summary          string   `json:"summary"`
	Responsibilities []string `json:"responsibilities"`
}

// BuildProjectDocs builds the project's docs: the parts and how they depend
// on each other from the code, and what they are for from the model's
// summaries when there are any.
func BuildProjectDocs(in DocsInputs) ProjectDocsReport {
	analysis := in.Analysis
	status := analysis.Report.Status
	if status == "" {
		status = ProjectAnalysisStatusUnavailable
	}
	components, edges := buildMapComponents(in)
	updatedAt := analysis.Report.GeneratedAt
	if updatedAt.IsZero() {
		updatedAt = analysis.AnalyzedAt
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return ProjectDocsReport{
		SchemaVersion:   projectDocsSchemaVersion,
		ProjectID:       analysis.ProjectID,
		ProjectRevision: analysis.ProjectRevision,
		Status:          status,
		Context:         buildDocsContext(analysis, components),
		Dependencies:    manifestDependencies(in.Map.Manifests),
		Components:      components,
		Scenarios:       buildDocsScenarios(analysis),
		Decisions:       buildDocsDecisions(analysis),
		Runbook:         buildRunbook(in.Profiles, in.Map.Manifests),
		Glossary:        buildDocsGlossary(analysis, components),
		Graph:           ProjectDocsGraph{Edges: edges, EntryPoints: in.Map.EntryPoints, Modules: len(in.Map.Modules), Truncated: in.Map.Truncated},
		UpdatedAt:       updatedAt,
	}
}

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

func parseReportComponent(raw string) ProjectDocsComponent {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ProjectDocsComponent{}
	}
	pathPart, rest := splitComponentHeader(trimmed)
	name, cleanPath := deriveComponentIdentity(pathPart)
	layer := classifyComponentLayer(cleanPath, name)
	summary := rest
	if summary == "" {
		summary = "No responsibility explanation was supplied."
	}
	summary = "Model interpretation: " + summary
	return ProjectDocsComponent{
		Name:             name,
		Layer:            layer,
		Path:             cleanPath,
		Summary:          summary,
		Responsibilities: []string{summary},
		Contracts:        []ProjectDocsContract{},
		Dependencies:     []string{},
	}
}

func splitComponentHeader(trimmed string) (string, string) {
	colonIdx := strings.Index(trimmed, ":")
	if colonIdx <= 0 {
		return trimmed, ""
	}
	return strings.TrimSpace(trimmed[:colonIdx]), strings.TrimSpace(trimmed[colonIdx+1:])
}

func deriveComponentIdentity(header string) (string, string) {
	if openIdx := strings.Index(header, "("); openIdx > 0 && strings.HasSuffix(header, ")") {
		cleanPath := strings.TrimSpace(header[:openIdx])
		label := strings.TrimSpace(header[openIdx+1 : len(header)-1])
		if label != "" {
			return label, cleanPath
		}
	}
	cleanPath := strings.Trim(header, "` ")
	base := filepath.Base(cleanPath)
	if base == "." || base == "/" || base == "" {
		base = cleanPath
	}
	return humanizeComponentName(base), cleanPath
}

func humanizeComponentName(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.'
	})
	if len(parts) == 0 {
		return "Core Subsystem"
	}
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func classifyComponentLayer(path, name string) string {
	lower := strings.ToLower(path + " " + name)
	switch {
	case containsAnyToken(lower, "desktop", "frontend", "ui", "view", "web", "client"):
		return "Presentation Layer"
	case containsAnyToken(lower, "api", "handler", "route", "http", "transport", "cmd", "daemon", "cli"):
		return "API / Transport Boundary"
	case containsAnyToken(lower, "app", "service", "workflow", "orchestrat", "job", "usecase"):
		return "Application / Orchestration"
	case containsAnyToken(lower, "script", "test", "eval", "bench", "ci", "quality"):
		return "Tooling & Verification"
	default:
		return "Domain & Persistence"
	}
}

func containsAnyToken(haystack string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			return true
		}
	}
	return false
}

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
