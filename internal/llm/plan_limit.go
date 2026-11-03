package llm

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// ErrPlanLimit marks a run that stopped because the subscription plan behind
// a CLI reached its usage limit. It is not retried: the work waits until the
// plan's window resets.
var ErrPlanLimit = errors.New("the subscription plan's usage limit is reached")

// PlanLimitError says which CLI's plan reached its limit and, when the CLI
// said so, when the limit resets.
type PlanLimitError struct {
	Provider config.ModelProvider
	// ResetsAt is zero when the CLI did not say when the limit resets.
	ResetsAt time.Time
}

func (e *PlanLimitError) Error() string {
	text := fmt.Sprintf("llm client: the %s reached its usage limit", planName(e.Provider))
	if !e.ResetsAt.IsZero() {
		text += "; it resets at " + e.ResetsAt.Local().Format("15:04 on Jan 2")
	}
	return text
}

func (e *PlanLimitError) Unwrap() error { return ErrPlanLimit }

// Plan names the subscription that reached its limit, such as "Claude plan".
func (e *PlanLimitError) Plan() string { return planName(e.Provider) }

// planName names the subscription a CLI runs on.
func planName(provider config.ModelProvider) string {
	switch provider {
	case config.ClaudeProvider:
		return "Claude plan"
	case config.AgyProvider:
		return "Google AI plan"
	case config.PiProvider:
		return "subscription pi signs in with"
	default:
		return "plan behind " + string(provider)
	}
}

// now is the clock reset times are computed from; tests replace it.
var now = time.Now

var (
	planLimitText = regexp.MustCompile(`(?i)usage[ _]limit|rate[ _]limit|limit reached|quota|resource[ _]exhausted|exhausted your (?:capacity|quota)|out of (?:ai )?credits|too many requests|\b429\b|hit your limit`)
	resetsIn      = regexp.MustCompile(`(?i)(?:try again|resets?|retry)\s+in\s+(?:about\s+|~)?(\d+)\s*(seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)\b`)
	resetsAfter   = regexp.MustCompile(`(?i)"?resets?_in_seconds"?\s*[:=]\s*(\d+)`)
	// resetsDuration reads Go-style durations: "will reset after 1h29m29s".
	resetsDuration = regexp.MustCompile(`(?i)(?:reset|try again|retry)\w*\s+(?:after|in)\s+((?:\d+h)?(?:\d+m)?(?:\d+(?:\.\d+)?s)?)\b`)
	// resetsEpoch reads Claude Code's "usage limit reached|1761900000".
	resetsEpoch = regexp.MustCompile(`(?i)limit reached\|(\d{10})\b`)
)

// planLimitFromText recognizes a CLI's report of an exhausted plan in its
// error text, with the reset time when the text gives one.
func planLimitFromText(provider config.ModelProvider, text string) *PlanLimitError {
	if !planLimitText.MatchString(text) {
		return nil
	}
	at := now()
	limit := &PlanLimitError{Provider: provider}
	if match := resetsEpoch.FindStringSubmatch(text); match != nil {
		if seconds, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			limit.ResetsAt = time.Unix(seconds, 0)
		}
		return limit
	}
	if match := resetsDuration.FindStringSubmatch(text); match != nil && match[1] != "" {
		if wait, err := time.ParseDuration(strings.ToLower(match[1])); err == nil && wait > 0 {
			limit.ResetsAt = at.Add(wait)
			return limit
		}
	}
	if match := resetsAfter.FindStringSubmatch(text); match != nil {
		if seconds, err := strconv.Atoi(match[1]); err == nil {
			limit.ResetsAt = at.Add(time.Duration(seconds) * time.Second)
		}
		return limit
	}
	if match := resetsIn.FindStringSubmatch(text); match != nil {
		amount, err := strconv.Atoi(match[1])
		if err != nil {
			return limit
		}
		unit := time.Second
		switch strings.ToLower(match[2])[0] {
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		}
		limit.ResetsAt = at.Add(time.Duration(amount) * unit)
	}
	return limit
}
