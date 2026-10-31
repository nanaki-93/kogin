package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type GitIdentity struct {
	Head   string `json:"head"`
	Branch string `json:"branch"`
}

type GitRepository struct {
	GitIdentity
	ProjectID       string   `json:"project_id"`
	ProjectRevision string   `json:"project_revision"`
	Available       bool     `json:"available"`
	Root            string   `json:"root"`
	AtRoot          bool     `json:"at_root"`
	Dirty           bool     `json:"dirty"`
	MergeHead       string   `json:"merge_head,omitempty"`
	Operation       string   `json:"operation,omitempty"`
	Conflicts       []string `json:"conflicts"`
}

func nonGitRepositoryError(err error) bool {
	var exit *exec.ExitError
	return errors.Is(err, exec.ErrNotFound) || (errors.As(err, &exit) && strings.Contains(strings.ToLower(err.Error()), "not a git repository"))
}

func ReadRepository(ctx context.Context, root string) (*GitRepository, error) {
	repository := &GitRepository{Conflicts: []string{}}
	top, err := Read(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		if nonGitRepositoryError(err) {
			return repository, nil
		}
		return nil, err
	}
	repository.Root, err = project.CanonicalRoot(strings.TrimSpace(top))
	if err != nil {
		return nil, err
	}
	repository.Available, repository.AtRoot = true, repository.Root == filepath.Clean(root)
	branch, err := Read(ctx, root, "branch", "--show-current")
	if err != nil {
		return nil, err
	}
	repository.Branch = strings.TrimSpace(branch)
	head, err := Read(ctx, root, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil && !ExitCode(err, 1) {
		return nil, err
	}
	repository.Head = strings.TrimSpace(head)
	status, err := Read(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	repository.Dirty = StatusDirty(status)
	unmerged, err := Read(ctx, root, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	if unmerged != "" {
		repository.Conflicts = strings.Split(strings.TrimSuffix(unmerged, "\x00"), "\x00")
	}
	if err := readGitOperation(ctx, root, repository); err != nil {
		return nil, err
	}
	return repository, nil
}

func readGitOperation(ctx context.Context, root string, repository *GitRepository) error {
	merge, err := Read(ctx, root, "rev-parse", "--verify", "-q", "MERGE_HEAD")
	if err == nil {
		repository.MergeHead = strings.TrimSpace(merge)
	} else if !ExitCode(err, 1) {
		return err
	}
	gitDir, err := Read(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	for _, name := range []string{"rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		_, err := os.Stat(filepath.Join(strings.TrimSpace(gitDir), name))
		if err == nil {
			repository.Operation = name
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func StatusDirty(status string) bool {
	for _, entry := range strings.Split(status, "\x00") {
		// Untracked private workflow metadata and worktrees are not user source changes.
		if entry == "" || strings.HasPrefix(entry, "?? .mini-orca/") || strings.HasPrefix(entry, "?? .worktrees/") {
			continue
		}
		return true
	}
	return false
}

func ExitCode(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

func RequireIdentity(repository *GitRepository, expected GitIdentity) error {
	if !repository.Available || !repository.AtRoot || repository.Branch == "" {
		return fmt.Errorf("open a Git worktree root on a local branch")
	}
	if expected.Head == "" || expected.Branch == "" || repository.GitIdentity != expected {
		return fmt.Errorf("%w: Git branch or HEAD changed; refresh and review again", project.ErrRevisionConflict)
	}
	if repository.MergeHead != "" || repository.Operation != "" || len(repository.Conflicts) != 0 {
		return fmt.Errorf("finish or abort the current Git operation first")
	}
	return nil
}

func CaptureChangeGit(ctx context.Context, root string) (*GitIdentity, error) {
	repository, err := ReadRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	if !repository.Available {
		return nil, nil
	}
	return &repository.GitIdentity, nil
}

// VerifyChangeBranch requires the checkout to stay on the captured branch;
// commits on it do not matter to a task whose freshness is path-scoped.
func VerifyChangeBranch(ctx context.Context, root string, expected *GitIdentity) error {
	if expected == nil {
		return nil
	}
	actual, err := CaptureChangeGit(ctx, root)
	if err != nil {
		return err
	}
	if actual == nil || actual.Branch != expected.Branch {
		return fmt.Errorf("%w: the checkout left branch %s since this proposal was made", project.ErrRevisionConflict, expected.Branch)
	}
	return nil
}

func VerifyChangeGit(ctx context.Context, root string, expected *GitIdentity) error {
	if expected == nil {
		return nil
	}
	actual, err := CaptureChangeGit(ctx, root)
	if err != nil {
		return err
	}
	if actual == nil || *actual != *expected {
		return project.ErrRevisionConflict
	}
	return nil
}
