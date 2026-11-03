package projectmap

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

// polyglot is a project in five languages, with the imports, entry points
// and manifests each one declares in its own way.
var polyglot = map[string]string{
	"go.mod":                               "module example.test/app\n\ngo 1.22\n\nrequire (\n\tgithub.com/acme/log v1.2.0\n\tgolang.org/x/sync v0.7.0 // indirect\n)\n",
	"cmd/app/main.go":                      "package main\n\nimport (\n\t\"fmt\"\n\t\"example.test/app/internal/store\"\n)\n\nfunc main() { fmt.Println(store.Open()) }\n",
	"internal/store/store.go":              "package store\n\nimport \"example.test/app/internal/model\"\n\nfunc Open() model.Item { return model.Item{} }\n",
	"internal/store/store_test.go":         "package store\n\nfunc main() {}\n",
	"internal/model/model.go":              "package model\n\ntype Item struct{}\n",
	"web/package.json":                     `{"name": "web", "main": "src/main.tsx", "scripts": {"dev": "vite", "test": "vitest"}, "dependencies": {"react": "^19.0.0"}, "devDependencies": {"vite": "^6.0.0"}}`,
	"web/tsconfig.json":                    "{\n  // Path aliases.\n  \"compilerOptions\": {\"baseUrl\": \".\", \"paths\": {\"@/*\": [\"./src/*\"]}}\n}\n",
	"web/src/main.tsx":                     "import { App } from './app';\nimport { util } from '@/lib/util';\nimport React from 'react';\n",
	"web/src/app.tsx":                      "export const App = 1;\n",
	"web/src/lib/util.ts":                  "export const util = 1;\n",
	"py/pyproject.toml":                    "[project]\nname = \"tool\"\ndependencies = [\n  \"requests>=2\",\n  \"rich\",\n]\n",
	"py/tool/__init__.py":                  "",
	"py/tool/core.py":                      "def run():\n    pass\n",
	"py/tool/cli/main.py":                  "from tool.core import run\nimport os\n\nif __name__ == \"__main__\":\n    run()\n",
	"java/src/main/java/com/acme/App.java": "package com.acme;\n\nimport com.acme.billing.Invoice;\n\npublic class App {\n  public static void main(String[] args) {}\n}\n",
	"java/src/main/java/com/acme/billing/Invoice.java": "package com.acme.billing;\n\npublic class Invoice {}\n",
	"rs/Cargo.toml":      "[package]\nname = \"rs\"\n\n[dependencies]\nserde = \"1\"\ntokio = { version = \"1.40\", features = [\"full\"] }\n",
	"rs/src/main.rs":     "use crate::util::helper;\n\nfn main() { helper(); }\n",
	"rs/src/util/mod.rs": "pub fn helper() {}\n",
	"docs/guide.md":      "# Guide\n",
}

func writePolyglot(t *testing.T) (string, []symbols.IndexFile) {
	t.Helper()
	root := t.TempDir()
	var files []symbols.IndexFile
	for name, content := range polyglot {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" {
			continue // The index leaves out files without a known extension.
		}
		language, imports := symbols.ExtractFileFacts(name, []byte(content))
		files = append(files, symbols.IndexFile{Path: name, Language: language, Imports: imports, LineCount: strings.Count(content, "\n"), SizeBytes: int64(len(content))})
	}
	return root, files
}

func TestAProjectMapResolvesImportsInEachLanguage(t *testing.T) {
	root, files := writePolyglot(t)
	m := Build(root, files)
	var modules []string
	for _, module := range m.Modules {
		modules = append(modules, module.Path)
	}
	want := []string{"cmd/app", "internal/model", "internal/store", "java/src/main/java/com/acme", "java/src/main/java/com/acme/billing", "py/tool", "py/tool/cli", "rs/src", "rs/src/util", "web/src", "web/src/lib"}
	if !slices.Equal(modules, want) {
		t.Fatalf("modules = %v", modules)
	}
	var edges []string
	for _, edge := range m.Edges {
		edges = append(edges, edge.From+" -> "+edge.To)
	}
	wantEdges := []string{"cmd/app -> internal/store", "internal/store -> internal/model", "java/src/main/java/com/acme -> java/src/main/java/com/acme/billing", "py/tool/cli -> py/tool", "rs/src -> rs/src/util", "web/src -> web/src/lib"}
	if !slices.Equal(edges, wantEdges) {
		t.Fatalf("edges = %v", edges)
	}
}

func TestAProjectMapFindsEntryPointsAndManifests(t *testing.T) {
	root, files := writePolyglot(t)
	m := Build(root, files)
	var entries []string
	for _, entry := range m.EntryPoints {
		entries = append(entries, entry.Path+" "+entry.Kind+" "+entry.Detail)
	}
	want := []string{"cmd/app/main.go main Go", "java/src/main/java/com/acme/App.java main Java", "py/tool/cli/main.py main Python", "rs/src/main.rs main Rust", "web/package.json script dev: vite", "web/src/main.tsx package main "}
	if !slices.Equal(entries, want) {
		t.Fatalf("entry points = %q", entries)
	}
	byPath := map[string]Manifest{}
	for _, manifest := range m.Manifests {
		byPath[manifest.Path] = manifest
	}
	goMod := byPath["go.mod"]
	if goMod.Kind != "go" || goMod.Name != "example.test/app" || len(goMod.Dependencies) != 1 || goMod.Dependencies[0] != (Dependency{Name: "github.com/acme/log", Version: "v1.2.0"}) {
		t.Fatalf("go.mod = %+v", goMod)
	}
	if web := byPath["web/package.json"]; web.Name != "web" || len(web.Dependencies) != 2 || !web.Dependencies[1].Dev || web.Scripts["test"] != "vitest" {
		t.Fatalf("package.json = %+v", web)
	}
	if py := byPath["py/pyproject.toml"]; py.Name != "tool" || len(py.Dependencies) != 2 || py.Dependencies[0].Name != "requests" || py.Dependencies[0].Version != ">=2" {
		t.Fatalf("pyproject.toml = %+v", py)
	}
	if rs := byPath["rs/Cargo.toml"]; rs.Name != "rs" || len(rs.Dependencies) != 2 || rs.Dependencies[1] != (Dependency{Name: "tokio", Version: "1.40"}) {
		t.Fatalf("Cargo.toml = %+v", rs)
	}
}

func TestComponentsFoldTheDeepestFoldersFirst(t *testing.T) {
	root, files := writePolyglot(t)
	m := Build(root, files)
	components, edges := m.Components(6)
	var paths []string
	for _, component := range components {
		paths = append(paths, component.Path)
	}
	if !slices.Equal(paths, []string{"cmd/app", "internal", "java/src/main/java/com/acme", "py/tool", "rs/src", "web/src"}) {
		t.Fatalf("components = %v", paths)
	}
	if internal := components[1]; !slices.Equal(internal.Modules, []string{"internal/model", "internal/store"}) || internal.Files != 3 {
		t.Fatalf("internal = %+v", internal)
	}
	if len(edges) != 1 || edges[0] != (Edge{From: "cmd/app", To: "internal", Imports: 1}) {
		t.Fatalf("component edges = %+v", edges)
	}
	all, _ := m.Components(100)
	if len(all) != len(m.Modules) {
		t.Fatalf("a generous limit folded modules: %d of %d", len(all), len(m.Modules))
	}
	if one, _ := m.Components(1); len(one) != 1 || one[0].Path != "." || one[0].Files != len(files)-5 { // Four manifests and a guide hold no code.
		t.Fatalf("one component = %+v", one)
	}
}
