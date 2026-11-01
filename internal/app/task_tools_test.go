package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const toolFixtureAgents = "# Rules\n\n```kasuri\nbudgets:\n  file_lines: 12\n```\n"

// toolTask opens a task in its own worktree in a small Go module and returns
// a tool run for it, with the worktree's service.
func toolTask(t *testing.T, role toolRole) (*Service, *ChangeSession, *taskToolRun) {
	t.Helper()
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	for name, content := range map[string]string{
		"go.mod":        "module example.test/tools\n\ngo 1.22\n",
		"AGENTS.md":     toolFixtureAgents,
		"calc.go":       "package main\n\n// Add adds.\nfunc Add(a, b int) int { return a - b }\n",
		"calc_test.go":  "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"Add\")\n\t}\n}\n",
		"docs/notes.md": "Add is used by main.\n",
		".env":          "SECRET=1\n",
		".kasuriignore": ".env\n",
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
	opened, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go", "calc.go"))
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
	run, err := child.newTaskToolRun(opened.WorktreePath, session, role)
	if err != nil {
		t.Fatal(err)
	}
	return child, session, run
}

func callTool(t *testing.T, run *taskToolRun, name string, arguments any) (string, error) {
	t.Helper()
	data, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range run.tools() {
		if tool.Name == name {
			return tool.Call(context.Background(), data)
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func mustTool(t *testing.T, run *taskToolRun, name string, arguments any, want ...string) string {
	t.Helper()
	text, err := callTool(t, run, name, arguments)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, part := range want {
		if !strings.Contains(text, part) {
			t.Fatalf("%s returned %q, want %q", name, text, part)
		}
	}
	return text
}

func refusedTool(t *testing.T, run *taskToolRun, name string, arguments any, want string) {
	t.Helper()
	if _, err := callTool(t, run, name, arguments); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s(%v) = %v, want an error with %q", name, arguments, err, want)
	}
}

func TestTheToolsReadTheTaskWorktreeWithinItsContextPolicy(t *testing.T) {
	_, _, run := toolTask(t, toolCoder)
	mustTool(t, run, "read_file", map[string]any{"path": "calc.go"}, "calc.go (lines 1-4 of 4)", "     4  func Add(a, b int) int { return a - b }")
	mustTool(t, run, "read_file", map[string]any{"path": "calc_test.go", "offset": 5, "limit": 2}, "lines 5-6 of 9", "more lines; read on with offset 7")
	listed := mustTool(t, run, "list_files", map[string]any{"pattern": "*.go"}, "calc.go", "calc_test.go")
	if strings.Contains(listed, ".env") || strings.Contains(listed, ".kasuri") {
		t.Fatalf("listed = %s", listed)
	}
	mustTool(t, run, "search", map[string]any{"query": "Add", "pattern": "*.md"}, "docs/notes.md:1: Add is used by main.")
	mustTool(t, run, "search", map[string]any{"query": `func \w+\(`, "regex": true}, "calc.go:4:", "calc_test.go:5:")
	mustTool(t, run, "outline", map[string]any{"path": "calc.go"}, "4 function Add: func Add(a, b int) int")
	mustTool(t, run, "find_definitions", map[string]any{"name": "Add"}, "calc.go:4 function: func Add(a, b int) int")
	mustTool(t, run, "find_references", map[string]any{"name": "Add"}, "calc_test.go:6:")

	refusedTool(t, run, "read_file", map[string]any{"path": ".env"}, "context policy")
	refusedTool(t, run, "read_file", map[string]any{"path": "../outside.go"}, "outside the project")
	refusedTool(t, run, "read_file", map[string]any{"path": ".git/config"}, "belongs to Git or Kasuri")
	refusedTool(t, run, "read_file", map[string]any{"path": "calc.go", "lines": 3}, "invalid arguments")
}

func TestTheToolsWriteThroughTheDiffPolicyAndReportTheGates(t *testing.T) {
	_, _, run := toolTask(t, toolCoder)
	refusedTool(t, run, "edit_file", map[string]any{"path": "calc.go", "old_text": "a * b", "new_text": "a + b"}, "does not appear")
	refusedTool(t, run, "edit_file", map[string]any{"path": "calc_test.go", "old_text": "\t", "new_text": "  "}, "appears 4 times")
	mustTool(t, run, "edit_file", map[string]any{"path": "calc.go", "old_text": "a - b", "new_text": "a + b"}, "Edited calc.go (1 replaced).")
	mustTool(t, run, "edit_file", map[string]any{"path": "calc.go", "old_text": "func Add", "new_text": "func  Add"}, "not gofmt-formatted")
	mustTool(t, run, "create_file", map[string]any{"path": "util/double.go", "content": strings.Repeat("// line\n", 13) + "package util\n"}, "Created util/double.go.", "Over budget: file has 14 lines (max 12).")
	refusedTool(t, run, "create_file", map[string]any{"path": "calc.go", "content": "package main\n"}, "exists; change it with edit_file")
	mustTool(t, run, "rename_file", map[string]any{"from": "docs/notes.md", "to": "docs/usage.md"}, "Moved docs/notes.md to docs/usage.md.")
	mustTool(t, run, "delete_file", map[string]any{"path": "docs/usage.md"}, "Deleted docs/usage.md.")

	refusedTool(t, run, "edit_file", map[string]any{"path": "AGENTS.md", "old_text": "12", "new_text": "999"}, "cannot change the rules it is checked against")
	refusedTool(t, run, "create_file", map[string]any{"path": ".kasuri/x.json", "content": "{}"}, "belongs to Git or Kasuri")
	refusedTool(t, run, "create_file", map[string]any{"path": ".env.local", "content": "X=1\n"}, "")
	for _, file := range []string{"calc.go", "util/double.go"} {
		if _, err := os.Stat(filepath.Join(run.root, file)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
}

func TestTheTestingAgentWritesOnlyTests(t *testing.T) {
	_, _, run := toolTask(t, toolTester)
	refusedTool(t, run, "edit_file", map[string]any{"path": "calc.go", "old_text": "a - b", "new_text": "a + b"}, "not a test file")
	mustTool(t, run, "create_file", map[string]any{"path": "calc_extra_test.go", "content": "package main\n\nimport \"testing\"\n\nfunc TestZero(t *testing.T) {\n\tif Add(0, 0) != 0 {\n\t\tt.Fatal(\"zero\")\n\t}\n}\n"}, "Created calc_extra_test.go.")
}

func TestAToolRunEndsAtFinishOrAtItsCallBudget(t *testing.T) {
	_, session, run := toolTask(t, toolCoder)
	run.limits.calls = 2
	mustTool(t, run, "outline", map[string]any{"path": "calc.go"})
	mustTool(t, run, "outline", map[string]any{"path": "calc.go"})
	refusedTool(t, run, "outline", map[string]any{"path": "calc.go"}, "used its 2 tool calls; call finish now")
	refusedTool(t, run, "finish", map[string]any{"summary": "Done.", "criteria": []map[string]any{{"criterion": 2, "how": "x"}}}, "number the task's 1 acceptance criteria")
	mustTool(t, run, "finish", map[string]any{"summary": "Nothing to change.", "criteria": []map[string]any{{"criterion": 1, "how": "Run already reports errors."}}}, "Recorded.")
	refusedTool(t, run, "read_file", map[string]any{"path": "calc.go"}, "this run is finished")
	if finish := run.finished(); finish == nil || finish.Criteria[0].How != "Run already reports errors." || len(session.AcceptanceCriteria) != 1 {
		t.Fatalf("finish = %+v", finish)
	}
	if calls, counts := run.usage(); calls != 4 || counts != "finish 2, outline 2" {
		t.Fatalf("usage = %d %q", calls, counts)
	}
}

func TestRunChecksChecksTheChangeAsTheWorktreeHoldsIt(t *testing.T) {
	_, _, run := toolTask(t, toolCoder)
	mustTool(t, run, "run_checks", map[string]any{}, "Nothing has changed yet")
	// TestAdd already fails at the base commit; TestMore is new.
	mustTool(t, run, "create_file", map[string]any{"path": "calc_more_test.go", "content": "package main\n\nimport \"testing\"\n\nfunc TestMore(t *testing.T) {\n\tif Add(1, 1) != 2 {\n\t\tt.Fatal(\"more\")\n\t}\n}\n"}, "Created")
	mustTool(t, run, "run_checks", map[string]any{}, "Some required checks fail", "- tests: FAILED", "New failures: example.test/tools.TestMore. Already failing before this change: example.test/tools.TestAdd.")
	mustTool(t, run, "edit_file", map[string]any{"path": "calc.go", "old_text": "a - b", "new_text": "a + b"}, "Edited")
	mustTool(t, run, "run_checks", map[string]any{}, "All required checks pass.", "- tests: PASSED")
}
