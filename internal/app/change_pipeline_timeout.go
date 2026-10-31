package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type workflowTimeoutError struct {
	stage string
	limit time.Duration
}

func (e *workflowTimeoutError) Error() string {
	if e.stage == "" {
		return fmt.Sprintf("Workflow reached its total %s budget. Inspect the saved evidence and retry the unfinished stage. No source was applied.", e.limit)
	}
	return fmt.Sprintf("%s model request timed out after %s, including retries. Raise the agent model call time limit in Settings → Models & agents, or choose a faster model, then retry this stage. No source was applied.", e.stage, e.limit)
}

func (e *workflowTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// Preserve a more specific owner deadline when a child returns the context sentinel.
// workflowStopCause records why the daemon, not the user, stopped a workflow.
type workflowStopCause struct{ reason string }

func (c *workflowStopCause) Error() string { return "workflow stopped because " + c.reason }
func (c *workflowStopCause) Unwrap() error { return context.Canceled }

// Reasons name the event in the user's terms; they complete "Stopped because …".
const (
	stopReasonReindex         = "the project was reindexed"
	stopReasonApply           = "changes were applied or undone in this checkout"
	stopReasonGit             = "a Git merge or worktree change touched this checkout"
	stopReasonProjectSwitch   = "another project was opened"
	stopReasonShutdown        = "the daemon is shutting down"
	stopReasonWorktreeRemoved = "its worktree is being removed"
)

// stop cancels the job and records reason as its cancellation cause.
func (job *changeWorkflowJob) stop(reason string) {
	if job.interrupt != nil {
		job.interrupt(&workflowStopCause{reason: reason})
	}
	job.cancel()
}

// workflowDeadlineCause reports why a stopped run ended: a daemon stop names
// its reason even when the interrupted step failed in another way first.
func workflowDeadlineCause(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	var stopped *workflowStopCause
	if cause := context.Cause(ctx); errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(cause, &stopped) {
		return cause
	}
	return err
}

// A run must accommodate every remaining model call, including optional repairs.
// Provider retries share each model's deadline; they do not multiply this budget.
// startStage is the first model stage the run may call.
func (s *Service) changeWorkflowTimeout(ctx context.Context, models ChangeWorkflowModels, startStage int, autoRepair bool) time.Duration {
	var full, remaining time.Duration
	for stage, profile := range models.selections() {
		runtime := s.workflowStageRuntime(stage, profile)
		limit := duration(runtime.effective.Timeout)
		switch {
		case stage < 2 && runtime.client != nil && runtime.client.SupportsAgentRuns():
			// The stage may run as a tool-using agent.
			limit = s.toolStageLimit(runtime)
		case stage == 2:
			limit *= time.Duration(s.workflowReviewWaves(ctx, len(workflowReviewRoles)))
		}
		full += limit
		if stage >= startStage {
			remaining += limit
		}
	}
	// One check pass has at most five commands: two baselines, vet, gitleaks
	// and candidate tests. Reserve preparation time for copying and identity work.
	checks := 5*s.conf().focusedCheckTimeout + 5*time.Minute
	budget := remaining + checks
	if startStage == 0 {
		budget += checks // An existing proposal may need a pre-generation probe.
	}
	if autoRepair {
		budget += time.Duration(s.admittedRuntime(ctx).Limits.Repairs) * (full + checks)
	}
	return max(30*time.Minute, budget)
}

func (s *Service) workflowReviewWaves(ctx context.Context, count int) int {
	concurrency := max(1, s.admittedRuntime(ctx).Limits.ConcurrentCalls)
	return max(1, (count+concurrency-1)/concurrency)
}
