package handbook

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
)

// mapped is a small project as the project map sees it: a command that
// uses a store, which uses a model package, and a web app.
func mapped() projectmap.Map {
	return projectmap.Map{
		Modules: []projectmap.Module{
			{Path: "cmd/app", Languages: []string{"Go"}, Files: 1, Lines: 20},
			{Path: "internal/model", Languages: []string{"Go"}, Files: 2, Lines: 80},
			{Path: "internal/store", Languages: []string{"Go"}, Files: 3, Lines: 200},
			{Path: "web/src", Languages: []string{"TypeScript"}, Files: 4, Lines: 300},
		},
		Edges: []projectmap.Edge{
			{From: "cmd/app", To: "internal/store", Imports: 1},
			{From: "internal/store", To: "internal/model", Imports: 2},
		},
		EntryPoints: []projectmap.EntryPoint{{Path: "cmd/app/main.go", Kind: "main", Detail: "Go"}, {Path: "web/package.json", Kind: "script", Detail: "dev: vite"}},
		Manifests: []projectmap.Manifest{
			{Path: "go.mod", Kind: "go", Name: "example.test/app", Dependencies: []projectmap.Dependency{{Name: "github.com/acme/log", Version: "v1.2.0"}}},
			{Path: "web/package.json", Kind: "node", Name: "web", Scripts: map[string]string{"dev": "vite", "test": "vitest"},
				Dependencies: []projectmap.Dependency{{Name: "react", Version: "^19.0.0"}, {Name: "vite", Version: "^6.0.0", Dev: true}}},
		},
	}
}

func TestDocsDescribeTheComponentsTheCodeDefines(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	analysis := Analysis{ProjectID: "p", ProjectRevision: "r", Name: "app", FileCount: 12, TotalLines: 600,
		Languages: map[string]int{"Go": 6, "TypeScript": 4, "Markdown": 2},
		Files:     []string{"cmd/app/main.go", "internal/store/store.go"},
		Report: ProjectAnalysisReport{Status: ProjectAnalysisStatusFresh, GeneratedAt: now, Purpose: "Keeps items.",
			Architecture: "cmd/app uses internal/store.", Components: []string{"internal/store (Store): Saves items.", "invented/dir (Ghost): Not in the code."},
			Walkthroughs: []Walkthrough{
				{Title: "Save an item", Summary: "The command saves one item.", Steps: []WalkthroughStep{
					{Text: "Parse the flags.", Path: "cmd/app/main.go", Symbol: "main", Line: 3},
					{Text: "A file that is gone.", Path: "internal/old/old.go"},
					{Text: "Write it.", Path: "internal/store/store.go"},
				}},
				{Title: "Gone", Summary: "Only gone files.", Steps: []WalkthroughStep{{Text: "x", Path: "internal/old/old.go"}}},
			}}}
	docs := BuildProjectDocs(DocsInputs{
		Analysis: analysis, Map: mapped(),
		Profiles:  []toolchain.Profile{{Dir: "", Kind: "go", Source: "go.mod", Test: []string{"go", "test", "./..."}, Lint: []string{"go", "vet", "./..."}}},
		Summaries: map[string]ModuleSummary{"internal/model": {Path: "internal/model", Summary: "Defines the item types.", Responsibilities: []string{"Item"}}},
	})
	if docs.SchemaVersion != "4" || docs.Status != ProjectAnalysisStatusFresh || !docs.UpdatedAt.Equal(now) {
		t.Fatalf("docs = %+v", docs)
	}
	wantOverview := ProjectDocsOverview{Purpose: "Keeps items.", Architecture: "cmd/app uses internal/store.", Languages: []ProjectDocsLanguage{{Name: "Go", Files: 6}, {Name: "TypeScript", Files: 4}}, Files: 12, Lines: 600}
	if !reflect.DeepEqual(docs.Overview, wantOverview) {
		t.Fatalf("overview = %+v", docs.Overview)
	}
	var paths []string
	byPath := map[string]ProjectDocsComponent{}
	for _, component := range docs.Components {
		paths = append(paths, component.Path)
		byPath[component.Path] = component
	}
	if !slices.Equal(paths, []string{"cmd/app", "internal/model", "internal/store", "web/src"}) {
		t.Fatalf("components = %v", paths)
	}
	store := byPath["internal/store"]
	if store.Name != "Store" || store.SummarySource != "overview" || store.Summary != "Saves items." || len(store.Responsibilities) != 0 || !slices.Equal(store.Imports, []string{"internal/model"}) || !slices.Equal(store.UsedBy, []string{"cmd/app"}) || store.Files != 3 {
		t.Fatalf("store = %+v", store)
	}
	if model := byPath["internal/model"]; model.SummarySource != "module" || model.Summary != "Defines the item types." || !slices.Equal(model.Responsibilities, []string{"Item"}) {
		t.Fatalf("model = %+v", model)
	}
	if web := byPath["web/src"]; web.SummarySource != "" || web.Summary != "" || len(web.Imports) != 0 {
		t.Fatalf("a component without summaries got one: %+v", web)
	}
	if app := byPath["cmd/app"]; len(app.EntryPoints) != 1 || app.EntryPoints[0].Path != "cmd/app/main.go" {
		t.Fatalf("entry points = %+v", app.EntryPoints)
	}
	if len(docs.Graph.Edges) != 2 || docs.Graph.Modules != 4 || len(docs.Graph.EntryPoints) != 2 {
		t.Fatalf("graph = %+v", docs.Graph)
	}
	wantWalkthroughs := []Walkthrough{{Title: "Save an item", Summary: "The command saves one item.", Steps: []WalkthroughStep{
		{Text: "Parse the flags.", Path: "cmd/app/main.go", Symbol: "main", Line: 3},
		{Text: "Write it.", Path: "internal/store/store.go"},
	}}}
	if !reflect.DeepEqual(docs.Walkthroughs, wantWalkthroughs) {
		t.Fatalf("walkthroughs = %+v", docs.Walkthroughs)
	}
}

func TestAComponentTakesOnlyTheOverviewLineForItsOwnPath(t *testing.T) {
	m := projectmap.Map{Modules: []projectmap.Module{{Path: ".", Languages: []string{"Go"}, Files: 1}, {Path: "internal/store", Languages: []string{"Go"}, Files: 1}}}
	analysis := Analysis{Name: "app", Report: ProjectAnalysisReport{Components: []string{"internal/store/sql (SQL): Writes rows.", "internal/store (Store): Saves items."}}}
	docs := BuildProjectDocs(DocsInputs{Analysis: analysis, Map: m})
	if root := docs.Components[0]; root.Path != "." || root.Summary != "" || root.SummarySource != "" {
		t.Fatalf("the root took a subdirectory's line: %+v", root)
	}
	if store := docs.Components[1]; store.Summary != "Saves items." {
		t.Fatalf("store = %+v", store)
	}
}

func TestDocsLeaveOutTheDiagramsOfOlderOverviews(t *testing.T) {
	docs := BuildProjectDocs(DocsInputs{Analysis: Analysis{Report: ProjectAnalysisReport{Status: ProjectAnalysisStatusStale, Purpose: "Keeps items.", Architecture: "flowchart TD\n A --> B"}}})
	if docs.Overview.Purpose != "Keeps items." || docs.Overview.Architecture != "" || docs.Status != ProjectAnalysisStatusStale {
		t.Fatalf("overview = %+v", docs.Overview)
	}
}

func TestDocsListWhatTheManifestsAndChecksDeclare(t *testing.T) {
	docs := BuildProjectDocs(DocsInputs{Map: mapped(), Profiles: []toolchain.Profile{
		{Dir: "", Kind: "go", Source: "go.mod", Test: []string{"go", "test", "./..."}},
		{Dir: "web", Kind: "node", Source: "web/package.json", Test: []string{"npm", "test"}, Lint: []string{"npm", "run", "lint"}},
	}})
	wantDependencies := []ProjectDocsDependency{
		{Name: "github.com/acme/log", Kind: "Go module", Version: "v1.2.0", Source: "go.mod"},
		{Name: "react", Kind: "npm package", Version: "^19.0.0", Source: "web/package.json"},
		{Name: "vite", Kind: "npm package", Version: "^6.0.0", Dev: true, Source: "web/package.json"},
	}
	if !reflect.DeepEqual(docs.Dependencies, wantDependencies) {
		t.Fatalf("dependencies = %+v", docs.Dependencies)
	}
	wantCommands := []ProjectDocsCommand{
		{Label: "Test", Command: "go test ./...", Source: "go.mod"},
		{Label: "Test", Command: "npm test", Dir: "web", Source: "web/package.json"},
		{Label: "Lint", Command: "npm run lint", Dir: "web", Source: "web/package.json"},
		{Label: "Dev", Command: "npm run dev", Dir: "web", Source: "web/package.json"},
	}
	if !reflect.DeepEqual(docs.Commands, wantCommands) {
		t.Fatalf("commands = %+v", docs.Commands)
	}
}

func TestDocsInventNothingWithoutAModelOrCode(t *testing.T) {
	docs := BuildProjectDocs(DocsInputs{Analysis: Analysis{ProjectID: "p", ProjectRevision: "r"}})
	if docs.Status != ProjectAnalysisStatusUnavailable || len(docs.Components) != 0 || len(docs.Walkthroughs) != 0 || len(docs.Dependencies) != 0 || len(docs.Commands) != 0 || docs.Overview.Purpose != "" {
		t.Fatalf("docs = %+v", docs)
	}
	data, err := json.Marshal(docs)
	if err != nil || !strings.Contains(string(data), `"walkthroughs":[]`) || !strings.Contains(string(data), `"commands":[]`) {
		t.Fatalf("empty lists must encode as lists: %s %v", data, err)
	}
}
