package handbook

import (
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project/insight"
	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
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
// project revision. Reports written by older prompts may carry other fields;
// they are not read any more.
type ProjectAnalysisReport struct {
	SchemaVersion   string `json:"schema_version"`
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	// Purpose and Architecture are short prose for a developer new to the
	// project. Reports from older prompts may hold a Mermaid diagram in
	// Architecture, which the docs leave out.
	Purpose      string `json:"purpose,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	// Components are "path (Name): responsibility" lines, used for a
	// component that has no summary of its own yet.
	Components []string `json:"components"`
	// Walkthroughs trace the main things the project does through its files.
	Walkthroughs       []Walkthrough         `json:"walkthroughs"`
	Risks              []ProjectAnalysisRisk `json:"risks"`
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

// Walkthrough is one thing the project does (a command, a request, a job),
// traced through the code by the overview's model: its steps in execution
// order, each at a file the project has.
type Walkthrough struct {
	Title   string            `json:"title"`
	Summary string            `json:"summary"`
	Steps   []WalkthroughStep `json:"steps"`
}

// WalkthroughStep is one step of a walkthrough. Symbol is the function,
// method or type the step happens in, and Line its declaration, both only
// when Kogin found that declaration in the file.
type WalkthroughStep struct {
	Text   string `json:"text"`
	Path   string `json:"path"`
	Symbol string `json:"symbol,omitempty"`
	Line   int    `json:"line,omitempty"`
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

// projectDocsSchemaVersion 4: an overview, components, walkthroughs,
// commands and dependencies; built on every read, never stored.
const projectDocsSchemaVersion = "4"

// ProjectDocsReport is the project's docs: what it is for and how its parts
// fit, from the overview's model; its components, commands, dependencies and
// the imports between components, from the code.
type ProjectDocsReport struct {
	SchemaVersion   string `json:"schema_version"`
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	// Status is the model-written parts' freshness: the overview's status.
	Status       string                  `json:"status"`
	Overview     ProjectDocsOverview     `json:"overview"`
	Components   []ProjectDocsComponent  `json:"components"`
	Walkthroughs []Walkthrough           `json:"walkthroughs"`
	Commands     []ProjectDocsCommand    `json:"commands"`
	Dependencies []ProjectDocsDependency `json:"dependencies"`
	Graph        ProjectDocsGraph        `json:"graph"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

// ProjectDocsOverview is the docs' first page. Purpose and Architecture are
// model-written and empty without an overview; the counts come from the index.
type ProjectDocsOverview struct {
	Purpose      string                `json:"purpose,omitempty"`
	Architecture string                `json:"architecture,omitempty"`
	Languages    []ProjectDocsLanguage `json:"languages"`
	Files        int                   `json:"files"`
	Lines        int                   `json:"lines"`
}

// ProjectDocsLanguage is a programming language and how many files use it.
type ProjectDocsLanguage struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
}

// ProjectDocsComponent is one part of the project: a directory of code and
// what is below it, as the project map groups it.
type ProjectDocsComponent struct {
	Name      string   `json:"name"`
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
	// EntryPoints are where programs in this component start.
	EntryPoints []projectmap.EntryPoint `json:"entry_points"`
	// Imports and UsedBy are the components this one imports and those that
	// import it, resolved from the code.
	Imports []string `json:"imports"`
	UsedBy  []string `json:"used_by"`
}

// ProjectDocsCommand is a command the project declares: a subproject's test
// or lint command, or a package script that builds or starts it.
type ProjectDocsCommand struct {
	Label   string `json:"label"`
	Command string `json:"command"`
	// Dir is where it runs, relative to the project root; empty is the root.
	Dir string `json:"dir,omitempty"`
	// Source is the file that declares it.
	Source string `json:"source"`
}

// ProjectDocsDependency is a dependency a manifest declares.
type ProjectDocsDependency struct {
	Name string `json:"name"`
	// Kind is the ecosystem, such as Go module or npm package.
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
	Dev     bool   `json:"dev,omitempty"`
	// Source is the manifest that declares it.
	Source string `json:"source"`
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
		Overview:        buildDocsOverview(analysis),
		Components:      components,
		Walkthroughs:    currentWalkthroughs(analysis),
		Commands:        buildCommands(in.Profiles, in.Map.Manifests),
		Dependencies:    manifestDependencies(in.Map.Manifests),
		Graph:           ProjectDocsGraph{Edges: edges, EntryPoints: in.Map.EntryPoints, Modules: len(in.Map.Modules), Truncated: in.Map.Truncated},
		UpdatedAt:       updatedAt,
	}
}
