package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// plannedChangeFixture opens the workflow task with a captured plan.
func plannedChangeFixture(t *testing.T, s *Service, plan ChangePlanInput) *ChangeSession {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.OpenChangeSession(context.Background(), ChangeCreateRequest{Plan: &plan, ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision,
		Kind: "feature", Title: "Implement a bounded task", Paths: []string{"new.go", "new_test.go"}, AcceptanceCriteria: []string{"Helper returns two."}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func plannedWorkflowFixture(t *testing.T, s *Service) *ChangeSession {
	t.Helper()
	return plannedChangeFixture(t, s, ChangePlanInput{Problem: "Expose Helper", Approach: "Add Helper and cover its result.", NonGoals: []string{"Do not change Run."}, ReadPaths: []string{"main.go"}})
}

func TestPlanApprovalRejectsUnapprovedRevisedAndStaleSourceWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	reply := func() string { calls.Add(1); return workflowImplementation }
	s, root, _ := workflowService(t, [3]func() string{reply, reply, reply})
	session := plannedWorkflowFixture(t, s)
	ctx := context.Background()
	if _, err := s.SendChangeMessage(ctx, session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Generate"}); err == nil {
		t.Fatal("unapproved plan generated code")
	}
	request := workflowRequest(session)
	if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err == nil {
		t.Fatal("missing approval accepted")
	}
	plan := session.Plan.ChangePlanInput
	plan.Approach += " Keep the public signature stable."
	revised := plannedChangeFixture(t, s, plan)
	if revised.Plan.Hash == session.Plan.Hash || revised.Plan.ApprovedHash != "" {
		t.Fatal("edited intent kept the plan's identity or approval")
	}
	request = workflowRequest(session)
	request.PlanHash = revised.Plan.Hash
	if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err == nil {
		t.Fatal("another plan approved")
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	request.PlanHash = session.Plan.Hash
	if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err == nil {
		t.Fatal("changed read context approved")
	}
	if calls.Load() != 0 {
		t.Fatalf("guard dispatched %d provider requests", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("guard wrote source")
	}
	stored, err := loadChangeSession(root, session.ID)
	if err != nil || stored.Workflow != nil || stored.Plan.ApprovedHash != "" {
		t.Fatalf("rejected approval changed stored state: %+v, %v", stored, err)
	}
}

func TestPlannedWorkflowResumesCriticAndRequiresCurrentFileReview(t *testing.T) {
	var calls [3]atomic.Int32
	s, root, _ := workflowService(t, [3]func() string{
		func() string { calls[0].Add(1); return workflowImplementation },
		func() string { calls[1].Add(1); return workflowTests },
		func() string {
			if calls[2].Add(1) == 1 {
				return "invalid review"
			}
			return workflowApproval
		},
	})
	session := plannedWorkflowFixture(t, s)
	ctx := context.Background()
	request := workflowRequest(session)
	request.PlanHash = session.Plan.Hash
	if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, s, session.ID)
	failedReviewCalls := calls[2].Load()
	if failed.Workflow.Status != "failed" || !requiredChecksPassed(failed.Checks) {
		t.Fatalf("unexpected critic failure: %+v", failed.Workflow)
	}
	request = workflowRequest(failed)
	request.PlanHash, request.ResumeFrom = failed.Plan.Hash, "review"
	if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, session.ID)
	if ready.Workflow.Status != "awaiting_human_review" || calls[0].Load() != 1 || calls[1].Load() != 1 || calls[2].Load() != failedReviewCalls+3 {
		t.Fatalf("resume regenerated earlier work: %+v", ready.Workflow)
	}
	if !reflect.DeepEqual(failed.Checks, ready.Checks) || ready.Workflow.Attempt != 2 {
		t.Fatal("resume lost check evidence or attempt accounting")
	}
	if ready.Workflow.Stages[2].StartedAt.IsZero() || ready.Workflow.Stages[2].FinishedAt.Before(ready.Workflow.Stages[2].StartedAt) {
		t.Fatal("missing stage timing")
	}
	if _, err := s.ReviewChange(ctx, session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(ready)}); err == nil {
		t.Fatal("uninspected files authorized apply")
	}
	if _, err := s.ReviewChangeFile(ctx, session.ID, ChangeFileReviewRequest{ChangeIdentity: changeIdentity(ready), Path: "main.go"}); err == nil {
		t.Fatal("unchanged read context marked reviewed")
	}
	for _, file := range ready.Changes {
		var err error
		ready, err = s.ReviewChangeFile(ctx, session.ID, ChangeFileReviewRequest{ChangeIdentity: changeIdentity(ready), Path: file.Path})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !allChangeFilesReviewed(ready) || ready.LastReviewedHash != ready.Hash {
		t.Fatal("complete review was not recorded")
	}
	if _, err := s.ReviewChange(ctx, session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(ready)}); err != nil {
		t.Fatal(err)
	}
	oldIdentity := changeIdentity(ready)
	var edits []ChangeEditInput
	for _, file := range ready.Changes {
		edits = append(edits, ChangeEditInput{Path: file.Path, Content: strings.ReplaceAll(file.Content, "return 2", "return 3")})
	}
	changed, err := s.SendChangeMessage(ctx, session.ID, ChangeMessageRequest{ChangeIdentity: oldIdentity, Changes: edits})
	if err != nil {
		t.Fatal(err)
	}
	if allChangeFilesReviewed(changed) || len(changed.ReviewedFiles) != 0 || len(changed.ReviewDiff) != 1 || changed.ReviewDiff[0].Path != "new.go" {
		t.Fatalf("revision retained review or lost comparison: %+v", changed)
	}
	if _, err := s.ReviewChangeFile(ctx, session.ID, ChangeFileReviewRequest{ChangeIdentity: oldIdentity, Path: "new.go"}); err == nil {
		t.Fatal("old diff review accepted")
	}
	if _, err := s.ApplyChange(ctx, session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(changed), Confirm: true}); err == nil {
		t.Fatal("changed proposal applied")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("review wrote source")
	}
}

func TestRegressionBaselinePreservesWhatWasActuallyObserved(t *testing.T) {
	for _, tc := range []struct {
		name    string
		check   CheckResult
		state   string
		outcome string
	}{
		{"red", CheckResult{State: CheckFailed, ExitCode: 1, Output: "--- FAIL: TestRegression (0s)"}, CheckPassed, "failed_before_change"},
		{"green", CheckResult{State: CheckPassed, ExitCode: 0}, CheckSkipped, "already_passes"},
		{"new API missing", CheckResult{State: CheckFailed, ExitCode: 1, Output: "undefined: Helper\nFAIL example [build failed]"}, CheckUnavailable, "could_not_execute"},
		{"unavailable", CheckResult{State: CheckUnavailable}, CheckUnavailable, "could_not_execute"},
		{"canceled", CheckResult{State: CheckCanceled}, CheckCanceled, "could_not_execute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classifyRegressionBaseline(&tc.check, func(check CheckResult) bool { return newTestsReproduced(nil, check.Output) })
			if tc.check.State != tc.state || tc.check.Outcome != tc.outcome || tc.check.Required {
				t.Fatalf("misclassified evidence: %+v", tc.check)
			}
		})
	}
}

func TestRunnerRegressionBaselineTreatsAnUnreadableFailureAsReproduced(t *testing.T) {
	reproduced := func(check CheckResult) bool { return newTestsReproduced(nil, check.Output) }
	red := CheckResult{State: CheckFailed, ExitCode: 1, Output: "1 failed"}
	classifyRegressionBaseline(&red, reproduced)
	green := CheckResult{State: CheckPassed, ExitCode: 0}
	classifyRegressionBaseline(&green, reproduced)
	if red.State != CheckPassed || red.Outcome != "failed_before_change" || green.State != CheckSkipped || green.Outcome != "already_passes" {
		t.Fatalf("runner baseline: red %+v, green %+v", red, green)
	}
	if newTestsReproduced([]string{"tests/test_a.py::test_old"}, "FAILED tests/test_a.py::test_old - boom\n") {
		t.Fatal("a failure that happened without the new tests reproduced nothing")
	}
}

func TestCriticRetryCannotReuseHistoricalCheckAuthority(t *testing.T) {
	s, _, _ := workflowService(t, [3]func() string{
		func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval },
	})
	session := plannedWorkflowFixture(t, s)
	request := workflowRequest(session)
	request.PlanHash = session.Plan.Hash
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, request); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, session.ID)
	restarted, err := New(scopedTestConfig("http://127.0.0.1:1"), s.manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.CancelChangeWorkflows)
	if _, err := restarted.TrustProjectExecution(context.Background(), ready.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	request = workflowRequest(ready)
	request.PlanHash, request.ResumeFrom = ready.Plan.Hash, "review"
	if _, err := restarted.StartChangeWorkflow(context.Background(), ready.ID, request); err == nil || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("historical checks authorized critic retry: %v", err)
	}
}

func TestPlanEvidenceRetainsLegacyHashAndInvalidatesNewApprovals(t *testing.T) {
	legacy := &ChangeSession{Kind: "fix", Title: "T", AcceptanceCriteria: []string{"works"}, Plan: &ChangePlan{ChangePlanInput: ChangePlanInput{Problem: "P", Approach: "A"}}}
	legacyJSON := `{"Kind":"fix","Title":"T","Plan":{"problem":"P","approach":"A"},"ReadTargets":null,"Targets":null,"Criteria":["works"],"Instructions":null,"Project":"","Revision":"","Workspace":"","Policy":""}`
	legacy.Plan.Hash = contentHash([]byte(legacyJSON))
	if err := validateStoredPlan(legacy); err != nil {
		t.Fatalf("legacy plan hash changed: %v", err)
	}
	encoded, err := json.Marshal(legacy.Plan.ChangePlanInput)
	if err != nil || strings.Contains(string(encoded), "citations") || strings.Contains(string(encoded), "assumptions") {
		t.Fatal("empty evidence changed legacy serialization")
	}
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	session := openChangeFixture(t, s, "main.go", "main_test.go")
	input := ChangePlanInput{Problem: "Cover Run", Approach: "Add regression tests"}
	if err := captureChangePlan(root, session, &input); err != nil {
		t.Fatal(err)
	}
	oldHash := session.Plan.Hash
	session.Plan.ApprovedHash = oldHash
	target := changeTarget(session, "main.go")
	input.Citations = []PlanEvidence{{Path: target.Path, Hash: target.Hash, StartLine: 1, EndLine: 1, Quote: "package main", Observation: "The source is in the main package"}}
	input.Assumptions = []string{"Keep the API stable"}
	if err := captureChangePlan(root, session, &input); err != nil {
		t.Fatal(err)
	}
	if session.Plan.Hash == oldHash || session.Plan.ApprovedHash != "" {
		t.Fatal("accepted evidence retained an earlier approval")
	}
	input.Citations[0].Quote = "fabricated source"
	if err := captureChangePlan(root, session, &input); err == nil {
		t.Fatal("fabricated evidence entered a stored plan")
	}
}
