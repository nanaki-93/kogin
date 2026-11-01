package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

func TestChangeWorkflowRerunAfterPinnedTestsAndLargeFunctionScope(t *testing.T) {
	var createCalls, testCalls, reviewCalls atomic.Int32
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			if createCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return `{"explanation":"Add Helper with updated value.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 4 }\n"}]}`
		},
		func() string {
			if testCalls.Add(1) == 1 {
				return workflowTests
			}
			return `{"explanation":"Exercise updated Helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 4 }\n"},{"path":"new_test.go","content":"package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 4 { t.Fatal(\"wrong result\") } }\n"}]}`
		},
		func() string {
			if reviewCalls.Add(1) == 1 {
				return `{"verdict":"changes_requested","summary":"Return 4 instead of 2.","findings":["Update Helper to return 4."]}`
			}
			return workflowApproval
		},
	})
	padding := "// " + strings.Repeat("context ", 2400) + "\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"+padding+"func main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, s, "main.go", "new.go", "new_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	req := workflowRequest(session)
	req.Models = ChangeWorkflowModels{Create: "code", Test: "analyze", Review: "code"}
	// Route code profile to creator on stage 0 and reviewer on stage 2 by using catalog IDs.
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) {
		return []llm.AgyModel{{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"}}, nil
	}
	catalog, err := s.AvailableModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req.Models = ChangeWorkflowModels{Create: catalog.Defaults["bug"], Test: catalog.Defaults["analyze"], Review: catalog.Defaults["code"]}
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatalf("first workflow start failed: %v", err)
	}
	first := finishedWorkflow(t, s, session.ID)
	if first.Workflow.Status != "changes_requested" || len(first.PinnedTests) != 1 {
		t.Fatalf("expected changes_requested with pinned test: %+v", first)
	}
	req.ChangeIdentity = changeIdentity(first)
	req.Message = "Add Helper returning four, addressing critic feedback."
	if _, err := s.StartChangeWorkflow(context.Background(), first.ID, req); err != nil {
		t.Fatalf("rerun workflow start failed: %v", err)
	}
	second := finishedWorkflow(t, s, first.ID)
	if second.Workflow.Status != "awaiting_human_review" || len(second.PinnedTests) != 1 {
		t.Fatalf("rerun workflow failed to complete: %+v", second.Workflow)
	}
	for _, msg := range second.Messages {
		if msg.Role == "user" && msg.Content == workflowTestPrompt {
			// Only the latest test stage prompt should remain after stripping prior test prompts.
			continue
		}
	}
	edited, err := s.SendChangeMessage(context.Background(), second.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(second),
		Changes: []ChangeEditInput{
			{Path: "new.go", Content: "package main\nfunc Helper() int { return 8 }\n"},
			{Path: "new_test.go", Content: "package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 8 { t.Fatal(\"wrong result\") } }\n"},
		},
	})
	if err != nil || len(edited.PinnedTests) != 0 {
		t.Fatalf("expected manual test update after pinned tests to succeed and clear stale pin, got %+v, err=%v", edited, err)
	}
}

func TestChangeWorkflowAutonomousSelfHealingLoop(t *testing.T) {
	var createCalls atomic.Int32
	s, _, session := workflowService(t, [3]func() string{
		func() string {
			if createCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	healed := finishedWorkflow(t, s, session.ID)
	if healed.Workflow.Status != "awaiting_human_review" || !healed.Workflow.AutoRepair || healed.Workflow.AutoRepairAttempts != 1 {
		t.Fatalf("expected autonomous self-healing loop to succeed in 1 auto-repair attempt, got %+v", healed.Workflow)
	}
	foundAutoRepairPrompt := false
	for _, msg := range healed.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Previous check failures to resolve:") && strings.Contains(msg.Content, "Harness² Propose-Probe-Compose:") && strings.Contains(msg.Content, "wrong result") {
			foundAutoRepairPrompt = true
		}
	}
	if !foundAutoRepairPrompt {
		t.Fatalf("expected auto-repair Stage 0 prompt to include Harness² Probe and failed test output, got %+v", healed.Messages)
	}
}

func TestChangeWorkflowAutoRepairStageOneTimeoutPreservesCheckEvidenceAndReconcilesStages(t *testing.T) {
	var createCalls atomic.Int32
	s, _, session := workflowService(t, [3]func() string{
		func() string {
			switch createCalls.Add(1) {
			case 1:
				return workflowImplementation
			case 2:
				return "invalid-json-causing-stage1-failure"
			default:
				return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
			}
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, s, session.ID)
	if failed.Workflow.Status != "failed" || failed.Workflow.Stages[0].Status != "failed" || failed.Workflow.Stages[1].Status != "skipped" {
		t.Fatalf("expected auto-repair Stage 0 failure to leave only Stage 0 failed, got %+v", failed.Workflow)
	}
	if len(failed.Checks) == 0 || requiredChecksPassed(failed.Checks) {
		t.Fatalf("expected failed checks from Attempt 0 to be preserved when Auto-Repair Stage 0 fails, got %+v", failed.Checks)
	}

	// If manual CheckChange runs after a Stage 0 failure, Stage 0 must not remain failed alongside Stage 1.
	checked, err := s.CheckChange(context.Background(), failed.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(failed),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil {
		t.Fatalf("CheckChange after Stage 0 failure: %v", err)
	}
	if checked.Workflow.Stages[0].Status == "failed" || checked.Workflow.Stages[0].Reason != "" || checked.Workflow.Stages[1].Status != "failed" {
		t.Fatalf("expected Stage 0 failure to clear when Stage 1 check failure is recorded, got %+v", checked.Workflow.Stages)
	}

	// Rerunning the workflow via StartChangeWorkflow must include the preserved check failure and recover cleanly.
	rerunReq := workflowRequest(checked)
	rerunReq.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), checked.ID, rerunReq); err != nil {
		t.Fatalf("rerun StartChangeWorkflow failed: %v", err)
	}
	recovered := finishedWorkflow(t, s, checked.ID)
	if recovered.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected rerun workflow to reach awaiting_human_review, got %+v", recovered.Workflow)
	}
}

func TestChangeWorkflowNeverWidensScopeForUncapturedFailingTests(t *testing.T) {
	var testCalls atomic.Int32
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			return `{"explanation":"Validate nil password in SavePassword.","changes":[{"path":"cmd/master.go","content":"package cmd\nimport \"example.test/workflow/internal/utils\"\nfunc SavePassword(p []byte) bool {\n\tif len(p) == 0 {\n\t\treturn false\n\t}\n\treturn utils.NotEmpty(p)\n}\n"}]}`
		},
		func() string {
			if testCalls.Add(1) == 1 {
				return `{"explanation":"Add SavePassword test in master_test.go.","changes":[{"path":"cmd/master_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePassword(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"}]}`
			}
			return `{"explanation":"Update sibling add_test.go expectation alongside master_test.go.","changes":[{"path":"cmd/master_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePassword(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"},{"path":"cmd/add_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePasswordWhenEmpty(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"}]}`
		},
		func() string { return workflowApproval },
	})
	for rel, content := range map[string]string{
		"internal/utils/utils.go":      "package utils\nfunc NotEmpty(b []byte) bool { return len(b) > 0 }\n",
		"internal/utils/utils_test.go": "package utils\nimport \"testing\"\nfunc TestNotEmpty(t *testing.T) { if !NotEmpty([]byte(\"a\")) { t.Fatal(\"bad\") } }\n",
		"cmd/master.go":                "package cmd\nimport \"example.test/workflow/internal/utils\"\nfunc SavePassword(p []byte) bool {\n\tif p != nil && len(p) == 0 {\n\t\treturn false\n\t}\n\t_ = utils.NotEmpty(p)\n\treturn true\n}\n",
		"cmd/master_test.go":           "package cmd\nimport \"testing\"\n// BULKY_TEST_BODY_MARKER\nfunc TestSavePassword(t *testing.T) {\n\tif !SavePassword([]byte(\"secret123\")) {\n\t\tt.Fatal(\"expected true\")\n\t}\n}\n",
		"cmd/add_test.go":              "package cmd\nimport \"testing\"\nfunc TestSavePasswordWhenEmpty(t *testing.T) {\n\tif !SavePassword(nil) {\n\t\tt.Fatal(\"legacy test expected nil to succeed\")\n\t}\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	// Open session capturing ONLY cmd/master.go and cmd/master_test.go (omitting cmd/add_test.go).
	session := openChangeFixture(t, s, "cmd/master.go", "cmd/master_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}

	// Verify Stage 0 prompt context omits BULKY_TEST_BODY_MARKER and includes imported local Go declarations.
	stageZeroText, _, err := composeChangeStagePromptContext(root, session, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stageZeroText, "BULKY_TEST_BODY_MARKER") {
		t.Fatalf("expected Stage 0 workflow prompt to summarize test target declarations instead of full body, got:\n%s", stageZeroText)
	}
	if !strings.Contains(stageZeroText, "func NotEmpty(b []byte) bool") {
		t.Fatalf("expected Stage 0 workflow prompt to include local imported package declarations from internal/utils, got:\n%s", stageZeroText)
	}

	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	// The repair cannot fix a test outside the task's files: the testing agent's
	// attempt to write it fails the run and names the file, and the task's
	// files never change on their own.
	blocked := finishedWorkflow(t, s, session.ID)
	if blocked.Workflow.Status != "failed" || changeTarget(blocked, "cmd/add_test.go") != nil || !strings.Contains(blocked.Workflow.Reason, "cmd/add_test.go") {
		t.Fatalf("an uncaptured failing test widened the scope or went unnamed: %+v", blocked.Workflow)
	}
	// A new conversation explicitly captures the named file. The user can
	// then pass the retained failure evidence to the newly admitted workflow.
	callsBefore := testCalls.Load()
	expanded := openChangeFixture(t, s, "cmd/master.go", "cmd/master_test.go", "cmd/add_test.go")
	approved := workflowRequest(expanded)
	approved.Message = workflowRetryMessage(req.Message, blocked.Workflow, blocked.Checks)
	if _, err := s.StartChangeWorkflow(context.Background(), expanded.ID, approved); err != nil {
		t.Fatal(err)
	}
	healed := finishedWorkflow(t, s, expanded.ID)
	if healed.Workflow.Status != "awaiting_human_review" || healed.Workflow.AutoRepairAttempts != 0 || testCalls.Load() != callsBefore+1 {
		t.Fatalf("explicitly expanded scope did not recover: %+v", healed.Workflow)
	}
	var testStageSawFailure bool
	for _, msg := range healed.Messages {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "Write meaningful regression and boundary tests") && strings.Contains(msg.Content, "failure to resolve:") && strings.Contains(msg.Content, "add_test.go") {
			testStageSawFailure = true
		}
	}
	if !testStageSawFailure {
		t.Fatalf("expected the Stage 1 testing agent prompt to carry the failure that named add_test.go, got messages=%+v", healed.Messages)
	}
}
