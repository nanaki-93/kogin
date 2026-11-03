package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestChangeChecksEnforceArchitectureBoundariesAndComplexityGate(t *testing.T) {
	overlyComplexGo := "package api\n\nimport \"example.test/internal/storage\"\n\nfunc Handle(x int) int {\n\t_ = storage.Name\n\tif x == 1 {\n\t\treturn 1\n\t}\n\tif x == 2 {\n\t\treturn 2\n\t}\n\tif x == 3 {\n\t\treturn 3\n\t}\n\tif x == 4 {\n\t\treturn 4\n\t}\n\tif x == 5 {\n\t\treturn 5\n\t}\n\tif x == 6 {\n\t\treturn 6\n\t}\n\tif x == 7 {\n\t\treturn 7\n\t}\n\tif x == 8 {\n\t\treturn 8\n\t}\n\tif x == 9 {\n\t\treturn 9\n\t}\n\tif x == 10 {\n\t\treturn 10\n\t}\n\tif x == 11 {\n\t\treturn 11\n\t}\n\tif x == 12 {\n\t\treturn 12\n\t}\n\tif x == 13 {\n\t\treturn 13\n\t}\n\tif x == 14 {\n\t\treturn 14\n\t}\n\tif x == 15 {\n\t\treturn 15\n\t}\n\treturn 0\n}\n"
	var reply atomic.Value
	reply.Store(`{"explanation":"Violating candidate.","changes":[{"path":"internal/api/handler.go","content":` + strconv.Quote(overlyComplexGo) + `}]}`)
	server := changeProvider(t, func() string { return reply.Load().(string) })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Agent Contract\n- `internal/api/` must never import `internal/storage` or `internal/llm`.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "internal/api/handler.go")
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || requiredChecksPassed(checked.Checks) {
		t.Fatalf("expected architecture and complexity checks to fail, got %+v, err=%v", checked, err)
	}
	var archFailed, complexityFailed bool
	for _, check := range checked.Checks {
		if check.Name == "architecture boundaries" && check.State == CheckFailed && strings.Contains(check.Output, "internal/storage") {
			archFailed = true
		}
		if check.Name == "complexity gate" && check.State == CheckFailed && strings.Contains(check.Output, "cyclomatic complexity 16") {
			complexityFailed = true
		}
	}
	if !archFailed || !complexityFailed {
		t.Fatalf("expected both architecture boundaries and complexity gate to fail with diagnostics, got %+v", checked.Checks)
	}

	cleanGo := "package api\n\nfunc Handle(x int) int {\n\tif x == 1 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n"
	reply.Store(`{"explanation":"Clean candidate.","changes":[{"path":"internal/api/handler.go","content":` + strconv.Quote(cleanGo) + `}]}`)
	repaired, err := service.RepairChangeMessage(context.Background(), checked.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(checked),
		Repair:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	passed, err := service.CheckChange(context.Background(), repaired.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(repaired)})
	if err != nil || !requiredChecksPassed(passed.Checks) {
		t.Fatalf("expected clean candidate to pass architecture and complexity checks, got %+v, err=%v", passed, err)
	}
}

func TestChangeChecksEnforceSecretAndHygieneGate(t *testing.T) {
	leakedConfig := "# Config\n<<<<<<< HEAD\naws_id = \"AKIAIOSFODNN7EXAMPLE\"\n=======\n>>>>>>> main\n"
	var reply atomic.Value
	reply.Store(`{"explanation":"Add config.","changes":[{"path":"docs/config.md","content":` + strconv.Quote(leakedConfig) + `}]}`)
	server := changeProvider(t, func() string { return reply.Load().(string) })
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "docs/config.md")
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || requiredChecksPassed(checked.Checks) {
		t.Fatalf("expected secret & hygiene gate to fail on conflict marker and AWS key, got %+v, err=%v", checked, err)
	}
	var hygieneFailed bool
	for _, check := range checked.Checks {
		if check.Name == "secret & hygiene gate" && check.State == CheckFailed && strings.Contains(check.Output, "AWS access key ID") && strings.Contains(check.Output, "unresolved Git merge conflict marker") {
			hygieneFailed = true
		}
	}
	if !hygieneFailed {
		t.Fatalf("expected secret & hygiene gate diagnostics, got %+v", checked.Checks)
	}

	cleanConfig := "# Config\naws_id_env = \"AWS_ACCESS_KEY_ID\"\n"
	reply.Store(`{"explanation":"Use env var.","changes":[{"path":"docs/config.md","content":` + strconv.Quote(cleanConfig) + `}]}`)
	repaired, err := service.RepairChangeMessage(context.Background(), checked.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(checked),
		Message:        "Replace the AWS key literal with AWS_ACCESS_KEY_ID",
		Repair:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var foundGuidance bool
	for _, msg := range repaired.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "User repair guidance:\nReplace the AWS key literal with AWS_ACCESS_KEY_ID") {
			foundGuidance = true
		}
	}
	if !foundGuidance {
		t.Fatalf("expected RepairChangeMessage to preserve custom user repair guidance, got messages: %+v", repaired.Messages)
	}
	passed, err := service.CheckChange(context.Background(), repaired.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(repaired)})
	if err != nil || !requiredChecksPassed(passed.Checks) {
		t.Fatalf("expected clean candidate to pass secret & hygiene gate, got %+v, err=%v", passed, err)
	}
}

func TestPolyglotArchitectureBoundariesQualityBudgetsAndEcosystemRunners(t *testing.T) {
	violatingTS := "import { query } from '../db/store';\n\nexport function renderView(x: number): number {\n  if (x === 1) return 1;\n  if (x === 2) return 2;\n  if (x === 3) return 3;\n  if (x === 4) return 4;\n  if (x === 5) return 5;\n  if (x === 6) return 6;\n  return query(x);\n}\n"
	var reply atomic.Value
	reply.Store(`{"explanation":"Add view.","changes":[{"path":"src/ui/view.ts","summary":"Render view with db query.","content":` + strconv.Quote(violatingTS) + `}]}`)
	server := changeProvider(t, func() string { return reply.Load().(string) })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "ui"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "ui", "view.ts"), []byte("export function renderView() { return 0; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"polyglot-app","scripts":{"test":"node -e \"process.exit(0)\"","lint":"node -e \"process.exit(0)\""}}`), 0644); err != nil {
		t.Fatal(err)
	}
	agentsContract := "# Agent Contract\n## Architecture boundaries & ownership\n- `src/ui/` must never import `src/db/`.\n- Max function complexity: 5\n- Max function lines: 25\n- Max file lines: 200\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(agentsContract), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "polyglot-app", Path: root, Type: "typescript"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "")
	if err != nil || len(trust.Commands) != 2 || strings.Join(trust.Commands[0], " ") != "npm test" || strings.Join(trust.Commands[1], " ") != "npm run lint" {
		t.Fatalf("expected npm test and npm run lint in execution trust commands, got %+v, err=%v", trust, err)
	}

	session := prepareChangeFixture(t, service, "src/ui/view.ts")
	if len(session.Changes) != 1 || session.Changes[0].Summary != "Render view with db query." {
		t.Fatalf("expected per-file edit summary to be preserved, got %+v", session.Changes)
	}
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || requiredChecksPassed(checked.Checks) {
		t.Fatalf("expected the polyglot architecture check to fail, got %+v, err=%v", checked, err)
	}
	// TypeScript functions are measured one by one, like Go's.
	var archFailed, complexityMeasured, parsed bool
	for _, check := range checked.Checks {
		if check.Name == "architecture boundaries" && check.State == CheckFailed && strings.Contains(check.Output, "src/db") {
			archFailed = true
		}
		if check.Name == "complexity gate" && check.State == CheckFailed && strings.Contains(check.Output, "function renderView has cyclomatic complexity 7 (max 5, 1 before this task)") {
			complexityMeasured = true
		}
		if check.Name == "syntax: src/ui/view.ts" && check.State == CheckPassed && !check.Required {
			parsed = true
		}
	}
	if !archFailed || !complexityMeasured || !parsed {
		t.Fatalf("expected the polyglot boundary to fail and TypeScript to be parsed and measured, got %+v", checked.Checks)
	}
}

func TestAFileAlreadyOverBudgetMayChangeWithoutGrowingAndOpensOneIssue(t *testing.T) {
	legacy := "def a():\n    return 1\n\ndef b():\n    return 2\n"
	reply := `{"explanation":"Return 3 from b.","changes":[{"path":"legacy.py","content":` + strconv.Quote(strings.Replace(legacy, "return 2", "return 3", 1)) + `}]}`
	server := changeProvider(t, func() string { return reply })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "legacy.py"), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("```kogin\nbudgets:\n  file_lines: 3\n```\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "legacy", Path: root, Type: "python"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		session := prepareChangeFixture(t, service, "legacy.py")
		checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
		if err != nil || !requiredChecksPassed(checked.Checks) {
			t.Fatalf("checks = %+v, %v", checked, err)
		}
		rules, err := service.ChangeCodeRules(context.Background(), session.ID)
		if err != nil || len(rules.Files) != 1 || !rules.Files[0].Proposed || rules.Files[0].Budgets.FileLines != 3 || len(rules.Files[0].Debt) != 1 || rules.Files[0].Sources[0] != "AGENTS.md" {
			t.Fatalf("task rules = %+v, %v", rules, err)
		}
	}
	projectRules, err := service.ProjectCodeRules()
	if err != nil || !slices.Contains(projectRules.Guides, "AGENTS.md") || !slices.Contains(projectRules.TestPatterns, "test_*.py") || len(projectRules.Problems) != 0 {
		t.Fatalf("project rules = %+v, %v", projectRules, err)
	}
	findings, err := service.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var debt []project.UnifiedFinding
	for _, finding := range findings {
		if finding.Source == project.FindingSourceRules {
			debt = append(debt, finding)
		}
	}
	if len(debt) != 1 || debt[0].Location.Path != "legacy.py" || debt[0].Category != project.FindingCategoryMaintainability || !strings.Contains(debt[0].Evidence, "file has 5 lines (max 3)") {
		t.Fatalf("debt issues = %+v", debt)
	}
	draft, err := service.DraftIssueTask(debt[0].ID)
	if err != nil || draft.Kind != "fix" || draft.Title != debt[0].Title || !slices.Equal(draft.Paths, []string{"legacy.py", "test_legacy.py"}) ||
		!strings.Contains(draft.AcceptanceCriteria[0], "splitting it into cohesive sibling files") || len(draft.FindingTargets) != 1 || draft.FindingTargets[0].ID != debt[0].ID {
		t.Fatalf("refactor task draft = %+v, %v", draft, err)
	}
	if _, err := service.DraftIssueTask("finding:missing"); err == nil {
		t.Fatal("a missing issue was drafted")
	}
}
