package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// groupedChangeResponse is a model's proposal that changes main.go and adds a doc.
const groupedChangeResponse = `{"explanation":"Update Run and add documentation.","changes":[{"path":"main.go","content":"package main\nfunc Run() { println(\"updated\") }\n"},{"path":"docs/change.md","content":"A documented change.\n"}]}`

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
