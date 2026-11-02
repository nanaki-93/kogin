package app

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// A task drafted to fix several issues of a file carries them as steps. In
// its own worktree, the creation stage fixes them one at a time: a fresh
// coding run per issue, the checks, and a commit for each issue it fixed. An
// issue the agents could not fix is rolled back, unlinked so it is open
// again, and the next one starts. Testing and review then cover the branch.

// Issue step states. A new step is pending; fixing marks the one running.
const (
	IssuePending  = "pending"
	IssueFixing   = "fixing"
	IssueFixed    = "fixed"
	IssueNotFixed = "not_fixed"
)

const (
	maxIssueTitleBytes  = 300
	maxIssueDetailBytes = 4 * 1024
	maxIssueReasonBytes = 1024
)

// ChangeIssue is one issue a task fixes in a step of its own.
type ChangeIssue struct {
	FindingID string   `json:"finding_id"`
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Detail    string   `json:"detail"`
	Criteria  []string `json:"criteria,omitempty"`
	Status    string   `json:"status,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Commit    string   `json:"commit,omitempty"`
}

// stepsEnabled reports whether the task fixes its issues one at a time: it
// has several and owns a worktree, where each fixed issue gets a commit.
func stepsEnabled(session *ChangeSession) bool {
	return len(session.Issues) > 1 && ownsWorktree(session)
}

// nextIssue is the index of the first issue not yet tried, or -1.
func nextIssue(session *ChangeSession) int {
	return slices.IndexFunc(session.Issues, func(issue ChangeIssue) bool {
		return issue.Status == IssuePending || issue.Status == IssueFixing
	})
}

// lastIssueCommit is the commit of the latest fixed issue, or "".
func lastIssueCommit(session *ChangeSession) string {
	for i := len(session.Issues) - 1; i >= 0; i-- {
		if issue := session.Issues[i]; issue.Status == IssueFixed && issue.Commit != "" {
			return issue.Commit
		}
	}
	return ""
}

// newChangeIssues validates a new task's issues against the issues it links
// and returns them pending. Each issue must be linked once.
func newChangeIssues(issues []ChangeIssue, targets []project.FindingTarget) ([]ChangeIssue, error) {
	if len(issues) == 0 {
		return nil, nil
	}
	if len(issues) < 2 || len(issues) > maxIssuesPerTask {
		return nil, fmt.Errorf("a task fixes two to %d issues one at a time", maxIssuesPerTask)
	}
	fresh := make([]ChangeIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Status != "" || issue.Reason != "" || issue.Commit != "" {
			return nil, fmt.Errorf("a new task's issues carry no progress")
		}
		issue.Status = IssuePending
		fresh = append(fresh, issue)
	}
	if err := validateChangeIssues(fresh, targets); err != nil {
		return nil, err
	}
	return fresh, nil
}

// validateChangeIssues checks a task's issues: distinct, bounded, and linked
// unless the agents could not fix them (those are unlinked).
func validateChangeIssues(issues []ChangeIssue, targets []project.FindingTarget) error {
	if len(issues) > maxIssuesPerTask {
		return fmt.Errorf("a task fixes at most %d issues one at a time", maxIssuesPerTask)
	}
	seen := map[string]bool{}
	for _, issue := range issues {
		linked := slices.ContainsFunc(targets, func(target project.FindingTarget) bool {
			return target.ID == issue.FindingID && target.Path == issue.Path
		})
		switch {
		case issue.FindingID == "" || seen[issue.FindingID]:
			return fmt.Errorf("each issue of a task is named once")
		case !boundedIssueText(issue.Title, maxIssueTitleBytes, true) || !boundedIssueText(issue.Detail, maxIssueDetailBytes, false) || !boundedIssueText(issue.Reason, maxIssueReasonBytes, false):
			return fmt.Errorf("an issue's title, detail and reason must be bounded text")
		case validateChangeCriteria(issue.Criteria) != nil:
			return fmt.Errorf("an issue's criteria must be bounded text")
		case !validIssueStep(issue, linked):
			return fmt.Errorf("issue %s has an inconsistent step", issue.FindingID)
		}
		seen[issue.FindingID] = true
	}
	return nil
}

func validIssueStep(issue ChangeIssue, linked bool) bool {
	switch issue.Status {
	case IssuePending, IssueFixing:
		return linked && issue.Commit == "" && issue.Reason == ""
	case IssueFixed:
		return linked && validCommitOID(issue.Commit)
	case IssueNotFixed:
		return !linked && issue.Commit == "" && issue.Reason != ""
	default:
		return false
	}
}

func validCommitOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	return strings.Trim(oid, "0123456789abcdef") == ""
}

func boundedIssueText(text string, limit int, required bool) bool {
	if text == "" {
		return !required
	}
	return len(text) <= limit && utf8.ValidString(text) && !strings.ContainsRune(text, 0) && strings.TrimSpace(text) != ""
}

// issuesForRequest restates a task's issues for capturing it again: those it
// still links, with their progress cleared.
func issuesForRequest(issues []ChangeIssue, targets []project.FindingTarget) []ChangeIssue {
	var kept []ChangeIssue
	for _, issue := range issues {
		if slices.ContainsFunc(targets, func(target project.FindingTarget) bool { return target.ID == issue.FindingID }) {
			issue.Status, issue.Reason, issue.Commit = "", "", ""
			kept = append(kept, issue)
		}
	}
	if len(kept) < 2 {
		return nil
	}
	return kept
}
