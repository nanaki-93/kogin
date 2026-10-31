package handbook

import (
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
	"github.com/nanaki-93/mini-orca/v2/internal/project/projectmap"
	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
	"github.com/nanaki-93/mini-orca/v2/internal/project/toolchain"
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
