package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/mini-orca/v2/internal/app/gitops"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type GitMergePreview = gitops.GitMergePreview

func (s *Service) PreviewGitMerge(ctx context.Context, request GitMutationRequest) (*GitMergePreview, error) {
	repository, err := s.GitRepository(ctx, request.ProjectID, request.ProjectRevision)
	if err != nil {
		return nil, err
	}
	return gitops.PreviewMerge(ctx, repository, request)
}

// mergePreflight resolves the clean merge target and the requested source
// worktree; callers hold jobLifecycleMu.
func (s *Service) mergePreflight(ctx context.Context, request GitMutationRequest) (*GitRepository, *GitWorktree, error) {
	repository, err := s.gitMutationPreflight(ctx, request, false)
	if err != nil {
		return nil, nil, err
	}
	if err := gitops.RequireMergeTarget(repository); err != nil {
		return nil, nil, err
	}
	source, err := gitops.MergeSource(ctx, repository, request)
	if err != nil {
		return nil, nil, err
	}
	return repository, source, nil
}

func (s *Service) MergeGitWorktree(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	repository, source, err := s.mergePreflight(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := requireFinishedWorktreeTasks(source.Path); err != nil {
		return nil, err
	}
	completed, err := gitops.MergedFindingTargets(ctx, repository, source, listChangeSessions)
	if err != nil {
		return nil, err
	}
	s.invalidateWholeTreeJobs()
	_, err = gitRead(ctx, repository.Root, "merge", "--no-edit", "--no-stat", "--no-autostash", "--no-rerere-autoupdate", source.Head)
	result := s.completeGitMutation(repository.Root, "merged", err)
	if result.State == "merged" {
		result.PreviousHead = repository.Head
		s.completeMergedWorktree(ctx, repository.Root, source, completed, result, !request.KeepWorktree)
	}
	return result, nil
}

// CleanupMergedWorktree reconciles a merge completed outside Kasuri. It never
// attempts a merge; ancestry must prove integration before checkout/branch cleanup.
func (s *Service) CleanupMergedWorktree(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	repository, source, err := s.mergePreflight(ctx, request)
	if err != nil {
		return nil, err
	}
	if request.SourceBranch == "" || source.Branch != request.SourceBranch {
		return nil, project.ErrRevisionConflict
	}
	if err := requireFinishedWorktreeTasks(source.Path); err != nil {
		return nil, err
	}
	if err := gitops.ValidateWorktreeRemoval(ctx, repository.Root, source, gitops.WorktreeMerged); err != nil {
		return nil, err
	}
	completed, err := gitops.MergedFindingTargets(ctx, repository, source, listChangeSessions)
	if err != nil {
		return nil, err
	}
	s.invalidateWholeTreeJobs()
	result := s.completeGitMutation(repository.Root, "merged", nil)
	s.completeMergedWorktree(ctx, repository.Root, source, completed, result, true)
	return result, nil
}

func requireFinishedWorktreeTasks(path string) error {
	history, err := listChangeSessions(path)
	if err != nil {
		return err
	}
	for _, entry := range history {
		if !entry.Integrated() {
			return fmt.Errorf("worktree contains unfinished task %q; finish it or explicitly discard the worktree", entry.Title)
		}
	}
	return nil
}

// completeMergedWorktree marks the merged findings done, then removes the
// worktree and its branch unless they are kept.
func (s *Service) completeMergedWorktree(ctx context.Context, root string, source *GitWorktree, completed []project.FindingCompletion, result *GitMutationResult, remove bool) {
	if err := gitops.CompleteMergedFindings(ctx, root, source.Head, completed); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Merge completed, but findings could not be marked Done: %v. Worktree %s and its task history were retained.", err, source.Path))
		return
	}
	result.Completed = completed
	if !remove {
		return
	}
	if err := s.removeGitWorktree(ctx, root, source, gitops.WorktreeMerged); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Merge completed, but worktree %s was not removed: %v. Refresh Worktrees and retry cleanup after resolving the issue.", source.Path, err))
		return
	}
	result.RemovedWorktree = source.Path
	if err := gitops.DeleteMergedBranch(ctx, root, source.GitIdentity); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Merge and worktree removal completed, but branch %s was retained: %v. Inspect the branch in Git before deleting it.", source.Branch, err))
		return
	}
	result.RemovedBranch = source.Branch
}

func (s *Service) AbortGitMerge(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	repository, err := s.gitMutationPreflight(ctx, request, true)
	if err != nil {
		return nil, err
	}
	if repository.MergeHead == "" || repository.MergeHead != request.MergeHead {
		return nil, project.ErrRevisionConflict
	}
	s.invalidateWholeTreeJobs()
	_, err = gitRead(ctx, repository.Root, "merge", "--abort")
	return s.completeGitMutation(repository.Root, "aborted", err), nil
}
