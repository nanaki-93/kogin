package app

import (
	"context"
	"io"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type GitIdentity = gitops.GitIdentity
type GitRepository = gitops.GitRepository

func (s *Service) GitRepository(ctx context.Context, id, revision string) (*GitRepository, error) {
	root := s.manager.Root()
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if index.ProjectID != id || index.ProjectRevision != revision {
		return nil, project.ErrRevisionConflict
	}
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	current, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if s.manager.Root() != root || current.ProjectID != id || current.ProjectRevision != revision {
		return nil, project.ErrRevisionConflict
	}
	repository.ProjectID, repository.ProjectRevision = id, revision
	return repository, nil
}

func readGitRepository(ctx context.Context, root string) (*GitRepository, error) {
	return gitops.ReadRepository(ctx, root)
}

func requireMatchingGitIdentity(repository *GitRepository, expected GitIdentity) error {
	return gitops.RequireIdentity(repository, expected)
}

func captureChangeGit(ctx context.Context, root string) (*GitIdentity, error) {
	return gitops.CaptureChangeGit(ctx, root)
}

func verifySessionGitIdentity(ctx context.Context, root string, expected *GitIdentity) error {
	return gitops.VerifyChangeGit(ctx, root, expected)
}

func verifySessionBranch(ctx context.Context, root string, expected *GitIdentity) error {
	return gitops.VerifyChangeBranch(ctx, root, expected)
}

func gitCommand(ctx context.Context, root, index string, input io.Reader, args ...string) (string, error) {
	return gitops.Command(ctx, root, index, input, args...)
}

func gitRead(ctx context.Context, root string, args ...string) (string, error) {
	return gitops.Read(ctx, root, args...)
}
