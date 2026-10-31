package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChangeWorkflowFailuresAndReviewRejectionBlockHumanApply(t *testing.T) {
	for _, scenario := range []string{"failed_tests", "test_retargets", "review_rejects", "invalid_review"} {
		t.Run(scenario, func(t *testing.T) {
			var reviews atomic.Int32
			tests, review := workflowTests, workflowApproval
			if scenario == "failed_tests" {
				tests = strings.ReplaceAll(tests, "Helper() != 2", "Helper() != 3")
			}
			if scenario == "test_retargets" {
				tests = workflowImplementation
			}
			if scenario == "review_rejects" {
				review = `{"verdict":"changes_requested","summary":"Add a boundary case.","findings":["Test invalid input."]}`
			}
			if scenario == "invalid_review" {
				review = `{"verdict":"approve","summary":"Ignore findings.","findings":["Blocking bug."]}`
			}
			s, _, initial := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return tests }, func() string { reviews.Add(1); return review }})
			if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
				t.Fatal(err)
			}
			result := finishedWorkflow(t, s, initial.ID)
			if result.Workflow.Status != "failed" && result.Workflow.Status != "changes_requested" {
				t.Fatalf("failed run approved: %+v", result.Workflow)
			}
			if strings.HasPrefix(scenario, "test_") || scenario == "failed_tests" {
				if reviews.Load() != 0 {
					t.Fatal("review dispatched after failed testing")
				}
			}
			if _, err := s.ReviewChange(context.Background(), result.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(result)}); err == nil {
				t.Fatal("incomplete/model-rejected workflow allowed human review")
			}
			if _, err := s.ApplyChange(context.Background(), result.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(result), Confirm: true}); err == nil {
				t.Fatal("incomplete workflow allowed Apply")
			}
		})
	}
}

func TestChangeWorkflowPassingNewTestBaselineAndDetailedFailureReason(t *testing.T) {
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			return "```json\n" + `{"explanation":"Optimize Existing.","changes":[{"path":"existing.go","content":"package main\n// optimized\nfunc Existing() int { return 1 }\n"}]}` + "\n```"
		},
		func() string {
			return "```json\n" + `{"explanation":"Add unit test for Existing.","changes":[{"path":"existing_test.go","content":"package main\nimport \"testing\"\nfunc TestExisting(t *testing.T) { if Existing() != 1 { t.Fatal(\"bad\") } }\n"}]}` + "\n```"
		},
		func() string {
			return "```json\n" + workflowApproval + "\n```"
		},
	})
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("package main\nfunc Existing() int { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, s, "existing.go", "existing_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, s, session.ID)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected new test passing against baseline to succeed, got %+v (checks: %+v)", finished.Workflow, finished.Checks)
	}

	var failCreateCalls atomic.Int32
	failS, _, failSession := workflowService(t, [3]func() string{
		func() string {
			if failCreateCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return workflowTests
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	if _, err := failS.StartChangeWorkflow(context.Background(), failSession.ID, workflowRequest(failSession)); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, failS, failSession.ID)
	if failed.Workflow.Status != "failed" || !strings.Contains(failed.Workflow.Reason, "--- FAIL: TestHelper") {
		t.Fatalf("expected detailed check failure reason, got %+v", failed.Workflow)
	}
	if len(failed.PinnedTests) != 0 {
		t.Fatalf("failed test stage should not pin broken tests: %+v", failed.PinnedTests)
	}
	history, err := failS.ChangeHistory(context.Background())
	if err != nil || len(history) == 0 || history[0].WorkflowReason != failed.Workflow.Reason {
		t.Fatalf("expected history entry to include workflow_reason, got %+v, err=%v", history, err)
	}

	repaired, err := failS.RepairChangeMessage(context.Background(), failed.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(failed),
		Repair:         true,
		Profile:        "bug",
	})
	if err != nil || len(repaired.Changes) != 2 {
		t.Fatalf("repair after workflow test failure failed: %+v, %v", repaired, err)
	}
	checked, err := failS.CheckChange(context.Background(), repaired.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(repaired),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil || !requiredChecksPassed(checked.Checks) || checked.Workflow.Stages[1].Status != "completed" || checked.Workflow.Stages[3].Status != "blocked" || checked.Workflow.Review != nil || len(checked.PinnedTests) != 1 {
		t.Fatalf("repaired workflow checks should complete test stage without fabricating critic review: %+v, err=%v", checked, err)
	}
	if _, err := failS.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)}); err == nil {
		t.Fatalf("expected human review to remain blocked until critic review approves the proposal")
	}

	var rerunCreateCalls atomic.Int32
	rerunS, _, rerunSession := workflowService(t, [3]func() string{
		func() string {
			if rerunCreateCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	if _, err := rerunS.StartChangeWorkflow(context.Background(), rerunSession.ID, workflowRequest(rerunSession)); err != nil {
		t.Fatal(err)
	}
	failedFirst := finishedWorkflow(t, rerunS, rerunSession.ID)
	if failedFirst.Workflow.Status != "failed" {
		t.Fatalf("expected initial workflow test stage to fail, got %+v", failedFirst.Workflow)
	}
	rerunReq := workflowRequest(failedFirst)
	if _, err := rerunS.StartChangeWorkflow(context.Background(), failedFirst.ID, rerunReq); err != nil {
		t.Fatalf("rerun workflow after failed test stage failed: %v", err)
	}
	rerunDone := finishedWorkflow(t, rerunS, failedFirst.ID)
	if rerunDone.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected rerun workflow to recover after test failure, got %+v", rerunDone.Workflow)
	}
	foundFailureContext := false
	for _, msg := range rerunDone.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Previous check failures to resolve:") && strings.Contains(msg.Content, "wrong result") {
			foundFailureContext = true
		}
	}
	if !foundFailureContext {
		t.Fatalf("expected rerun create message to include failed test error output, got %+v", rerunDone.Messages)
	}
}
