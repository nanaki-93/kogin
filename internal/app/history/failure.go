package history

import "strings"

// Failure categories: a fixed set, so failures can be counted and the most
// frequent fixed first.
const (
	FailurePlanLimit       = "plan_limit"
	FailureTimeout         = "timeout"
	FailureContextLimit    = "context_limit"
	FailureCallBudget      = "call_budget"
	FailureNoFinish        = "no_finish"
	FailureInvalidResponse = "invalid_response"
	FailureChecks          = "checks_failed"
	FailureReviewRejected  = "review_rejected"
	FailureMainChanged     = "main_changed"
	FailureProvider        = "provider"
	FailureCanceled        = "canceled"
	FailureOther           = "other"
)

// failureSigns map words of a failure's reason to its category, first
// match wins.
var failureSigns = []struct {
	category string
	signs    []string
}{
	{FailurePlanLimit, []string{"usage limit", "plan's limit", "plan limit"}},
	{FailureTimeout, []string{"timed out", "deadline exceeded", "time limit"}},
	{FailureContextLimit, []string{"context exceeds", "exceeds 256 kib", "token limit", "too large"}},
	{FailureCallBudget, []string{"model calls limit", "calls limit", "call budget", "tool calls"}},
	{FailureNoFinish, []string{"without finish", "stopped before finishing", "never called finish", "did not finish"}},
	{FailureInvalidResponse, []string{"invalid response", "result could not be used", "invalid workflow", "not the expected json", "parse"}},
	{FailureChecks, []string{"check", "test", "lint", "budget"}},
	{FailureMainChanged, []string{"main changed", "out of date", "stale", "main moved"}},
	{FailureProvider, []string{"provider", "could not start", "unavailable", "connection", "status 5"}},
}

// Categorize names the failure category of a stage or run from its status
// and reason; empty for one that did not fail.
func Categorize(status, reason string) string {
	switch status {
	case "paused":
		return FailurePlanLimit
	case "canceled", "interrupted":
		return FailureCanceled
	case "changes_requested":
		return FailureReviewRejected
	case "stale":
		return FailureMainChanged
	case "failed":
	default:
		return ""
	}
	lower := strings.ToLower(reason)
	for _, sign := range failureSigns {
		for _, word := range sign.signs {
			if strings.Contains(lower, word) {
				return sign.category
			}
		}
	}
	return FailureOther
}
