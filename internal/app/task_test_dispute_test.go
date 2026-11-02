package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const wrongSubTest = "package main\n\nimport \"testing\"\n\nfunc TestSubOfZero(t *testing.T) {\n\tif Sub(0, 0) == 0 {\n\t\tt.Fatal(\"Sub(0, 0) must not be 0\")\n\t}\n}\n"

func TestACoderDisputesATesterTestOnceAndTheTesterDecides(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub."}},
		{Text: "Done.", Next: true},
		// The repair after the failing test: the tester's test is not the coder's to change.
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc_zero_test.go", "old_text": "== 0", "new_text": "!= 0"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Sub(0, 0) is 0 for any subtraction; the test asks for the impossible.", "outcome": "test_disputed", "disputed_tests": []string{"calc_zero_test.go"}}},
		{Text: "Disputed.", Next: true},
	}
	tester := []fakePiStep{
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_zero_test.go", "content": wrongSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Sub of zero must not be zero.", "follow_ups": []map[string]any{{"title": "Sub overflows", "detail": "Sub(minInt, 1) wraps; guarding it needs a new int helper.", "path": "calc.go"}}}},
		{Text: "Done.", Next: true},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc_zero_test.go", "old_text": "== 0", "new_text": "!= 0"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "The coding agent is right: Sub(0, 0) is 0. Corrected the test."}},
		{Text: "Done."},
	}
	flow := startToolFlowWith(t, coder, tester, nil, nil)
	failed := flow.finished(t)
	if failed.Workflow.Status != "failed" || len(failed.FollowUps) != 1 || failed.FollowUps[0].Title != "Sub overflows" {
		t.Fatalf("first run = %s (%s), follow-ups %+v", failed.Workflow.Status, failed.Workflow.Reason, failed.FollowUps)
	}
	// Running the agents again repairs the change from the failing check.
	request := flow.request
	request.ChangeIdentity = changeIdentity(failed)
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, request); err != nil {
		t.Fatal(err)
	}
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if !slices.Contains(finished.TesterTests, "calc_zero_test.go") || !slices.Contains(finished.DecidedTests, "calc_zero_test.go") || finished.TestDispute != nil {
		t.Fatalf("tests %v, decided %v, dispute %+v", finished.TesterTests, finished.DecidedTests, finished.TestDispute)
	}
	if data, err := os.ReadFile(filepath.Join(flow.worktree, "calc_zero_test.go")); err != nil || !strings.Contains(string(data), "Sub(0, 0) != 0") {
		t.Fatalf("the tester's decision is not the test: %q %v", data, err)
	}
	if len(finished.FollowUps) != 0 {
		t.Fatalf("the last testing run suggested no follow-up: %+v", finished.FollowUps)
	}
	last := fakePiLog(t, flow.dir, "tester")
	if !strings.Contains(last.Prompt, "The coding agent disputes tests you wrote and it may not change: calc_zero_test.go. It says: Sub(0, 0) is 0 for any subtraction") {
		t.Fatalf("the tester did not get the dispute:\n%s", last.Prompt)
	}
	if !strings.Contains(fakePiLog(t, flow.dir, "coder").Results[0], "calc_zero_test.go was written by the testing agent") {
		t.Fatalf("the coder changed the tester's test: %v", fakePiLog(t, flow.dir, "coder").Results)
	}
}

func TestADisputeNamesTheTesterTestsOnlyOnce(t *testing.T) {
	_, session, run := toolTask(t, toolCoder)
	session.TesterTests, session.DecidedTests = []string{"calc_test.go", "calc_zero_test.go"}, []string{"calc_zero_test.go"}
	run.session = session
	refusedTool(t, run, "finish", map[string]any{"summary": "x", "outcome": "test_disputed", "disputed_tests": []string{"calc_zero_test.go"}}, "already decided on calc_zero_test.go")
	refusedTool(t, run, "finish", map[string]any{"summary": "x", "outcome": "test_disputed", "disputed_tests": []string{"other_test.go"}}, "is not a test the testing agent wrote")
	refusedTool(t, run, "finish", map[string]any{"summary": "x", "outcome": "test_disputed"}, "name one to eight disputed tests")
	refusedTool(t, run, "finish", map[string]any{"summary": "x", "follow_ups": []map[string]any{{"title": "t", "detail": "d"}}}, "follow_ups are the testing agent's")
	refusedTool(t, run, "edit_file", map[string]any{"path": "calc_test.go", "old_text": "TestAdd", "new_text": "TestAddition"}, "calc_test.go was written by the testing agent")
	mustTool(t, run, "finish", map[string]any{"summary": "The test checks the wrong sign.", "outcome": "test_disputed", "disputed_tests": []string{"calc_test.go"}}, "Recorded.")
	if err := validateTestRecords(session); err != nil {
		t.Fatal(err)
	}
}
