package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestTheDocsFollowTheCodeOnEveryRead(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	for name, content := range map[string]string{
		"go.mod":              "module example.test/fixture\n\ngo 1.22\n",
		"store/store.go":      "package store\n\nimport \"example.test/fixture/model\"\n\nvar Items []model.Item\n",
		"model/model.go":      "package model\n\ntype Item struct{}\n",
		"web/package.json":    `{"name": "web", "scripts": {"dev": "vite"}, "dependencies": {"react": "19"}}`,
		"web/src/main.ts":     "import { util } from './lib/util';\n",
		"web/src/lib/util.ts": "export const util = 1;\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	overview, err := service.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	docs := overview.Docs
	var paths []string
	for _, component := range docs.Components {
		paths = append(paths, component.Path)
	}
	if !slices.Equal(paths, []string{".", "model", "store", "web/src", "web/src/lib"}) {
		t.Fatalf("components = %v", paths)
	}
	if len(docs.Graph.Edges) != 2 || len(docs.Graph.EntryPoints) != 1 || len(docs.Dependencies) != 1 || docs.Dependencies[0].Name != "react" {
		t.Fatalf("graph %+v, dependencies %+v", docs.Graph, docs.Dependencies)
	}
	// A package added later is in the next read's docs.
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "api.go"), []byte("package api\n\nimport \"example.test/fixture/store\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	overview, err = service.ProjectOverview()
	if err != nil || len(overview.Docs.Components) != 6 || len(overview.Docs.Graph.Edges) != 3 {
		t.Fatalf("after a new package: %+v, %v", overview.Docs.Components, err)
	}
}
