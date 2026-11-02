package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeTask opens a task that fixes an issue of main.go, in a module where
// main.go imports vault and nothing imports other, and returns a coding run.
func scopeTask(t *testing.T) (*ChangeSession, *taskToolRun) {
	t.Helper()
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	for name, content := range map[string]string{
		"go.mod":         "module example.test/scope\n\ngo 1.22\n",
		"main.go":        "package main\n\nimport \"example.test/scope/vault\"\n\nfunc main() { println(vault.Key()) }\n",
		"main_test.go":   "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n",
		"vault/vault.go": "package vault\n\n// Key derives the vault key.\nfunc Key() string { return \"k\" }\n",
		"vault/salt.go":  "package vault\n\nfunc salt() string { return \"s\" }\n",
		"other/other.go": "package other\n\nfunc Other() {}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	initializeChangeGit(t, service, root)
	trustedGitRequest(t, service)
	opened, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go", "main_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	session, err := child.ChangeSession(context.Background(), opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := child.newTaskToolRun(opened.WorktreePath, session, toolCoder)
	if err != nil {
		t.Fatal(err)
	}
	return session, run
}

func TestAFixWidensItsScopeToCodeItsCodeImports(t *testing.T) {
	session, run := scopeTask(t)
	refusedTool(t, run, "edit_file", map[string]any{"path": "vault/vault.go", "old_text": "\"k\"", "new_text": "\"key\""}, "ask for it with widen_scope")
	refusedTool(t, run, "widen_scope", map[string]any{"paths": []string{"other/other.go"}, "reason": "Other holds the key."}, "other/other.go is not in code this task's code imports (it imports vault)")
	mustTool(t, run, "widen_scope", map[string]any{"paths": []string{"vault/vault.go", "vault/vault_test.go"}, "reason": "The vault key is derived in vault."}, "Granted: you may now change vault/vault.go.")
	mustTool(t, run, "edit_file", map[string]any{"path": "vault/vault.go", "old_text": "\"k\"", "new_text": "\"key\""}, "Edited vault/vault.go")
	// A grant is the file asked for, not its whole folder.
	refusedTool(t, run, "edit_file", map[string]any{"path": "vault/salt.go", "old_text": "\"s\"", "new_text": "\"salt\""}, "outside this fix's scope")
	mustTool(t, run, "widen_scope", map[string]any{"paths": []string{"vault/vault.go"}, "reason": "Again."}, "already in this fix's scope")

	grants := run.grantsMade()
	if len(grants) != 1 || grants[0].Path != "vault/vault.go" || grants[0].Reason != "The vault key is derived in vault." {
		t.Fatalf("grants = %+v", grants)
	}
	stored := *session
	stored.ScopeGrants = withGrants(nil, grants)
	if err := toolProposalAllowed(&stored, toolCoder, []ChangeEdit{{Path: "vault/vault.go", Hash: "x"}}, run.resolver.IsTestFile); err != nil {
		t.Fatalf("a granted file was refused: %v", err)
	}
	if err := toolProposalAllowed(session, toolCoder, []ChangeEdit{{Path: "vault/vault.go", Hash: "x"}}, run.resolver.IsTestFile); err == nil {
		t.Fatal("a file was allowed without its grant")
	}
	if prompt := toolTaskPrompt(run.root, &stored, toolCoder, "Fix it.", ""); !strings.Contains(prompt, "change code only in the project's top folder, vault/vault.go") || !strings.Contains(prompt, "widen_scope") {
		t.Fatalf("prompt does not name the granted file:\n%s", prompt)
	}
	if err := validateSettledIssues(&stored); err != nil {
		t.Fatal(err)
	}
}

func TestAWidenedScopeStaysSmall(t *testing.T) {
	_, run := scopeTask(t)
	run.grants = []ScopeGrant{{Path: "a/a.go", Reason: "x"}, {Path: "b/b.go", Reason: "x"}}
	if err := run.withinScopeLimits([]string{"c/c.go"}); err == nil || !strings.Contains(err.Error(), "at most 2 folders beyond its own") {
		t.Fatalf("a third folder: %v", err)
	}
	run.grants = nil
	many := []string{"a/1.go", "a/2.go", "a/3.go", "a/4.go", "a/5.go", "a/6.go", "a/7.go", "a/8.go"}
	if err := run.withinScopeLimits(many); err == nil || !strings.Contains(err.Error(), "at most 8 code files") {
		t.Fatalf("too many files with main.go: %v", err)
	}
	if err := run.withinScopeLimits(many[:7]); err != nil {
		t.Fatal(err)
	}
	if kept := withoutStepGrants([]ScopeGrant{{Path: "a/1.go", Issue: "finding:a"}, {Path: "a/2.go", Issue: "finding:b"}, {Path: "a/3.go"}}, "finding:a"); len(kept) != 2 || kept[0].Path != "a/2.go" {
		t.Fatalf("rolled-back step's grants kept: %+v", kept)
	}
}
