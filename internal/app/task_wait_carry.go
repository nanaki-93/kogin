package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// carryIssueCommits applies the fixed issues of the task this one continues
// on top of its base, commit by commit and in order, until one does not
// apply cleanly to the updated code. The carried commits become this task's
// issue commits and their changes its proposal; the issues from the first
// that did not apply stay to be fixed by its run. It returns how many fixed
// issues it carried.
func (s *Service) carryIssueCommits(ctx context.Context, id string, old *ChangeSession) (int, error) {
	commits := fixedIssueCommits(old)
	if len(commits) == 0 {
		return 0, nil
	}
	root := s.manager.Root()
	session, err := s.ChangeSession(ctx, id)
	if err != nil || !ownsWorktree(session) {
		return 0, err
	}
	picked, err := cherryPickInWorktree(ctx, root, session.Worktree.Branch, commits)
	if err != nil || len(picked) == 0 {
		return 0, err
	}
	final := picked[len(picked)-1]
	diff, err := gitRead(ctx, root, "diff", "--binary", session.Worktree.Base, final, "--")
	if err == nil && diff != "" {
		_, err = gitCommand(ctx, root, "", strings.NewReader(diff), "apply", "--whitespace=nowarn", "-")
	}
	if err != nil {
		return 0, fmt.Errorf("apply the carried fixes: %w", err)
	}
	return s.adoptCarriedIssues(ctx, id, old, commits[:len(picked)], picked)
}

// fixedIssueCommits are the distinct commits of a task's fixed issues, in
// the order they were made.
func fixedIssueCommits(session *ChangeSession) []string {
	var commits []string
	for _, issue := range session.Issues {
		if issue.Status == IssueFixed && validCommitOID(issue.Commit) && (len(commits) == 0 || commits[len(commits)-1] != issue.Commit) {
			commits = append(commits, issue.Commit)
		}
	}
	return commits
}

// cherryPickInWorktree replays commits on the worktree's base with the
// worktree detached, stops at the first that does not apply, and puts the
// worktree back on its branch, unchanged. It returns the new commits.
func cherryPickInWorktree(ctx context.Context, root, branch string, commits []string) ([]string, error) {
	if _, err := gitRead(ctx, root, "checkout", "-q", "--detach"); err != nil {
		return nil, err
	}
	var picked []string
	for _, commit := range commits {
		if _, err := gitRead(ctx, root, "cherry-pick", "--allow-empty", "--keep-redundant-commits", commit); err != nil {
			_, _ = gitRead(ctx, root, "cherry-pick", "--abort")
			break
		}
		head, err := gitRead(ctx, root, "rev-parse", "--verify", "HEAD")
		if err != nil {
			break
		}
		picked = append(picked, strings.TrimSpace(head))
	}
	if _, err := gitRead(ctx, root, "checkout", "-q", branch); err != nil {
		return nil, fmt.Errorf("return the worktree to %s: %w", branch, err)
	}
	return picked, nil
}

// adoptCarriedIssues makes the worktree's carried changes the proposal and
// marks the issues of the carried commits fixed with their new commits.
func (s *Service) adoptCarriedIssues(ctx context.Context, id string, old *ChangeSession, commits, picked []string) (int, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return 0, err
	}
	edits, err := readWorktreeProposal(ctx, root, session, session.Changes)
	if err != nil {
		return 0, err
	}
	if err := ensureWorktreeTargets(ctx, root, session, edits); err != nil {
		return 0, err
	}
	session.Changes, session.Hash = edits, changeProposalHash(edits)
	session.Revision++
	carried := 0
	for _, issue := range old.Issues {
		at := slices.Index(commits, issue.Commit)
		if issue.Status != IssueFixed || at < 0 {
			continue
		}
		for i := range session.Issues {
			if session.Issues[i].FindingID == issue.FindingID {
				session.Issues[i].Status, session.Issues[i].Commit, session.Issues[i].Reason = IssueFixed, picked[at], issue.Reason
				carried++
			}
		}
	}
	if _, err := gitRead(ctx, root, "update-ref", "-m", "kogin: carried issue steps", issueStepsRef(session), picked[len(picked)-1]); err != nil {
		return 0, err
	}
	if err := storeChangeSession(root, session); err != nil {
		return 0, err
	}
	return carried, nil
}
