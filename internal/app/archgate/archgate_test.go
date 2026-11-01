package archgate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
)

func gateCheck(t *testing.T, checks []CheckResult) CheckResult {
	t.Helper()
	for _, check := range checks {
		if check.Name == "complexity gate" {
			return check
		}
	}
	t.Fatalf("no complexity gate in %+v", checks)
	return CheckResult{}
}

func writeAgents(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFileLineBudgetAppliesToFilesWithoutFunctions(t *testing.T) {
	root := t.TempDir()
	writeAgents(t, root, "# Agent Contract\n- `src/ui` must never import `src/db`.\n3. Max file lines: 5\n")
	checks, _ := ChangeArchitectureAndComplexityChecks(root, nil, []ChangeEdit{{
		Path:    "src/constants.ts",
		Content: "export const A = 1;\nexport const B = 2;\nexport const C = 3;\nexport const D = 4;\nexport const E = 5;\nexport const F = 6;\n",
	}})
	if gate := gateCheck(t, checks); gate.State != CheckFailed || !strings.Contains(gate.Output, "file has 6 lines (max 5)") {
		t.Fatalf("gate = %+v", gate)
	}
}

func TestFileLineCountIgnoresTheFinalNewline(t *testing.T) {
	for _, test := range []struct {
		content string
		lines   int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n", 1},
	} {
		if got := countLines(test.content); got != test.lines {
			t.Errorf("countLines(%q) = %d, want %d", test.content, got, test.lines)
		}
	}
	report := measureBudgets(rules.Budgets{FileLines: 2}, ChangeEdit{Path: "a.ts", Content: "a\nb\n"}, "", false)
	if len(report.Over) != 0 {
		t.Fatalf("a two-line file broke a two-line budget: %+v", report.Over)
	}
}

func TestTargetProjectsGetOnlyTheirOwnImportRules(t *testing.T) {
	root := t.TempDir()
	edits := []ChangeEdit{
		{Path: "internal/api/handler.go", Content: "package api\nimport _ \"example.test/target/internal/storage\"\n"},
		{Path: "internal/app/service.go", Content: "package app\nimport _ \"google.golang.org/adk/v2/runner\"\n"},
	}
	checks, _ := ChangeArchitectureAndComplexityChecks(root, nil, edits)
	for _, check := range checks {
		if check.Name == "architecture boundaries" && check.State != CheckPassed {
			t.Fatalf("a project without import rules got Kasuri's own: %+v", check)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("- `internal/api` must never import `internal/storage`.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	checks, _ = ChangeArchitectureAndComplexityChecks(root, nil, edits)
	if len(checks) == 0 || checks[0].State != CheckFailed || !strings.Contains(checks[0].Output, "internal/api must never import internal/storage") || strings.Contains(checks[0].Output, "adk") {
		t.Fatalf("declared rule = %+v", checks)
	}
}

func TestNonGoFunctionsAreMeasuredOneByOne(t *testing.T) {
	var ts strings.Builder
	for i := 0; i < 20; i++ {
		ts.WriteString("export function f" + string(rune('a'+i)) + "(x: number) { if (x > 0) { return 1; } return 0; }\n")
	}
	edits := []ChangeEdit{
		{Path: "src/many.ts", Content: ts.String()},
		{Path: "deploy/config.yaml", Content: "# if this case for while catch\nsteps:\n  - if: always\n  - for: each\n"},
		{Path: "src/Lib.hs", Content: "module Lib where\nf x = if x then 1 else 2\n"},
	}
	checks, _ := ChangeArchitectureAndComplexityChecks(t.TempDir(), nil, edits)
	gate := gateCheck(t, checks)
	if gate.State != CheckPassed || !strings.Contains(gate.Output, "not checked in src/Lib.hs") || strings.Contains(gate.Output, "src/many.ts") || strings.Contains(gate.Output, "deploy/config.yaml") {
		t.Fatalf("gate = %+v", gate)
	}

	branchy := "def pick(x):\n" + strings.Repeat("    if x:\n        return 1\n", 16) + "    return 0\n"
	checks, _ = ChangeArchitectureAndComplexityChecks(t.TempDir(), nil, []ChangeEdit{{Path: "app/pick.py", Content: branchy}})
	if gate := gateCheck(t, checks); gate.State != CheckFailed || !strings.Contains(gate.Output, "app/pick.py: line 1: function pick has cyclomatic complexity 17 (max 15)") {
		t.Fatalf("python gate = %+v", gate)
	}
}

func TestBudgetsRatchetOnWhatTheFileWasBeforeTheTask(t *testing.T) {
	root := t.TempDir()
	writeAgents(t, root, "```kasuri\nbudgets:\n  file_lines: 4\n  function_complexity: 3\n```\n")
	legacy := "def old(x):\n    if x and x > 1 or x < 0:\n        return 1\n    return 2\n\ndef small():\n    return 0\n"
	target := ChangeTarget{Path: "app/legacy.py", Exists: true, Content: legacy}

	// The task changes small() only: the file and old() were already over.
	untouched := strings.Replace(legacy, "return 0", "return 1", 1)
	checks, reports := ChangeArchitectureAndComplexityChecks(root, []ChangeTarget{target}, []ChangeEdit{{Path: target.Path, Content: untouched}})
	if gate := gateCheck(t, checks); gate.State != CheckPassed || !strings.Contains(gate.Output, "app/legacy.py: file has 7 lines (max 4)") || !strings.Contains(gate.Output, "function old has cyclomatic complexity 4 (max 3)") {
		t.Fatalf("debt gate = %+v", gate)
	}
	if len(reports) != 1 || len(reports[0].Debt) != 2 || len(reports[0].Over) != 0 {
		t.Fatalf("debt report = %+v", reports)
	}

	// Growing the file or the over-budget function fails.
	grown := legacy + "\ndef more():\n    return 3\n"
	worse := strings.Replace(legacy, "if x and", "if x and x != 5 and", 1)
	for name, content := range map[string]string{"file": grown, "function": worse} {
		checks, _ := ChangeArchitectureAndComplexityChecks(root, []ChangeTarget{target}, []ChangeEdit{{Path: target.Path, Content: content}})
		if gate := gateCheck(t, checks); gate.State != CheckFailed {
			t.Fatalf("%s grew and passed: %+v", name, gate)
		}
	}

	// A new file gets no allowance.
	checks, reports = ChangeArchitectureAndComplexityChecks(root, []ChangeTarget{{Path: "app/new.py"}}, []ChangeEdit{{Path: "app/new.py", Content: legacy}})
	if gate := gateCheck(t, checks); gate.State != CheckFailed || !reports[0].OverFileLines() {
		t.Fatalf("new file gate = %+v", gate)
	}
}

func TestRepositoryArchitectureBoundaries(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	var prodEdits []ChangeEdit
	for _, rootDir := range []string{"cmd", "internal"} {
		walkRoot := filepath.Join(repoRoot, rootDir)
		err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			prodEdits = append(prodEdits, ChangeEdit{
				Path:    filepath.ToSlash(rel),
				Content: string(content),
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(prodEdits) == 0 {
		t.Fatal("expected production Go files in repository")
	}
	resolver := rules.NewResolver(repoRoot)
	if own := resolver.For("internal/app/service.go"); len(own.Imports) < 15 || len(resolver.Problems()) > 0 {
		t.Fatalf("Kasuri's AGENTS.md rules = %+v, problems %+v", own, resolver.Problems())
	}
	checks, _ := ChangeArchitectureAndComplexityChecks(repoRoot, nil, prodEdits)
	for _, check := range checks {
		if check.State != CheckPassed {
			t.Fatalf("repository check %q failed:\n%s", check.Name, check.Output)
		}
	}
	for _, edit := range prodEdits {
		if violations := adkBoundaryViolations(edit.Path, extractEditImports(edit.Path, edit.Content, nil)); len(violations) > 0 {
			t.Fatalf("repository ADK boundary:\n%s", strings.Join(violations, "\n"))
		}
	}

	violationChecks, _ := ChangeArchitectureAndComplexityChecks(repoRoot, nil, []ChangeEdit{
		{
			Path:    "internal/project/diff/bad.go",
			Content: "package diff\nimport _ \"github.com/nanaki-93/mini-orca/v2/internal/project/symbols\"\n",
		},
		{
			Path:    "internal/app/gitops/bad.go",
			Content: "package gitops\nimport _ \"github.com/nanaki-93/mini-orca/v2/internal/app/archgate\"\n",
		},
	})
	if len(violationChecks) == 0 || violationChecks[0].State != CheckFailed {
		t.Fatalf("expected subpackage boundary violations to fail, got %+v", violationChecks)
	}
	for _, want := range []string{
		"internal/project/diff must never import internal/project/symbols",
		"internal/app/gitops must never import internal/app/archgate",
	} {
		if !strings.Contains(violationChecks[0].Output, want) {
			t.Fatalf("expected violation %q in output, got:\n%s", want, violationChecks[0].Output)
		}
	}
}

// adkBoundaryViolations checks Kasuri's own runtime boundary, which no
// target project shares: SDK types stay in internal/app/agentruntime, and that
// leaf never imports its parent internal/app (the independent agentstate store
// is allowed).
func adkBoundaryViolations(path string, imports []string) []string {
	if !pathMatchesPrefix(path, "internal") && !pathMatchesPrefix(path, "cmd") {
		return nil
	}
	isRuntime := pathMatchesPrefix(path, "internal/app/agentruntime")
	var violations []string
	for _, imported := range imports {
		if isRuntime && (imported == "internal/app" || strings.HasSuffix(imported, "/internal/app")) {
			violations = append(violations, path+" imports "+imported+": agentruntime must not import its app parent")
		}
		if !isRuntime && (importMatchesForbidden(imported, "google.golang.org/adk") || importMatchesForbidden(imported, "google.golang.org/genai")) {
			violations = append(violations, path+" imports "+imported+": ADK and GenAI imports belong in internal/app/agentruntime")
		}
	}
	return violations
}

func TestAgentRuntimeImportBoundary(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, imported string
		allowed        bool
	}{
		{"internal/app/agentruntime/model.go", "google.golang.org/adk/v2/model", true},
		{"internal/app/agentruntime/model.go", "google.golang.org/genai", true},
		{"internal/app/agentruntime/session.go", "github.com/nanaki-93/mini-orca/v2/internal/app/agentstate", true},
		{"internal/app/agentstate/store.go", "github.com/nanaki-93/mini-orca/v2/internal/storage", true},
		{"internal/app/agentstate/store.go", "github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime", false},
		{"internal/app/agentstate/store.go", "google.golang.org/adk/v2/session", false},
		{"internal/app/agentruntime/model.go", "github.com/nanaki-93/mini-orca/v2/internal/app", false},
		{"internal/app/agentruntime/model.go", "github.com/nanaki-93/mini-orca/v2/internal/project", false},
		{"internal/app/agentruntime/model.go", "github.com/nanaki-93/mini-orca/v2/internal/storage", false},
		{"internal/app/service.go", "google.golang.org/adk/v2/runner", false},
		{"internal/project/project_overview.go", "google.golang.org/adk/v2/model", false},
		{"internal/storage/file.go", "google.golang.org/adk/v2/session", false},
		{"internal/agenteval/eval.go", "google.golang.org/adk/v2/runner", false},
		{"internal/insighteval/eval.go", "google.golang.org/genai", false},
		{"cmd/daemon/main.go", "google.golang.org/adk/v2/runner", false},
		{"cmd/agent-eval/main.go", "google.golang.org/adk/v2/runner", false},
		{"cmd/agent-inspect/main.go", "google.golang.org/genai", false},
		{"main.go", "google.golang.org/adk/v2/runner", true},
	} {
		t.Run(test.path+"/"+test.imported, func(t *testing.T) {
			edit := ChangeEdit{Path: test.path, Content: "package fixture\nimport _ \"" + test.imported + "\"\n"}
			checks, _ := ChangeArchitectureAndComplexityChecks(repoRoot, nil, []ChangeEdit{edit})
			violations := adkBoundaryViolations(edit.Path, extractEditImports(edit.Path, edit.Content, nil))
			allowed := len(checks) > 0 && checks[0].State == CheckPassed && len(violations) == 0
			if allowed != test.allowed {
				t.Fatalf("allowed=%v, checks=%+v, ADK boundary=%v", test.allowed, checks, violations)
			}
		})
	}
}
