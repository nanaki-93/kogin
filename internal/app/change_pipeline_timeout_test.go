package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestWorkflowModelTimeoutHonorsConfigurationWithoutChangingOtherRequests(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int{0, 900, 1800} {
		cfg := config.Default()
		cfg.Timeouts.WorkflowModelSeconds = seconds
		s, err := New(cfg, manager)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Duration(max(900, seconds)) * time.Second
		for stage, profile := range []string{"bug", "code", "analyze"} {
			if got := s.workflowStageRuntime(stage, profile).effective.Timeout; got != want.String() {
				t.Fatalf("stage %d timeout = %s, want %s", stage, got, want)
			}
		}
		if s.conf().runtimes.function.effective.Timeout != "5m0s" {
			t.Fatal("workflow changed standalone generation timeout")
		}
		s.conf().runtimes.bug.effective.Timeout = "45m0s"
		if got := s.workflowStageRuntime(0, "bug").effective.Timeout; got != "45m0s" {
			t.Fatalf("longer selected timeout shortened to %s", got)
		}
		s.conf().runtimes.function.effective.Timeout = "50m0s"
		if got := s.workflowStageRuntime(2, "analyze").effective.Timeout; got != "50m0s" {
			t.Fatalf("longer generation timeout shortened to %s", got)
		}
	}
}

func TestTestingTimeoutRetainsCreationAndResumesWithoutRegeneration(t *testing.T) {
	var createCalls, testCalls, reviewCalls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	s, root, initial := workflowService(t, [3]func() string{
		func() string { createCalls.Add(1); return workflowImplementation },
		func() string {
			if testCalls.Add(1) == 1 {
				<-release
			}
			return workflowTests
		},
		func() string { reviewCalls.Add(1); return workflowApproval },
	})
	s.conf().workflowModelTimeout = time.Second
	s.conf().runtimes.analyze.effective.Timeout = "1s"
	s.conf().runtimes.function.effective.Timeout = "1s"
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, s, initial.ID)
	if failed.Workflow.Status != "failed" || failed.Workflow.Stages[0].Status != "completed" || failed.Workflow.Stages[1].Status != "failed" {
		t.Fatalf("timeout stages = %+v", failed.Workflow)
	}
	if !strings.Contains(failed.Workflow.Reason, "Testing model request timed out after 1s") || !strings.Contains(failed.Workflow.Reason, "agent model call time limit in Settings → Models & agents") {
		t.Fatalf("missing actionable timeout: %s", failed.Workflow.Reason)
	}
	if createCalls.Load() != 1 || testCalls.Load() != 1 || reviewCalls.Load() != 0 || len(failed.Checks) != 0 || len(failed.Changes) != 1 {
		t.Fatalf("timeout dispatched more work or lost creation: calls=%d/%d/%d, checks=%v, changes=%d", createCalls.Load(), testCalls.Load(), reviewCalls.Load(), failed.Checks, len(failed.Changes))
	}
	s.conf().workflowModelTimeout = 15 * time.Minute
	retry := workflowRequest(failed)
	retry.ResumeFrom = "test"
	if _, err := s.StartChangeWorkflow(context.Background(), failed.ID, retry); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, s, initial.ID)
	if finished.Workflow.Status != "awaiting_human_review" || createCalls.Load() != 1 || testCalls.Load() != 2 || reviewCalls.Load() != 3 || !requiredChecksPassed(finished.Checks) {
		t.Fatalf("test retry did not recover: %+v, calls=%d/%d/%d", finished.Workflow, createCalls.Load(), testCalls.Load(), reviewCalls.Load())
	}
	if finished.Workflow.Stages[1].Model.Timeout != "15m0s" {
		t.Fatalf("retry reused the old timeout: %+v", finished.Workflow.Stages[1].Model)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("workflow applied source without human review")
	}
}

func TestWorkflowTimeoutCausesDistinguishModelAndOverallBudgets(t *testing.T) {
	for _, test := range []struct{ stage, want string }{
		{"Creation", "Creation model request"}, {"Testing", "Testing model request"},
		{"Review", "Review model request"}, {"", "total 15m0s budget"},
	} {
		err := &workflowTimeoutError{stage: test.stage, limit: 15 * time.Minute}
		status, reason := workflowFailure(err, nil)
		if !errors.Is(err, context.DeadlineExceeded) || status != "failed" || !strings.Contains(reason, test.want) {
			t.Fatalf("timeout classification: %s, %s", status, reason)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _ := workflowFailure(context.Cause(ctx), nil)
	if status != "canceled" {
		t.Fatalf("cancellation became a timeout: %s", status)
	}
	overall := &workflowTimeoutError{limit: time.Hour}
	expired, stop := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), overall)
	defer stop()
	if got := workflowDeadlineCause(expired, context.DeadlineExceeded); got != overall {
		t.Fatalf("lost total workflow deadline cause: %v", got)
	}
	if got := workflowDeadlineCause(expired, project.ErrRevisionConflict); got != project.ErrRevisionConflict {
		t.Fatalf("deadline hid a source conflict: %v", got)
	}
}

func TestTimedOutChecksStopAutomaticRepairAndRetainEvidence(t *testing.T) {
	var createCalls, testCalls, reviewCalls atomic.Int32
	s, _, initial := workflowService(t, [3]func() string{
		func() string { createCalls.Add(1); return workflowImplementation },
		func() string { testCalls.Add(1); return workflowTests },
		func() string { reviewCalls.Add(1); return workflowApproval },
	})
	s.conf().focusedCheckTimeout = time.Nanosecond
	req := workflowRequest(initial)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, req); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, s, initial.ID)
	if failed.Workflow.Status != "failed" || failed.Workflow.AutoRepairAttempts != 0 || createCalls.Load() != 1 || testCalls.Load() != 1 || reviewCalls.Load() != 0 {
		t.Fatalf("interrupted checks triggered generation: %+v, calls=%d/%d/%d", failed.Workflow, createCalls.Load(), testCalls.Load(), reviewCalls.Load())
	}
	if !strings.Contains(failed.Workflow.Reason, "Required checks did not complete") {
		t.Fatalf("interruption mislabeled as a code failure: %s", failed.Workflow.Reason)
	}
	for _, check := range failed.Checks {
		if check.Name == "tests" && check.State == CheckCanceled && strings.Contains(check.Output, "check time limit in Settings → Models & agents") {
			return
		}
	}
	t.Fatalf("timed-out check evidence missing: %+v", failed.Checks)
}

func TestWorkflowRepairsOnlyEstablishedCheckFailures(t *testing.T) {
	for _, state := range []string{CheckCanceled, CheckUnavailable, CheckNotRun, CheckSkipped, CheckPassed, CheckFailed} {
		checks := []CheckResult{{Required: true, State: state}, {Required: false, State: CheckUnavailable}}
		if got := workflowChecksRepairable(checks); got != (state == CheckFailed) {
			t.Fatalf("repairable %s = %t", state, got)
		}
	}
	if workflowChecksRepairable([]CheckResult{{Required: true, State: CheckFailed}, {Required: true, State: CheckCanceled}}) {
		t.Fatal("partial check execution triggered repair")
	}
}

func TestWorkflowTotalDeadlineAccommodatesStagesChecksAndRepairs(t *testing.T) {
	s := &Service{}
	s.configured.Store(&configuration{workflowModelTimeout: 15 * time.Minute, focusedCheckTimeout: time.Minute})
	s.conf().agentRuntime, _ = config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	models := ChangeWorkflowModels{Create: "bug", Test: "code", Review: "analyze"}
	full := s.changeWorkflowTimeout(t.Context(), models, 0, false)
	resumed := s.changeWorkflowTimeout(t.Context(), models, 1, false)
	if full <= 45*time.Minute || resumed <= 30*time.Minute || resumed >= full {
		t.Fatalf("remaining models/checks do not fit: full=%s, test retry=%s", full, resumed)
	}
	withRepairs := s.changeWorkflowTimeout(t.Context(), models, 1, true)
	if withRepairs < resumed+2*(45*time.Minute+5*s.conf().focusedCheckTimeout) {
		t.Fatalf("repair attempts would be cut short: %s", withRepairs)
	}
	s.conf().agentRuntime.Limits.Repairs = 0
	if got := s.changeWorkflowTimeout(t.Context(), models, 1, true); got != resumed {
		t.Fatalf("disabled repairs retained time allowance: %s", got)
	}
	s.conf().focusedCheckTimeout = 10 * time.Minute
	if got := s.changeWorkflowTimeout(t.Context(), models, 0, false); got < 45*time.Minute+10*s.conf().focusedCheckTimeout {
		t.Fatalf("probe and candidate checks would be cut short: %s", got)
	}
}
