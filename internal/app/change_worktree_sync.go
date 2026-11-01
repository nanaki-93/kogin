package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// verifyWorktreeBase checks that a task's worktree is still on the task's
// branch at its base commit or, once accepted, at the commit that accepted
// it. Commits elsewhere, such as on the main branch, do not matter.
func verifyWorktreeBase(ctx context.Context, root string, session *ChangeSession) error {
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return err
	}
	head := session.Worktree.Base
	if session.Commit != nil && session.Commit.State == "committed" {
		head = session.Commit.OID
	}
	if repository.Branch != session.Worktree.Branch || repository.Head != head {
		return fmt.Errorf("%w: the task's worktree moved off its base commit on %s", project.ErrRevisionConflict, session.Worktree.Branch)
	}
	return nil
}

// verifyWorktreeProposal checks that the worktree's files still hold the
// stored proposal.
func verifyWorktreeProposal(ctx context.Context, root string, session *ChangeSession) error {
	if session.State != "draft" {
		return nil
	}
	read, err := readWorktreeProposal(ctx, root, session, session.Changes)
	if err != nil {
		// The task is out of date rather than unreadable.
		return fmt.Errorf("%w: %v", project.ErrRevisionConflict, err)
	}
	if !sameProposal(read, session.Changes) {
		return fmt.Errorf("%w: files in the task's worktree changed outside Kasuri", project.ErrRevisionConflict)
	}
	return nil
}

// sameProposal compares what proposals do to files, not their summaries.
func sameProposal(a, b []ChangeEdit) bool {
	if len(a) != len(b) {
		return false
	}
	byPath := map[string]ChangeEdit{}
	for _, edit := range b {
		byPath[edit.Path] = edit
	}
	for _, edit := range a {
		other, ok := byPath[edit.Path]
		if !ok || other.Hash != edit.Hash || editStatus(other) != editStatus(edit) || other.OldPath != edit.OldPath {
			return false
		}
	}
	return true
}

func editStatus(edit ChangeEdit) string {
	if edit.Status == "" {
		return EditModified
	}
	return edit.Status
}

// adoptWorktreeEdits makes edits made directly in a task's worktree (in an
// editor, by hand) part of its proposal: they become a new version, and
// checks, reviews and approval of the old one no longer apply. A worktree
// that moved off its base is left for the read to report as out of date.
func (s *Service) adoptWorktreeEdits(ctx context.Context, root string, session *ChangeSession) (*ChangeSession, error) {
	if _, changed := worktreeEditsToAdopt(ctx, root, session); !changed {
		return session, nil
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, err := loadChangeSession(root, session.ID)
	if err != nil || current.Revision != session.Revision || current.State != "draft" || isChangePipelineActive(current.Workflow) {
		return session, nil
	}
	read, changed := worktreeEditsToAdopt(ctx, root, current)
	if !changed {
		return session, nil
	}
	if err := ensureWorktreeTargets(ctx, root, current, read); err != nil {
		return nil, err
	}
	keepSummaries(read, current.Changes)
	current.Revision++
	current.Changes, current.Hash = read, changeProposalHash(read)
	current.Checks, current.PinnedTests = []CheckResult{}, nil
	revokeApproval(current)
	invalidateChangeWorkflow(current, "Files in the task's worktree changed. Run the checks and reviewers again before human review.")
	delete(s.changeAuthority, current.ProjectID+"/"+current.ID)
	now := time.Now().UTC()
	if len(current.Messages) < maxChangeMessages {
		current.Messages = append(current.Messages, ChangeMessage{Role: "assistant", Content: "Kasuri picked up edits made directly in this task's worktree; they are now part of the proposal.", CreatedAt: now})
	}
	current.UpdatedAt = now
	if err := storeChangeSession(root, current); err != nil {
		return nil, err
	}
	return current, nil
}

// worktreeEditsToAdopt reads the worktree's proposal and reports whether it
// differs from the stored one while the worktree is still at its base.
func worktreeEditsToAdopt(ctx context.Context, root string, session *ChangeSession) ([]ChangeEdit, bool) {
	if verifyWorktreeBase(ctx, root, session) != nil {
		return nil, false
	}
	read, err := readWorktreeProposal(ctx, root, session, session.Changes)
	if err != nil || sameProposal(read, session.Changes) {
		return nil, false
	}
	return read, true
}

// keepSummaries carries each unchanged file's summary and criteria over.
func keepSummaries(read, prior []ChangeEdit) {
	byPath := map[string]ChangeEdit{}
	for _, edit := range prior {
		byPath[edit.Path] = edit
	}
	for i := range read {
		if edit, ok := byPath[read[i].Path]; ok && edit.Hash == read[i].Hash {
			read[i].Summary, read[i].Criteria = edit.Summary, edit.Criteria
		}
	}
}
