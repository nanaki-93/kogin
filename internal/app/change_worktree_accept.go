package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// acceptWorktreeChange accepts a task in its own worktree: the reviewed
// proposal, already in the worktree's files, is committed on the task's
// branch on top of its base. Nothing is written to the files; discarding the
// worktree drops the task. The caller holds the locks and has checked
// approval.
func (s *Service) acceptWorktreeChange(ctx context.Context, root string, session *ChangeSession, request ChangeApplyRequest) (*ChangeMutationResult, error) {
	message := strings.TrimSpace(request.CommitMessage)
	if message == "" {
		message = session.Title
	}
	if err := validateCommitMessage(message); err != nil {
		return nil, err
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return nil, err
	}
	oid, warning, err := commitWorktreeProposal(ctx, root, session, message)
	if err != nil {
		return nil, err
	}
	commit := &ChangeCommit{GitIdentity: GitIdentity{Head: session.Worktree.Base, Branch: session.Worktree.Branch}, State: "committed", OID: oid, Message: message}
	session.State, session.Commit, session.UpdatedAt = "applied", commit, time.Now().UTC()
	result := &ChangeMutationResult{SessionID: session.ID, ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, State: session.State, Hash: session.Hash, Commit: commit, Warnings: []string{}}
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	if err := storeChangeSession(root, session); err != nil {
		result.Warnings = append(result.Warnings, "Committed as "+oid+", but the task history could not be updated: "+err.Error())
	}
	completeFindingTargets(root, session, commit)
	delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
	s.publishEvent(Event{Kind: "workflow", SessionID: session.ID, Title: session.Title, Status: "applied"})
	return result, nil
}

// commitWorktreeProposal builds the commit from the base commit and the
// reviewed proposal in a private index, on top of the commits of the issues
// the task fixed one at a time (when it adds anything to them), then moves
// the task branch from the base to it only if nothing else moved it, and
// finally lines the worktree's own index up with the new commit.
func commitWorktreeProposal(ctx context.Context, root string, session *ChangeSession, message string) (string, string, error) {
	base := session.Worktree.Base
	tree, err := worktreeProposalTree(ctx, root, session)
	if err != nil {
		return "", "", err
	}
	parent, warning := acceptParent(ctx, root, session)
	oid := parent
	if parent == base || !sameTree(ctx, root, parent, tree) {
		created, err := gitCommand(ctx, root, "", strings.NewReader(message+"\n"), "commit-tree", tree, "-p", parent)
		if err != nil {
			return "", "", err
		}
		oid = strings.TrimSpace(created)
	}
	if _, err := gitRead(ctx, root, "update-ref", "-m", "kogin: "+strings.Split(message, "\n")[0], "refs/heads/"+session.Worktree.Branch, oid, base); err != nil {
		return "", "", fmt.Errorf("%w: the task branch moved while it was accepted: %v", project.ErrRevisionConflict, err)
	}
	dropIssueStepsRef(ctx, root, session)
	if _, err := gitRead(ctx, root, "reset", "--quiet"); err != nil {
		return oid, warning, fmt.Errorf("committed as %s; refresh the worktree's staging area with git reset: %w", oid, err)
	}
	return oid, warning, nil
}

func stageWorktreeEdit(ctx context.Context, root, index string, session *ChangeSession, edit ChangeEdit) error {
	if edit.OldPath != "" {
		if _, err := gitCommand(ctx, root, index, nil, "update-index", "--force-remove", "--", edit.OldPath); err != nil {
			return err
		}
	}
	if edit.Deleted() {
		_, err := gitCommand(ctx, root, index, nil, "update-index", "--force-remove", "--", edit.Path)
		return err
	}
	blob, err := gitCommand(ctx, root, index, strings.NewReader(edit.Content), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	mode := uint32(0o644)
	if target := changeTarget(session, edit.Path); target != nil && target.Exists {
		mode = target.Mode
	} else if full, err := project.ResolveWritePath(root, edit.Path); err == nil {
		if info, err := os.Stat(full); err == nil {
			mode = uint32(info.Mode().Perm())
		}
	}
	_, err = gitCommand(ctx, root, index, nil, "update-index", "--add", "--cacheinfo", gitFileMode(mode), strings.TrimSpace(blob), edit.Path)
	return err
}
