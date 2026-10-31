package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

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
