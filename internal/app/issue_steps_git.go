package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Each fixed issue becomes a commit while the task runs, without moving the
// task's branch: the worktree stays on its base, so the proposal is still
// what differs from it. The commits chain from the base under a private ref
// that keeps them alive; accepting the task puts the branch on them.

// issueStepsRef keeps a task's issue commits until it is accepted.
func issueStepsRef(session *ChangeSession) string {
	return "refs/kasuri/tasks/" + session.ID
}

// worktreeProposalTree writes the tree of the base commit with the proposal
// applied, in a private index, and returns its ID.
func worktreeProposalTree(ctx context.Context, root string, session *ChangeSession) (string, error) {
	temporary, err := os.MkdirTemp("", "kasuri-git-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	index := filepath.Join(temporary, "index")
	if _, err := gitCommand(ctx, root, index, nil, "read-tree", session.Worktree.Base); err != nil {
		return "", err
	}
	for _, edit := range session.Changes {
		if err := stageWorktreeEdit(ctx, root, index, session, edit); err != nil {
			return "", err
		}
	}
	tree, err := gitCommand(ctx, root, index, nil, "write-tree")
	return strings.TrimSpace(tree), err
}

// commitIssueStep commits the proposal as it is after an issue's step, on
// top of the previous fixed issue (or the base), and keeps it under the
// task's ref.
func commitIssueStep(ctx context.Context, root string, session *ChangeSession, issue ChangeIssue) (string, error) {
	tree, err := worktreeProposalTree(ctx, root, session)
	if err != nil {
		return "", err
	}
	parent := lastIssueCommit(session)
	if parent == "" {
		parent = session.Worktree.Base
	}
	oid, err := gitCommand(ctx, root, "", strings.NewReader(issueCommitMessage(issue)+"\n"), "commit-tree", tree, "-p", parent)
	if err != nil {
		return "", err
	}
	oid = strings.TrimSpace(oid)
	if _, err := gitRead(ctx, root, "update-ref", "-m", "kasuri: issue step", issueStepsRef(session), oid); err != nil {
		return "", err
	}
	return oid, nil
}

// issueCommitMessage is a fixed issue's commit subject: "fix: <issue>".
func issueCommitMessage(issue ChangeIssue) string {
	title := strings.Join(strings.Fields(issue.Title), " ")
	for _, prefix := range []string{"fix:", "fix "} {
		if len(title) >= len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
			title = strings.TrimSpace(title[len(prefix):])
		}
	}
	if title == "" {
		title = "issue in " + issue.Path
	}
	runes := []rune(title)
	runes[0] = unicode.ToLower(runes[0])
	return "fix: " + string(runes)
}

// acceptParent is the commit an accepted task's last commit builds on: the
// latest fixed issue's commit when it still descends from the base, or the
// base. A warning says why the issue commits were left out.
func acceptParent(ctx context.Context, root string, session *ChangeSession) (string, string) {
	base, last := session.Worktree.Base, lastIssueCommit(session)
	if last == "" {
		return base, ""
	}
	if _, err := gitRead(ctx, root, "merge-base", "--is-ancestor", base, last); err != nil {
		return base, "The commits of the fixed issues could not be found, so the change was committed as one commit."
	}
	return last, ""
}

// sameTree reports whether commit holds tree.
func sameTree(ctx context.Context, root, commit, tree string) bool {
	got, err := gitRead(ctx, root, "rev-parse", commit+"^{tree}")
	return err == nil && strings.TrimSpace(got) == tree
}

// dropIssueStepsRef forgets a task's issue commits; failing to only leaves
// them reachable.
func dropIssueStepsRef(ctx context.Context, root string, session *ChangeSession) {
	if len(session.Issues) > 0 {
		_, _ = gitRead(ctx, root, "update-ref", "-d", issueStepsRef(session))
	}
}

// describeIssueCommits names the fixed issues' commits for a message.
func describeIssueCommits(session *ChangeSession) string {
	var parts []string
	for _, issue := range session.Issues {
		if issue.Status == IssueFixed {
			parts = append(parts, fmt.Sprintf("%s (%s)", issueCommitMessage(issue), issue.Commit[:min(len(issue.Commit), 8)]))
		}
	}
	return strings.Join(parts, "; ")
}
