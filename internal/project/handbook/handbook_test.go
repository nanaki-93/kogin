package handbook

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/projectmap"
	"github.com/nanaki-93/mini-orca/v2/internal/project/toolchain"
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
	analysis := Analysis{ProjectID: "p", ProjectRevision: "r", Name: "app", Languages: map[string]int{"Go": 6, "TypeScript": 4},
		Report: ProjectAnalysisReport{Status: ProjectAnalysisStatusFresh, GeneratedAt: now, Purpose: "Keeps items.",
			Architecture: "flowchart TD\n A --> B", Components: []string{"internal/store (Store): Saves items.", "invented/dir (Ghost): Not in the code."}}}
	docs := BuildProjectDocs(DocsInputs{
		Analysis: analysis, Map: mapped(),
		Profiles:  []toolchain.Profile{{Dir: "", Kind: "go", Source: "go.mod", Test: []string{"go", "test", "./..."}, Lint: []string{"go", "vet", "./..."}}},
		Summaries: map[string]ModuleSummary{"internal/model": {Path: "internal/model", Summary: "Defines the item types.", Responsibilities: []string{"Item"}}},
	})
	if docs.SchemaVersion != "3" || docs.Status != ProjectAnalysisStatusFresh || !docs.UpdatedAt.Equal(now) {
		t.Fatalf("docs = %+v", docs)
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
	if store.Name != "Store" || store.SummarySource != "overview" || !strings.Contains(store.Summary, "Saves items.") || !slices.Equal(store.Dependencies, []string{"internal/model"}) || !slices.Equal(store.UsedBy, []string{"cmd/app"}) || store.Files != 3 {
		t.Fatalf("store = %+v", store)
	}
	if model := byPath["internal/model"]; model.SummarySource != "module" || model.Summary != "Defines the item types." {
		t.Fatalf("model = %+v", model)
	}
	if web := byPath["web/src"]; web.SummarySource != "" || web.Summary != "" || len(web.Dependencies) != 0 {
		t.Fatalf("a component without summaries got one: %+v", web)
	}
	if app := byPath["cmd/app"]; len(app.Contracts) != 1 || app.Contracts[0].Name != "cmd/app/main.go" {
		t.Fatalf("entry points = %+v", app.Contracts)
	}
	if len(docs.Graph.Edges) != 2 || docs.Graph.Modules != 4 || len(docs.Graph.EntryPoints) != 2 {
		t.Fatalf("graph = %+v", docs.Graph)
	}
	if strings.Contains(docs.Context.Topology, "flowchart") || !strings.Contains(docs.Context.Topology, "4 components") {
		t.Fatalf("topology = %q", docs.Context.Topology)
	}
}

func TestDocsListWhatTheManifestsAndChecksDeclare(t *testing.T) {
	docs := BuildProjectDocs(DocsInputs{Map: mapped(), Profiles: []toolchain.Profile{
		{Dir: "", Kind: "go", Source: "go.mod", Test: []string{"go", "test", "./..."}},
		{Dir: "web", Kind: "node", Source: "web/package.json", Test: []string{"npm", "test"}, Lint: []string{"npm", "run", "lint"}},
	}})
	var dependencies []string
	for _, dependency := range docs.Dependencies {
		dependencies = append(dependencies, dependency.Kind+" "+dependency.Name+" "+dependency.Version)
	}
	if !slices.Equal(dependencies, []string{"Go module github.com/acme/log v1.2.0", "npm package react ^19.0.0", "npm package (development) vite ^6.0.0"}) {
		t.Fatalf("dependencies = %v", dependencies)
	}
	var runbook []string
	for _, item := range docs.Runbook {
		runbook = append(runbook, item.Label+": "+item.Command)
	}
	if !slices.Equal(runbook, []string{"Test: go test ./...", "Test (web): npm test", "Lint (web): npm run lint", "Run dev (web): npm run dev"}) {
		t.Fatalf("runbook = %v", runbook)
	}
}

func TestDocsInventNothingWithoutAModelOrCode(t *testing.T) {
	docs := BuildProjectDocs(DocsInputs{Analysis: Analysis{ProjectID: "p", ProjectRevision: "r"}})
	if docs.Status != ProjectAnalysisStatusUnavailable || len(docs.Components) != 0 || len(docs.Scenarios) != 0 || len(docs.Decisions) != 0 || len(docs.Dependencies) != 0 || len(docs.Runbook) != 0 {
		t.Fatalf("docs = %+v", docs)
	}
	data, err := json.Marshal(docs)
	if err != nil || strings.Contains(string(data), "validates inputs") || strings.Contains(string(data), "verified state") {
		t.Fatalf("unsupported guarantees: %s %v", data, err)
	}
}
