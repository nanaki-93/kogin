package gitops

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type GitMergePreview struct {
	Target  GitIdentity `json:"target"`
	Source  GitIdentity `json:"source"`
	Path    string      `json:"path"`
	Diff    string      `json:"diff"`
	Commits string      `json:"commits"`
}

type WorktreeRemoval string

const (
	WorktreeMerged    WorktreeRemoval = "merged"
	WorktreeDiscarded WorktreeRemoval = "discarded"
	// WorktreeAbandoned removes a worktree with its uncommitted changes.
	WorktreeAbandoned WorktreeRemoval = "abandoned"
)

var commitOIDPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func MergeSource(ctx context.Context, repository *GitRepository, request GitMutationRequest) (*GitWorktree, error) {
	return worktreeSource(ctx, repository, request, false)
}

// AbandonedSource is the worktree a discard throws away with its work: a
// merge source that may have uncommitted changes.
func AbandonedSource(ctx context.Context, repository *GitRepository, request GitMutationRequest) (*GitWorktree, error) {
	return worktreeSource(ctx, repository, request, true)
}

func worktreeSource(ctx context.Context, repository *GitRepository, request GitMutationRequest, dirty bool) (*GitWorktree, error) {
	trees, err := ReadWorktrees(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	for _, tree := range trees {
		if tree.Path != request.Path {
			continue
		}
		if tree.Head != request.SourceHead || request.SourceBranch != "" && tree.Branch != request.SourceBranch {
			return nil, project.ErrRevisionConflict
		}
		source, err := ReadRepository(ctx, tree.Path)
		if err != nil {
			return nil, err
		}
		source.Dirty = source.Dirty && !dirty
		if err := RequireMergeSource(&tree, source); err != nil {
			return nil, err
		}
		return &tree, nil
	}
	return nil, fmt.Errorf("choose an existing worktree from this repository")
}

func RequireMergeSource(tree *GitWorktree, source *GitRepository) error {
	if tree.Current || tree.Locked || tree.Prunable || tree.Branch == "" {
		return project.ErrRevisionConflict
	}
	if err := RequireIdentity(source, tree.GitIdentity); err != nil {
		return err
	}
	if source.Dirty {
		return fmt.Errorf("commit the worktree's remaining changes before merging or discarding it")
	}
	return nil
}

func RequireMergeTarget(repository *GitRepository) error {
	if err := RequireIdentity(repository, repository.GitIdentity); err != nil {
		return err
	}
	if repository.Dirty {
		return fmt.Errorf("commit or move local changes before merging")
	}
	return nil
}

// WorktreeMergeable is a passive guard, not a promise that Git will resolve conflicts.
func WorktreeMergeable(ctx context.Context, target *GitRepository, tree *GitWorktree, source *GitRepository) (bool, error) {
	if RequireMergeTarget(target) != nil || RequireMergeSource(tree, source) != nil {
		return false, nil
	}
	for _, entry := range tree.Sessions {
		if !entry.Integrated() {
			return false, nil
		}
	}
	if _, err := Read(ctx, target.Root, "merge-base", target.Head, source.Head); err != nil {
		if ExitCode(err, 1) {
			return false, nil
		}
		return false, err
	}
	_, err := Read(ctx, target.Root, "merge-base", "--is-ancestor", source.Head, target.Head)
	if ExitCode(err, 1) {
		return true, nil
	}
	return false, err
}

func PreviewMerge(ctx context.Context, repository *GitRepository, request GitMutationRequest) (*GitMergePreview, error) {
	if err := RequireIdentity(repository, request.GitIdentity); err != nil {
		return nil, err
	}
	source, err := MergeSource(ctx, repository, request)
	if err != nil {
		return nil, err
	}
	diff, err := Read(ctx, repository.Root, "diff", "--no-ext-diff", "--no-textconv", "--no-color", repository.Head+"..."+source.Head, "--")
	if err != nil {
		return nil, err
	}
	commits, err := Read(ctx, repository.Root, "log", "--format=%h %s", repository.Head+".."+source.Head, "--")
	if err != nil {
		return nil, err
	}
	return &GitMergePreview{Target: repository.GitIdentity, Source: source.GitIdentity, Path: source.Path, Diff: diff, Commits: commits}, nil
}

func ValidateWorktreeRemoval(ctx context.Context, root string, source *GitWorktree, removal WorktreeRemoval) error {
	repository, err := ReadRepository(ctx, root)
	if err != nil {
		return err
	}
	if err := RequireIdentity(repository, repository.GitIdentity); err != nil {
		return err
	}
	if removal == WorktreeMerged {
		if _, err := Read(ctx, root, "merge-base", "--is-ancestor", source.Head, repository.Head); err != nil {
			return fmt.Errorf("verify the reviewed commit is merged: %w", err)
		}
	}
	current, err := worktreeSource(ctx, repository, GitMutationRequest{Path: source.Path, SourceHead: source.Head}, removal == WorktreeAbandoned)
	if err != nil {
		return err
	}
	if current.GitIdentity != source.GitIdentity {
		return fmt.Errorf("the worktree branch changed after review")
	}
	trees, err := ReadWorktrees(ctx, root)
	if err != nil {
		return err
	}
	for _, tree := range trees {
		if tree.Path == source.Path {
			continue
		}
		relative, err := filepath.Rel(source.Path, tree.Path)
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the checkout contains another registered worktree at %s", tree.Path)
		}
	}
	return nil
}

// MergedFindingTargets captures finding completions before merging: merge drivers
// and later work must not change which findings the reviewed commits address.
func MergedFindingTargets(ctx context.Context, repository *GitRepository, source *GitWorktree, loadHistory func(string) ([]ChangeHistoryEntry, error)) ([]project.FindingCompletion, error) {
	history, err := loadHistory(source.Path)
	if err != nil {
		return nil, fmt.Errorf("read worktree finding targets: %w", err)
	}
	completed := []project.FindingCompletion{}
	for _, entry := range history {
		if len(entry.FindingTargets) == 0 || !entry.Integrated() {
			continue
		}
		if !commitOIDPattern.MatchString(entry.Commit.OID) {
			return nil, fmt.Errorf("invalid committed fix identity in %s", entry.ID)
		}
		_, err := Read(ctx, repository.Root, "merge-base", "--is-ancestor", entry.Commit.OID, source.Head)
		if ExitCode(err, 1) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("verify finding commit %s: %w", entry.Commit.OID, err)
		}
		for _, target := range entry.FindingTargets {
			if target.ProjectID == repository.ProjectID {
				completed = append(completed, project.FindingCompletion{FindingTarget: target, CommitOID: entry.Commit.OID})
			}
		}
	}
	return completed, nil
}

func CompleteMergedFindings(ctx context.Context, root, head string, completions []project.FindingCompletion) error {
	if len(completions) == 0 {
		return nil
	}
	if _, err := Read(ctx, root, "merge-base", "--is-ancestor", head, "HEAD"); err != nil {
		return fmt.Errorf("verify the reviewed commit was merged: %w", err)
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		return err
	}
	return store.Complete(completions)
}
