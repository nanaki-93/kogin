package app

import (
	"fmt"
	"slices"
	"strings"
)

// Once every issue had its step, the task is what the steps fixed: testing,
// review and repairs read only those issues' acceptance criteria and are
// told which issues were left out, so they do not take those up again
// through tests, review findings or repairs. The stored task keeps all its
// criteria; the views here are only read.

// leftOutIssues are the issues the steps settled without a fix of their
// own in this task: they do not hold, or could not be fixed. None while a
// step is still to run.
func leftOutIssues(session *ChangeSession) []ChangeIssue {
	if !stepsEnabled(session) || nextIssue(session) >= 0 {
		return nil
	}
	var left []ChangeIssue
	for _, issue := range session.Issues {
		if issue.Status == IssueNotFixed || issue.Status == IssueDoesNotHold {
			left = append(left, issue)
		}
	}
	return left
}

// fixedIssuesView is the task as the stages after its steps read it: the
// acceptance criteria of the issues it fixed only. A task whose fixed
// issues carry no criteria of their own keeps all of them.
func fixedIssuesView(session *ChangeSession) *ChangeSession {
	if len(leftOutIssues(session)) == 0 {
		return session
	}
	var criteria []string
	for _, issue := range session.Issues {
		if issue.Status != IssueFixed {
			continue
		}
		for _, criterion := range issue.Criteria {
			if !slices.Contains(criteria, criterion) {
				criteria = append(criteria, criterion)
			}
		}
	}
	if len(criteria) == 0 {
		return session
	}
	view := *session
	view.AcceptanceCriteria = criteria
	return &view
}

// leftOutBrief tells a stage after the steps which issues the task left
// out, and that they are not its work.
func leftOutBrief(session *ChangeSession) string {
	left := leftOutIssues(session)
	if len(left) == 0 {
		return ""
	}
	var brief strings.Builder
	brief.WriteString("\nIssues this task left out. They are not part of this change: do not write tests for them, fix them, or ask for them in review.\n")
	for _, issue := range left {
		ended := "it does not hold"
		if issue.Status == IssueNotFixed {
			ended = "not fixed here; it is open again in Issues for a task of its own"
		}
		fmt.Fprintf(&brief, "- %s (%s): %s\n", issue.Title, issue.Path, ended)
	}
	return brief.String()
}
