package app

import (
	"context"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type GitBranch = gitops.GitBranch
type GitWorktree = gitops.GitWorktree
type GitWorkspace = gitops.GitWorkspace

func (s *Service) GitWorkspace(ctx context.Context, id, revision string) (*GitWorkspace, error) {
	repository, err := s.GitRepository(ctx, id, revision)
	if err != nil {
		return nil, err
	}
	result := &GitWorkspace{GitRepository: repository, Branches: []GitBranch{}, Worktrees: []GitWorktree{}}
	if !repository.Available {
		return result, nil
	}
	result.Worktrees, err = readGitWorktrees(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	s.releaseStaleRunLocks(ctx, repository.Root, result.Worktrees)
	for i := range result.Worktrees {
		gitops.PopulateWorktree(ctx, repository, &result.Worktrees[i], listChangeSessions)
	}
	result.Branches, err = gitops.ReadBranches(ctx, repository.Root, result.Worktrees)
	if err != nil {
		return nil, err
	}
	current, err := s.GitRepository(ctx, id, revision)
	if err != nil {
		return nil, err
	}
	if current.GitIdentity != repository.GitIdentity {
		return nil, project.ErrRevisionConflict
	}
	return result, nil
}

func readGitWorktrees(ctx context.Context, root string) ([]GitWorktree, error) {
	return gitops.ReadWorktrees(ctx, root)
}
