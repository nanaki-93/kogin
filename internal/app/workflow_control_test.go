package app

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDaemonStoppedWorkflowsSayWhy(t *testing.T) {
	ctx, interrupt := context.WithCancelCause(context.Background())
	canceled := false
	job := &changeWorkflowJob{interrupt: interrupt, cancel: func() { canceled = true }}
	job.stop(stopReasonApply)
	if !canceled {
		t.Fatal("stop must also cancel the job")
	}
	status, reason := workflowFailure(workflowDeadlineCause(ctx, ctx.Err()), nil)
	if status != "canceled" || !strings.Contains(reason, "Stopped because changes were applied or undone in this checkout") {
		t.Fatalf("daemon stop must name its cause: %s, %q", status, reason)
	}

	userCtx, userCancel := context.WithCancel(context.Background())
	userCancel()
	if _, reason := workflowFailure(workflowDeadlineCause(userCtx, userCtx.Err()), nil); reason != "Workflow canceled. No source was applied." {
		t.Fatalf("a user cancel keeps its own message, got %q", reason)
	}
}

func TestPreExistingTestFailuresAreNotSentToRepair(t *testing.T) {
	checks := []CheckResult{
		{Name: "tests: web", Required: true, State: CheckFailed, Outcome: "also_failed_before"},
		{Name: "tests", Required: true, State: CheckFailed},
	}
	if workflowChecksRepairable(checks) {
		t.Fatal("a failure that also happened before the change cannot be repaired by changing the proposal")
	}
	if _, reason := workflowFailure(errWorkflowChecksFailed, checks); !strings.Contains(reason, "Checks already failed before this change") {
		t.Fatalf("the reason must say the checks failed before the change: %q", reason)
	}
	if !workflowChecksRepairable(checks[1:]) {
		t.Fatal("a failure introduced by the proposal stays repairable")
	}
}

func TestRetryMessageKeepsFeedbackWhenTheRequestIsLong(t *testing.T) {
	request := strings.Repeat("é", maxWorkflowRetryMessage) // 2 bytes per rune
	workflow := &ChangeWorkflow{Review: &ChangeWorkflowReview{Verdict: "changes_requested", Findings: []ReviewItem{{Title: "handle the nil config"}}}}
	message := workflowRetryMessage(request, workflow, nil)
	if len(message) > maxWorkflowRetryMessage || !utf8.ValidString(message) {
		t.Fatalf("retry message must fit the budget as valid UTF-8: %d bytes", len(message))
	}
	if !strings.Contains(message, "handle the nil config") {
		t.Fatal("review feedback was cut off by a long request")
	}
}
