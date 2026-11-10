package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// A plan's verdicts end a task early: when the investigation finds that no
// linked issue needs a change, there is nothing to approve and nothing for
// the agents to write. The task is closed from the plan step, dismissing the
// issues that do not hold or are already fixed.

// judgedIssues is how many issues a plan for the task gives a verdict to:
// every step of a task that fixes its issues one at a time, or the one issue
// a task links without steps. A task that links none has no verdicts.
func judgedIssues(session *ChangeSession) int {
	switch {
	case len(session.Issues) > 1:
		return len(session.Issues)
	case len(session.Issues) == 0 && len(session.FindingTargets) == 1:
		return 1
	}
	return 0
}

// judgedFindingID is the finding a plan's verdict number names (1-based).
func judgedFindingID(session *ChangeSession, number int) string {
	if len(session.Issues) > 1 {
		return session.Issues[number-1].FindingID
	}
	return session.FindingTargets[0].ID
}

// planNoFix returns the linked issues the task's investigator plan says do
// not hold or are already fixed, and whether the plan says of every issue
// that this task has no fix to make for it.
func planNoFix(session *ChangeSession) ([]project.FindingTarget, bool) {
	plan := session.Plan
	if plan == nil || plan.Source != planSourceInvestigator || len(plan.IssueVerdicts) == 0 || len(plan.IssueVerdicts) != judgedIssues(session) {
		return nil, false
	}
	var dismiss []project.FindingTarget
	for _, verdict := range plan.IssueVerdicts {
		if verdict.Verdict == planVerdictHolds {
			return nil, false
		}
		if verdict.Verdict == outcomeNeedsOwnTask {
			continue
		}
		for _, target := range session.FindingTargets {
			if target.ID == verdict.FindingID {
				dismiss = append(dismiss, target)
			}
		}
	}
	return dismiss, true
}

// planNoFixReason is why the plan says a task without issue steps has
// nothing to fix: its one verdict's note.
func planNoFixReason(session *ChangeSession) string {
	if session.Plan == nil || len(session.Plan.IssueVerdicts) != 1 {
		return ""
	}
	verdict := session.Plan.IssueVerdicts[0]
	lead := "the issue does not hold"
	if verdict.Verdict == outcomeAlreadyFixed {
		lead = "the code already fixes the issue"
	}
	if note := strings.TrimSpace(verdict.Note); note != "" {
		return lead + ". " + note
	}
	return lead + "."
}

// closableWithoutChange reports the issues closing the task dismisses, or
// why it cannot be closed: a run that ended with nothing to fix, or a plan
// waiting for review that says so of every issue.
func closableWithoutChange(session *ChangeSession) ([]project.FindingTarget, error) {
	if session.Workflow == nil || len(session.Changes) > 0 {
		return nil, errNotNothingToFix
	}
	switch session.Workflow.Status {
	case "nothing_to_fix":
		return session.DismissedTargets, nil
	case "awaiting_plan_review":
		if dismiss, settled := planNoFix(session); settled {
			return dismiss, nil
		}
	}
	return nil, errNotNothingToFix
}

// singleIssueNoFix ends the creation stage of a task that links one issue
// and fixes it without steps, when its coding agent changed nothing and said
// the issue is already fixed or does not hold.
func singleIssueNoFix(session *ChangeSession, run *ChangeStageTools, err error) (*toolFinish, bool) {
	if !errors.Is(err, errCoderNoChange) || judgedIssues(session) != 1 || len(session.Issues) != 0 || run == nil || run.finish == nil {
		return nil, false
	}
	if outcome := run.finish.Outcome; outcome != outcomeAlreadyFixed && outcome != outcomeDoesNotHold {
		return nil, false
	}
	return run.finish, true
}

// noFixReason is a coding agent's own account of why it changed nothing.
func noFixReason(finish *toolFinish) string {
	lead := "the coding agent found the issue does not hold"
	if finish.Outcome == outcomeAlreadyFixed {
		lead = "the coding agent found the code already fixes the issue"
	}
	return fmt.Sprintf("%s. %s", lead, clip(strings.TrimSpace(finish.Summary), 600))
}
