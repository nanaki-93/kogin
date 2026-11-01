package app

import (
	"context"
	"errors"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

func workflowStageLabel(stage string) string {
	switch stage {
	case "create":
		return "creation"
	case "test":
		return "testing"
	default:
		return stage
	}
}

func workflowFailure(err error, checks []CheckResult) (string, string) {
	var timeout *workflowTimeoutError
	var stopped *workflowStopCause
	switch {
	case errors.As(err, &stopped):
		return "canceled", "Stopped because " + stopped.reason + ". No source was applied; run the task again to continue."
	case errors.Is(err, context.Canceled):
		return "canceled", "Workflow canceled. No source was applied."
	case errors.As(err, &timeout):
		return "failed", timeout.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "failed", "An operation timed out. Inspect the failed stage and check evidence before retrying."
	case errors.Is(err, project.ErrRevisionConflict), errors.Is(err, project.ErrExcludedFile):
		return "stale", staleWorkflowReason(staleReason(err))
	case errors.Is(err, errWorkflowChecksFailed):
		base := "Tests or source checks failed. Inspect the check evidence before running again."
		if existingTestsFail(checks) {
			base = "Checks already failed before this change and Kasuri could not tell which failures are new, so the agent cannot fix them. Fix or skip those checks, or declare a narrower command in AGENTS.md, then run again."
		} else if !workflowChecksRepairable(checks) {
			base = "Required checks did not complete. Inspect the check evidence and retry checks before changing code."
		}
		if detail := summarizeFailedChecks(checks); detail != "" {
			return "failed", base + " (" + detail + ")"
		}
		return "failed", base
	default:
		base := "The stage could not complete or returned an invalid response. Check its model and captured scope, then run again."
		if detail := sanitizeWorkflowError(err); detail != "" {
			return "failed", base + " (" + detail + ")"
		}
		return "failed", base
	}
}

func summarizeFailedChecks(checks []CheckResult) string {
	for _, check := range checks {
		if !check.Required || check.State == CheckPassed {
			continue
		}
		line := firstCheckFailureLine(check.Output)
		if line != "" {
			return check.Name + ": " + line
		}
		return check.Name + ": " + check.State
	}
	return ""
}

func firstCheckFailureLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.Contains(trimmed, "--- FAIL:") || strings.HasPrefix(trimmed, "FAIL") || strings.Contains(trimmed, "Error:") || strings.Contains(trimmed, "undefined:") {
			if len(trimmed) > 140 {
				return textutil.Truncate(trimmed, 140) + "…"
			}
			return trimmed
		}
	}
	for _, raw := range lines {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			if len(trimmed) > 140 {
				return textutil.Truncate(trimmed, 140) + "…"
			}
			return trimmed
		}
	}
	return ""
}

func sanitizeWorkflowError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = strings.TrimSpace(text[:idx])
	}
	if len(text) > 160 {
		text = textutil.Truncate(text, 160) + "…"
	}
	return text
}
