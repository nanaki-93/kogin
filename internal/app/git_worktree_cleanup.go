package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/brand"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

// The caller owns jobLifecycleMu, so no new worktree request can be routed while
// its worker is stopped and its checkout is removed. Git retains the branch.
func (s *Service) removeGitWorktree(ctx context.Context, root string, source *GitWorktree, removal gitops.WorktreeRemoval) error {
	s.worktreesMu.Lock()
	child := s.worktrees[source.Path]
	s.worktreesMu.Unlock()
	if child != nil {
		child.jobLifecycleMu.Lock()
		defer child.jobLifecycleMu.Unlock()
		if err := gitops.ValidateWorktreeRemoval(ctx, root, source, removal); err != nil {
			return err
		}
		cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := child.stopWorktreeJobs(cleanup); err != nil {
			return fmt.Errorf("wait for implementation cleanup: %w", err)
		}
		child.changesMu.Lock()
		defer child.changesMu.Unlock()
	}
	if err := gitops.ValidateWorktreeRemoval(ctx, root, source, removal); err != nil {
		return err
	}
	if removal == gitops.WorktreeMerged {
		if err := requireFinishedWorktreeTasks(source.Path); err != nil {
			return err
		}
	}
	// Private task history can be untracked even though the checkout is clean.
	// Ignore only that metadata so Git still refuses other uncommitted files.
	// A checkout made before the rename to Kogin may still hold .kasuri/ or
	// .mini-orca/.
	for _, folder := range brand.MetadataDirs() {
		if err := gitops.AddLocalExclude(ctx, root, "/"+folder+"/"); err != nil {
			return err
		}
	}
	remove := []string{"worktree", "remove"}
	if removal == gitops.WorktreeAbandoned {
		// Its uncommitted changes go with it; a locked worktree still stays.
		remove = append(remove, "--force")
	}
	if _, err := gitRead(ctx, root, append(remove, "--", source.Path)...); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(root, ".worktrees"))
	s.worktreesMu.Lock()
	delete(s.worktrees, source.Path)
	s.worktreesMu.Unlock()
	return nil
}

func (s *Service) stopWorktreeJobs(ctx context.Context) error {
	s.invalidateJobsForProjectChange(stopReasonWorktreeRemoved)
	if err := s.ShutdownChangeWorkflows(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Service) DiscardGitWorktree(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	repository, err := s.gitMutationPreflight(ctx, request, false)
	if err != nil {
		return nil, err
	}
	source, removal, err := discardSource(ctx, repository, request)
	if err != nil {
		return nil, err
	}
	if request.SourceBranch == "" || source.Branch != request.SourceBranch {
		return nil, fmt.Errorf("worktree branch changed; refresh Git status before discarding: %w", project.ErrRevisionConflict)
	}
	history, err := listChangeSessions(source.Path)
	if err != nil {
		return nil, fmt.Errorf("read discarded worktree findings: %w", err)
	}
	if request.DiscardWork && len(history) > 0 {
		return nil, fmt.Errorf("delete the worktree's tasks before discarding its work")
	}
	var targets []project.FindingTarget
	for _, entry := range history {
		targets = append(targets, entry.FindingTargets...)
	}
	result := &GitMutationResult{State: "discarded", Path: source.Path, Repository: repository, Warnings: []string{}}
	if err := s.removeGitWorktree(ctx, repository.Root, source, removal); err != nil {
		result.State, result.Error = "failed", err.Error()
		return result, nil
	}
	result.RemovedWorktree = source.Path
	if err := s.reopenDiscardedWorktreeFindings(ctx, repository.Root, repository.ProjectID, targets); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Worktree discarded, but linked issues could not be reopened: %v. Refresh Issues and retry their status update.", err))
	}
	deleteDiscardedBranch(ctx, repository.Root, source.GitIdentity, request.DiscardWork, result)
	s.scheduleResume()
	return result, nil
}

// discardSource is the worktree a discard removes: one whose work goes with
// it may have uncommitted changes.
func discardSource(ctx context.Context, repository *GitRepository, request GitMutationRequest) (*GitWorktree, gitops.WorktreeRemoval, error) {
	if request.DiscardWork {
		source, err := gitops.AbandonedSource(ctx, repository, request)
		return source, gitops.WorktreeAbandoned, err
	}
	source, err := gitops.MergeSource(ctx, repository, request)
	return source, gitops.WorktreeDiscarded, err
}

// deleteDiscardedBranch deletes a discarded worktree's branch: always when
// its work was discarded with it, otherwise only when it has no commits of
// its own.
func deleteDiscardedBranch(ctx context.Context, root string, source gitops.GitIdentity, abandoned bool, result *GitMutationResult) {
	deleted, err := true, error(nil)
	if abandoned {
		err = gitops.DeleteAbandonedBranch(ctx, root, source)
	} else {
		deleted, err = deleteBranchWithoutOwnCommits(ctx, root, source)
	}
	switch {
	case err != nil:
		result.Warnings = append(result.Warnings, fmt.Sprintf("Worktree discarded, but branch %s was retained: %v.", source.Branch, err))
	case deleted:
		result.RemovedBranch = source.Branch
	}
}

// deleteBranchWithoutOwnCommits deletes a discarded worktree's branch only
// when the current branch already contains its commits, so nothing is lost;
// a branch with commits of its own is kept.
func deleteBranchWithoutOwnCommits(ctx context.Context, root string, source gitops.GitIdentity) (bool, error) {
	_, err := gitRead(ctx, root, "merge-base", "--is-ancestor", source.Head, "HEAD")
	if gitops.ExitCode(err, 1) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, gitops.DeleteMergedBranch(ctx, root, source)
}

func (s *Service) reopenDiscardedWorktreeFindings(ctx context.Context, root, projectID string, targets []project.FindingTarget) error {
	if len(targets) == 0 {
		return nil
	}
	active := s.inProgressFindingTargets(ctx, root, projectID)
	stillActive := make(map[string]bool, len(active))
	for _, t := range active {
		stillActive[t.ID] = true
	}
	discarded := make(map[string]bool, len(targets))
	for _, t := range targets {
		if t.ProjectID == projectID && !stillActive[t.ID] {
			discarded[t.ID] = true
		}
	}
	if len(discarded) == 0 {
		return nil
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		return err
	}
	stored, err := store.Load(project.FindingInput{ProjectID: projectID})
	if err != nil {
		return err
	}
	for _, f := range stored {
		if f.Status == project.FindingStatusInProgress && discarded[f.ID] {
			if err := store.SetStatus(f.ID, project.FindingStatusOpen); err != nil {
				return err
			}
		}
	}
	return nil
}
