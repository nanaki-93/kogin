package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func checkNamed(checks []CheckResult, name string) *CheckResult {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func toolchainService(t *testing.T, reply func() string, files map[string]string) (*Service, string) {
	t.Helper()
	server := changeProvider(t, reply)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	return service, root
}

func checkedWithTests(t *testing.T, service *Service, paths ...string) *ChangeSession {
	t.Helper()
	session := prepareChangeFixture(t, service, paths...)
	if _, err := service.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session), CheckOptions: CheckOptions{RunTests: true, RunLint: true}})
	if err != nil {
		t.Fatal(err)
	}
	return checked
}

const (
	addSource  = "package main\n\nfunc Add(a, b int) int { return a - b }\n"
	addTest    = "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 2) != 4 {\n\t\tt.Fatal(\"Add is wrong\")\n\t}\n}\n"
	brokenTest = "package main\n\nimport \"testing\"\n\nfunc TestBroken(t *testing.T) { t.Fatal(\"this test was already broken\") }\n"
)

func TestAnAlreadyFailingTestDoesNotBlockAnUnrelatedFix(t *testing.T) {
	fixed := `{"explanation":"Add adds.","changes":[{"path":"add.go","content":` + strconv.Quote(strings.Replace(addSource, "a - b", "a + b", 1)) + `}]}`
	service, _ := toolchainService(t, func() string { return fixed }, map[string]string{
		"go.mod": "module example.test/known\n\ngo 1.22\n", "add.go": addSource, "add_test.go": addTest, "broken_test.go": brokenTest,
	})
	checked := checkedWithTests(t, service, "add.go")
	tests := checkNamed(checked.Checks, "tests")
	if tests == nil || tests.State != CheckPassed || tests.Outcome != "known_failures" || !strings.Contains(tests.Output, "example.test/known.TestBroken") || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("checks = %+v", checked.Checks)
	}
}

func TestANewFailureStillBlocksAndIsNamed(t *testing.T) {
	broken := `{"explanation":"Rename the result.","changes":[{"path":"add.go","content":` + strconv.Quote(strings.Replace(addSource, "a - b", "a * b + 1", 1)) + `}]}`
	service, _ := toolchainService(t, func() string { return broken }, map[string]string{
		"go.mod": "module example.test/known\n\ngo 1.22\n", "add.go": strings.Replace(addSource, "a - b", "a + b", 1), "add_test.go": addTest, "broken_test.go": brokenTest,
	})
	checked := checkedWithTests(t, service, "add.go")
	tests := checkNamed(checked.Checks, "tests")
	if tests == nil || tests.State != CheckFailed || !strings.Contains(tests.Output, "New failures: example.test/known.TestAdd. Already failing before this change: example.test/known.TestBroken.") {
		t.Fatalf("checks = %+v", checked.Checks)
	}
	if !workflowChecksRepairable(checked.Checks) {
		t.Fatal("a new failure must stay repairable")
	}
}

func TestAMixedRepositoryRunsEveryTouchedToolchain(t *testing.T) {
	both := `{"explanation":"Change both sides.","changes":[{"path":"add.go","content":` + strconv.Quote(strings.Replace(addSource, "a - b", "a + b", 1)) + `},{"path":"web/app.ts","content":"export const answer = 42;\n"}]}`
	service, _ := toolchainService(t, func() string { return both }, map[string]string{
		"go.mod": "module example.test/mixed\n\ngo 1.22\n", "add.go": addSource, "add_test.go": addTest,
		"web/Makefile": "test:\n\t@echo web tests ran\nlint:\n\t@echo web lint ran\n", "web/app.ts": "export const answer = 41;\n",
		"tools/Makefile": "test:\n\t@false\n",
	})
	checked := checkedWithTests(t, service, "add.go", "web/app.ts")
	for _, name := range []string{"tests", "lint", "tests: web"} {
		if check := checkNamed(checked.Checks, name); check == nil || check.State != CheckPassed {
			t.Fatalf("%s did not pass: %+v", name, checked.Checks)
		}
	}
	if check := checkNamed(checked.Checks, "lint: web"); check != nil {
		t.Fatalf("a Makefile's lint target was run: %+v", check)
	}
	other := checkNamed(checked.Checks, "other subprojects")
	if other == nil || !strings.Contains(other.Output, "tools") || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("checks = %+v", checked.Checks)
	}
	trust, err := service.ExecutionTrust(context.Background(), checked.ProjectRevision, "")
	if err != nil || len(trust.Commands) != 4 || strings.Join(trust.Commands[2], " ") != "(in tools) make test" {
		t.Fatalf("trust commands = %+v, %v", trust, err)
	}
}
