package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectContextHonorsExplicitAnalysisExclusions(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"main.go": "package main\nfunc Run() {}\n", "excluded.go": "package main\n// private excluded content\n", "README.md": "Project description.\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	text, manifest, err := NewContextBuilder().BuildWithExcludedFiles(root, "", []string{"excluded.go"})
	if err != nil || strings.Contains(text, "excluded.go") || strings.Contains(text, "private excluded content") || !strings.Contains(text, "main.go") {
		t.Fatalf("excluded context=%s %v", text, err)
	}
	found := false
	for _, file := range manifest.Excluded {
		if file.Path == "excluded.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("exclusion missing from inspection metadata")
	}
	if _, _, err := NewContextBuilder().BuildWithExcludedFiles(root, "excluded.go", []string{"excluded.go"}); err == nil {
		t.Fatal("explicitly excluded target sent as context")
	}
}

func TestWorktreeSourcesCannotEnterContextThroughExplicitIncludes(t *testing.T) {
	root := t.TempDir()
	writeSourceWalkFixture(t, root, "main.go", "package main\n")
	writeSourceWalkFixture(t, root, ".worktrees/fix/duplicate.go", "package duplicate\n")
	writeSourceWalkFixture(t, root, ".kogin/context-policy.json", `{"include":["**"]}`)
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Decide(".worktrees/fix/duplicate.go").Include {
		t.Fatal("explicit include admitted a nested worktree")
	}
	text, _, err := NewContextBuilder().BuildWithExcludedFiles(root, "", nil)
	if err != nil || strings.Contains(text, "duplicate") {
		t.Fatalf("nested source reached context: %s, %v", text, err)
	}
}
