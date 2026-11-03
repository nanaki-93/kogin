package app

import (
	"strings"
	"testing"
)

func TestARepairReadsWhatFailedNotTheProgramsOutput(t *testing.T) {
	prompts := strings.Repeat("Enter your new password: password must be at least 8 characters long\n", 60)
	output := "=== RUN   TestRotate\n--- FAIL: TestRotate (0.01s)\n    master_test.go:41: rotation kept the old salt\n" + prompts +
		"FAIL\nFAIL\texample.test/pwd/cmd\t50.4s\nok  \texample.test/pwd/utils\t5.5s\nFAIL\nerror: recipe `test` failed on line 18 with exit code 1"
	excerpt := checkFailureExcerpt(output, 1500)
	if len(excerpt) > 1600 || !strings.HasPrefix(excerpt, "Lines that say what failed:\n--- FAIL: TestRotate (0.01s)\n    master_test.go:41: rotation kept the old salt\n…\nEnd of the output:\n") || !strings.HasSuffix(excerpt, "exit code 1") {
		t.Fatalf("excerpt (%d bytes):\n%s", len(excerpt), excerpt)
	}
	if short := checkFailureExcerpt("--- FAIL: TestA (0.00s)\nFAIL\texample.test/a\t0.1s", 1500); short != "--- FAIL: TestA (0.00s)\nFAIL\texample.test/a\t0.1s" {
		t.Fatalf("a short output was changed:\n%s", short)
	}
	// A test binary that exits mid-test reports no failing test: say so.
	exited := checkFailureExcerpt(prompts+"save vault: refusing to access non-regular file: /tmp/TestSaveAborts1/001/.pwd\nFAIL\nFAIL\texample.test/pwd/cmd\t50.4s\nFAIL", 1500)
	if !strings.HasPrefix(exited, "Note: the tests of example.test/pwd/cmd exited before any test reported a failure") || !strings.Contains(exited, "TestSaveAborts1") {
		t.Fatalf("excerpt of an exited test binary:\n%s", exited)
	}

	checks := []CheckResult{
		{Name: "tests", Required: true, State: CheckFailed, Output: output},
		{Name: "lint", Required: true, State: CheckPassed},
		{Name: "regression baseline", Required: false, State: CheckUnavailable, Outcome: "could_not_execute", Output: "cmd/a_test.go:4:2: undefined: rotate\nFAIL\texample.test/pwd/cmd [build failed]"},
	}
	evidence := failedWorkflowCheckEvidence(checks)
	if !strings.HasPrefix(evidence, "tests (failed):\nLines that say what failed:") || strings.Contains(evidence, "regression baseline") || strings.Contains(evidence, "undefined: rotate") {
		t.Fatalf("repair evidence:\n%s", evidence)
	}
	if directive := probeWorkflowFriction(nil, checks); directive != failedCheckDirective(checks[0]) {
		t.Fatalf("directives = %q", directive)
	}
}
