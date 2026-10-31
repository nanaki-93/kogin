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

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func prepareChangeFixture(t *testing.T, service *Service, paths ...string) *ChangeSession {
	t.Helper()
	session := openChangeFixture(t, service, paths...)
	proposal, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Implement the change."})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func approveChangeFixture(t *testing.T, service *Service, session *ChangeSession) *ChangeSession {
	t.Helper()
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("checks: %+v, %v", checked, err)
	}
	reviewed, err := service.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)})
	if err != nil {
		t.Fatal(err)
	}
	return reviewed
}

func TestChangeChecksRequireTrustAndProveNewTests(t *testing.T) {
	server := changeProvider(t, func() string {
		return `{"explanation":"Add helper and regression test.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 2 }\n"},{"path":"new_test.go","content":"package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 2 { t.Fatal(\"wrong result\") } }\n"}]}`
	})
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/change\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "new.go", "new_test.go")
	request := ChangeCheckRequest{ChangeIdentity: changeIdentity(session), CheckOptions: CheckOptions{RunTests: true}}
	if _, err := service.CheckChange(context.Background(), session.ID, request); err == nil {
		t.Fatal("checks ran without trust")
	}
	if _, err := service.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckChange(context.Background(), session.ID, request)
	if err != nil || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("trusted checks: %+v, %v", checked, err)
	}
	var baseline, tests bool
	for _, check := range checked.Checks {
		if check.Name == "regression baseline" {
			baseline = check.State == CheckUnavailable && check.Outcome == "could_not_execute" && !check.Required
		}
		if check.Name == "tests" {
			tests = check.State == CheckPassed
		}
	}
	if !baseline || !tests {
		t.Fatalf("missing baseline/candidate evidence: %+v", checked.Checks)
	}
	if _, err := os.Stat(filepath.Join(root, "new_test.go")); !os.IsNotExist(err) {
		t.Fatal("checks wrote project test")
	}
}

func TestGoRegressionBaselineRecordsANewTestThatAlreadyPasses(t *testing.T) {
	server := changeProvider(t, func() string {
		return `{"explanation":"Add a guard test.","changes":[{"path":"main.go","content":"package main\n\nimport \"fmt\"\n\nfunc Run() { fmt.Println(\"run\") }\n\nfunc Ready() bool { return true }\n"},{"path":"green_test.go","content":"package main\n\nimport \"testing\"\n\nfunc TestRunExists(t *testing.T) { Run() }\n"}]}`
	})
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/change\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "main.go", "green_test.go")
	if _, err := service.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session), CheckOptions: CheckOptions{RunTests: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checked.Checks {
		if check.Name == "regression baseline" {
			if check.State != CheckSkipped || check.Outcome != "already_passes" || check.Required {
				t.Fatalf("a new test that passes before the change = %+v", check)
			}
			return
		}
	}
	t.Fatalf("no regression baseline: %+v", checked.Checks)
}

func TestChangeFailedChecksRepairAndApprovalInvalidation(t *testing.T) {
	var reply atomic.Value
	reply.Store(`{"explanation":"Incomplete candidate.","changes":[{"path":"new.go","content":"package main\nfunc Broken( {\n"}]}`)
	server := changeProvider(t, func() string { return reply.Load().(string) })
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "new.go")
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || requiredChecksPassed(checked.Checks) {
		t.Fatalf("failed checks lost: %+v, %v", checked, err)
	}
	if _, err := service.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)}); err == nil {
		t.Fatal("failed checks authorized review")
	}
	reply.Store(`{"explanation":"Complete candidate.","changes":[{"path":"new.go","content":"package main\nfunc Ready() {}\n"}]}`)
	repaired, err := service.RepairChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(checked), Repair: true})
	if err != nil || repaired.RepairAttempts != 1 || len(repaired.Checks) != 0 || repaired.ReviewedHash != "" {
		t.Fatalf("repair: %+v, %v", repaired, err)
	}
	approveChangeFixture(t, service, repaired)
}

func TestReviewChangeAcrossRestartAndDirectHunkEdits(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "main.go", "docs/change.md")
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("checks: %+v, %v", checked, err)
	}

	restarted, err := New(scopedTestConfig(server.URL), service.manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ApplyChange(context.Background(), checked.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(checked), Confirm: true}); err == nil {
		t.Fatal("apply succeeded without review after restart")
	}
	reviewed, err := restarted.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)})
	if err != nil || reviewed.ReviewedHash != checked.Hash {
		t.Fatalf("review after restart failed: %+v, %v", reviewed, err)
	}
	reviewedAgain, err := restarted.ReviewChange(context.Background(), reviewed.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(reviewed)})
	if err != nil || reviewedAgain.ReviewedHash != reviewed.Hash {
		t.Fatalf("repeat review failed: %+v, %v", reviewedAgain, err)
	}
}

func TestNonGoJustfileChecksAndTestsExecuteUnderTrust(t *testing.T) {
	binDir := t.TempDir()
	fakeJust := filepath.Join(binDir, "just")
	script := "#!/bin/sh\nif [ \"$1\" = \"test\" ]; then\n  grep -q \"return 42\" app.ts || { echo \"FAIL: expected 42 in app.ts\"; exit 1; }\n  echo \"PASS: app.test.ts\"\n  exit 0\nfi\nif [ \"$1\" = \"check\" ]; then\n  echo \"check ok\"\n  exit 0\nfi\nexit 1\n"
	if err := os.WriteFile(fakeJust, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var reply atomic.Value
	reply.Store(`{"explanation":"Initial implementation.","changes":[{"path":"app.ts","content":"export function run() { return 0; }\n"},{"path":"app.test.ts","content":"import { run } from './app';\n"}]}`)
	server := changeProvider(t, func() string { return reply.Load().(string) })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"ts-just"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "justfile"), []byte("setup:\n  npm install\ndev:\n  npm run dev\ntest:\n  npm test\ncheck:\n  npm run check\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("export function run() { return 0; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "ts-just", Path: root, Type: "typescript"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.CancelChangeWorkflows)
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "")
	if err != nil || len(trust.Commands) != 2 || strings.Join(trust.Commands[0], " ") != "just test" || strings.Join(trust.Commands[1], " ") != "just check" {
		t.Fatalf("expected just test and just check in execution trust commands, got %+v, err=%v", trust, err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), analysis.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "app.ts", "app.test.ts")
	failedCheck, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(session),
		CheckOptions:   CheckOptions{RunTests: true, RunLint: true},
	})
	if err != nil || requiredChecksPassed(failedCheck.Checks) {
		t.Fatalf("expected just test to fail when app.ts returns 0, got %+v, err=%v", failedCheck, err)
	}
	reply.Store(`{"explanation":"Return 42.","changes":[{"path":"app.ts","content":"export function run() { return 42; }\n"},{"path":"app.test.ts","content":"import { run } from './app';\n"}]}`)
	repaired, err := service.RepairChangeMessage(context.Background(), failedCheck.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(failedCheck),
		Repair:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	passedCheck, err := service.CheckChange(context.Background(), repaired.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(repaired),
		CheckOptions:   CheckOptions{RunTests: true, RunLint: true},
	})
	if err != nil || !requiredChecksPassed(passedCheck.Checks) {
		t.Fatalf("expected just test and just check to pass after repair, got %+v, err=%v", passedCheck, err)
	}
}

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
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"polyglot-app","scripts":{"test":"node -e \"process.exit(0)\"","check":"node -e \"process.exit(0)\""}}`), 0644); err != nil {
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
	if err != nil || len(trust.Commands) != 2 || strings.Join(trust.Commands[0], " ") != "npm test" || strings.Join(trust.Commands[1], " ") != "npm run check" {
		t.Fatalf("expected npm test and npm run check in execution trust commands, got %+v, err=%v", trust, err)
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

func TestNonGoRequiredTestsWithoutRunnerAreUnavailableAndBlockVerification(t *testing.T) {
	pyReply := `{"explanation":"Broken Python module.","changes":[{"path":"app.py","content":"def broken(:\n    pass\n"},{"path":"test_app.py","content":"def test_always_fails():\n    assert False\n"}]}`
	server := changeProvider(t, func() string { return pyReply })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("def broken():\n    return 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "py-no-runner", Path: root, Type: "python"}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "app.py", "test_app.py")
	if _, err := service.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(session),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil {
		t.Fatalf("CheckChange returned unexpected error: %v", err)
	}
	if requiredChecksPassed(checked.Checks) {
		t.Fatalf("expected required checks not to pass without an executable test runner, got %+v", checked.Checks)
	}
	var testsUnavailable bool
	for _, chk := range checked.Checks {
		if chk.Name == "tests" && chk.Required && chk.State == CheckUnavailable {
			testsUnavailable = true
		}
	}
	if !testsUnavailable {
		t.Fatalf("expected the tests check to be unavailable, got %+v", checked.Checks)
	}
	if _, err := service.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)}); err == nil {
		t.Fatalf("expected ReviewChange to fail when required tests did not execute")
	}
}

func TestRerunningChecksPreservesCriticRejectionAndBlocksApproval(t *testing.T) {
	s, _, session := workflowService(t, [3]func() string{
		func() string { return workflowImplementation },
		func() string { return workflowTests },
		func() string {
			return `{"verdict":"changes_requested","summary":"Missing edge-case guard.","findings":["Handle zero input explicitly"]}`
		},
	})
	req := workflowRequest(session)
	req.AutoRepair = false
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	rejected := finishedWorkflow(t, s, session.ID)
	if rejected.Workflow == nil || rejected.Workflow.Status != "changes_requested" || rejected.Workflow.Review == nil || rejected.Workflow.Review.Verdict != "changes_requested" {
		t.Fatalf("expected critic changes_requested verdict, got %+v", rejected.Workflow)
	}
	checked, err := s.CheckChange(context.Background(), rejected.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(rejected),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil {
		t.Fatalf("CheckChange after critic rejection failed: %v", err)
	}
	if checked.Workflow.Status != "changes_requested" || checked.Workflow.Review == nil || checked.Workflow.Review.Verdict != "changes_requested" || len(checked.Workflow.Review.Findings) != 1 {
		t.Fatalf("expected CheckChange to preserve critic rejection and findings, got %+v", checked.Workflow)
	}
	if _, err := s.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)}); err == nil {
		t.Fatalf("expected ReviewChange to remain blocked after rerunning deterministic checks on a critic-rejected proposal")
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
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("```mini-orca\nbudgets:\n  file_lines: 3\n```\n"), 0644); err != nil {
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
