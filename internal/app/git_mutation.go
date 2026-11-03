package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type GitMutationRequest = gitops.GitMutationRequest
type GitMutationResult = gitops.GitMutationResult

func (s *Service) gitMutationPreflight(ctx context.Context, request GitMutationRequest, abort bool) (*GitRepository, error) {
	if !request.Confirm || !request.ConfirmExecution {
		return nil, fmt.Errorf("confirm the Git action and execution of repository filters or merge drivers")
	}
	repository, err := s.GitRepository(ctx, request.ProjectID, request.ProjectRevision)
	if err != nil {
		return nil, err
	}
	expected := *repository
	if abort {
		expected.MergeHead = ""
		expected.Conflicts = nil
	}
	if err := requireMatchingGitIdentity(&expected, request.GitIdentity); err != nil {
		return nil, err
	}
	if err := s.requireProjectExecutionTrust(ctx, request.ProjectRevision); err != nil {
		return nil, err
	}
	return repository, nil
}

func (s *Service) CreateGitWorktree(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	result, err := s.createGitWorktreeCheckout(ctx, request)
	if err != nil || result.State != "created" {
		return result, err
	}
	if err := s.initializeWorktreeInventory(ctx, result.Path); err != nil {
		result.Error = "Worktree created, but local initialization failed: " + err.Error()
	}
	return result, nil
}

func (s *Service) createGitWorktreeCheckout(ctx context.Context, request GitMutationRequest) (*GitMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	repository, err := s.gitMutationPreflight(ctx, request, false)
	if err != nil {
		return nil, err
	}
	if err := gitops.ValidateNewBranch(ctx, repository.Root, request.TargetBranch); err != nil {
		return nil, err
	}
	path, err := gitops.ValidateNewWorktreePath(ctx, repository.Root, request.Path)
	if err != nil {
		return nil, err
	}
	if err := gitops.PrepareWorktreeDirectory(ctx, repository.Root); err != nil {
		return nil, err
	}
	_, err = gitRead(ctx, repository.Root, "worktree", "add", "--no-track", "-b", request.TargetBranch, "--", path, repository.Head)
	result := &GitMutationResult{State: "created", Path: path, Repository: repository, Warnings: []string{}}
	if err != nil {
		result.State, result.Error = "failed", err.Error()
	}
	return result, nil
}

func (s *Service) initializeWorktreeInventory(ctx context.Context, path string) error {
	runtime := s.conf().runtimes.analyze
	analyzer := project.NewAnalyzerWithProvenance(nil, runtime.profile.Model, runtime.effective.Scope, runtime.effective.ProviderOrigin, runtime.effective.ReasoningEffort)
	return analyzer.StoreLocalInventory(ctx, path)
}

// Even a failed or canceled Git command may have written source. Reindex and
// return the observed outcome instead of presenting the attempt as a no-op.
func (s *Service) completeGitMutation(root, state string, cause error) *GitMutationResult {
	result := &GitMutationResult{State: state, Warnings: []string{}}
	if cause != nil {
		result.State, result.Error = "failed", cause.Error()
	}
	index, err := s.reindexActiveProject(stopReasonGit)
	if err != nil {
		result.Warnings = append(result.Warnings, "Refresh project facts: "+err.Error())
	} else {
		result.Index = index
	}
	repository, err := readGitRepository(context.Background(), root)
	if err != nil {
		result.Warnings = append(result.Warnings, "Refresh Git status: "+err.Error())
		return result
	}
	if index != nil {
		repository.ProjectID, repository.ProjectRevision = index.ProjectID, index.ProjectRevision
	}
	result.Repository = repository
	if len(repository.Conflicts) > 0 {
		result.State = "conflicts"
	}
	return result
}
