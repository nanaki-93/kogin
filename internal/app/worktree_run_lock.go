package app

import (
	"context"
	"strings"
)

// runLockReason marks the Git lock Kasuri holds on a task's worktree
// while agents run there. A locked worktree cannot be merged, pruned or
// removed, by Kasuri or by git worktree.
const runLockReason = "kasuri: agents are running a task in this worktree"

// lockTaskWorktree locks the worktree of a task that owns it for the length
// of a workflow run and returns the unlock. A lock someone else set is left
// alone; a stale lock of Kasuri's is taken over.
func (s *Service) lockTaskWorktree(job *changeWorkflowJob) func() {
	s.changesMu.Lock()
	session, err := loadChangeSession(job.root, job.sessionID)
	s.changesMu.Unlock()
	if err != nil || !ownsWorktree(session) {
		return func() {}
	}
	ctx := context.Background()
	reason, locked := worktreeLock(ctx, job.root)
	if locked && !strings.HasPrefix(reason, "kasuri:") {
		return func() {}
	}
	if !locked {
		if _, err := gitRead(ctx, job.root, "worktree", "lock", "--reason", runLockReason, job.root); err != nil {
			return func() {}
		}
	}
	return func() {
		if reason, locked := worktreeLock(context.Background(), job.root); locked && reason == runLockReason {
			_, _ = gitRead(context.Background(), job.root, "worktree", "unlock", job.root)
		}
	}
}

// worktreeLock reads the lock on the worktree at root.
func worktreeLock(ctx context.Context, root string) (string, bool) {
	trees, err := readGitWorktrees(ctx, root)
	if err != nil {
		return "", false
	}
	for _, tree := range trees {
		if tree.Current {
			return tree.LockReason, tree.Locked
		}
	}
	return "", false
}

// releaseStaleRunLocks unlocks worktrees Kasuri locked for a run that no
// longer exists, such as after the daemon stopped mid-run.
func (s *Service) releaseStaleRunLocks(ctx context.Context, repositoryRoot string, trees []GitWorktree) {
	for i := range trees {
		tree := &trees[i]
		if !tree.Locked || tree.LockReason != runLockReason || s.worktreeRunActive(tree.Path) {
			continue
		}
		if _, err := gitRead(ctx, repositoryRoot, "worktree", "unlock", tree.Path); err == nil {
			tree.Locked, tree.LockReason = false, ""
		}
	}
}

// worktreeRunActive reports a workflow job running in the worktree at path.
func (s *Service) worktreeRunActive(path string) bool {
	s.worktreesMu.Lock()
	child := s.worktrees[path]
	s.worktreesMu.Unlock()
	if child == nil {
		return false
	}
	child.changeWorkflow.mu.Lock()
	defer child.changeWorkflow.mu.Unlock()
	return child.changeWorkflow.anyRunning()
}
