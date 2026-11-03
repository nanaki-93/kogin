package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/app/changes"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// Every task has its own worktree, and accepting it commits the reviewed
// proposal on the task's branch. Kogin no longer writes a proposal into the
// main checkout, so a task made there before (without a worktree) can be read
// and deleted, not accepted.

// ErrCheckoutTask refuses to accept a task made in the main checkout.
var ErrCheckoutTask = errors.New("this task was made in the main checkout, which Kogin no longer changes; delete it and create it again: every task now gets its own worktree")

// ApplyChange accepts the reviewed, checked proposal of a task in its own
// worktree as a commit on its branch.
func (s *Service) ApplyChange(ctx context.Context, id string, request ChangeApplyRequest) (*ChangeMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if !request.Confirm {
		return nil, fmt.Errorf("explicit Accept confirmation is required")
	}
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	if !s.changeApproved(session) {
		return nil, fmt.Errorf("review and check the current proposal before Accept")
	}
	if err := verifyChecksWorkspace(ctx, root, session); err != nil {
		return nil, err
	}
	if !ownsWorktree(session) {
		return nil, ErrCheckoutTask
	}
	return s.acceptWorktreeChange(ctx, root, session, request)
}

func (s *Service) changeApproved(session *ChangeSession) bool {
	return (session.Plan == nil || allChangeFilesReviewed(session)) && canHumanReviewProposal(session) && s.changeAuthority[session.ProjectID+"/"+session.ID] == "review:"+session.Hash && session.ReviewedHash == session.Hash && len(session.Checks) > 0 && requiredChecksPassed(session.Checks) && checksComplete(session.Checks)
}

// completeFindingTargets records the issues an accepted task fixed as
// completed by its commit; a failure leaves them in progress until merge.
func completeFindingTargets(root string, session *ChangeSession, commit *ChangeCommit) {
	if len(session.FindingTargets) == 0 || commit == nil || commit.State != "committed" || commit.OID == "" {
		return
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		return
	}
	var completions []project.FindingCompletion
	for _, target := range session.FindingTargets {
		if target.ProjectID == session.ProjectID {
			completions = append(completions, project.FindingCompletion{FindingTarget: target, CommitOID: commit.OID})
		}
	}
	if len(completions) > 0 {
		_ = store.Complete(completions)
	}
}

func atomicWrite(path string, content []byte) error {
	return changes.AtomicWriteSource(path, content)
}

func contentHash(content []byte) string {
	return changes.ContentHash(content)
}
