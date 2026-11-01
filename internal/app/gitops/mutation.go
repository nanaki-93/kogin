package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type GitMutationRequest struct {
	GitIdentity
	ProjectID        string `json:"project_id"`
	ProjectRevision  string `json:"project_revision"`
	Confirm          bool   `json:"confirm"`
	ConfirmExecution bool   `json:"confirm_execution"`
	TargetBranch     string `json:"target_branch,omitempty"`
	Path             string `json:"path,omitempty"`
	SourceHead       string `json:"source_head,omitempty"`
	SourceBranch     string `json:"source_branch,omitempty"`
	MergeHead        string `json:"merge_head,omitempty"`
	// KeepWorktree keeps a merged worktree and its branch; by default a
	// merge removes both.
	KeepWorktree bool `json:"keep_worktree,omitempty"`
	// DiscardWork discards a worktree left with no tasks together with its
	// work: its uncommitted changes and its branch, commits the current
	// branch lacks included.
	DiscardWork bool `json:"discard_work,omitempty"`
}

type GitMutationResult struct {
	State string `json:"state"`
	// PreviousHead is the commit the branch was on before a merge, for the
	// checks of the merged result.
	PreviousHead    string                      `json:"previous_head,omitempty"`
	Path            string                      `json:"path,omitempty"`
	RemovedWorktree string                      `json:"removed_worktree,omitempty"`
	RemovedBranch   string                      `json:"removed_branch,omitempty"`
	Completed       []project.FindingCompletion `json:"completed,omitempty"`
	Repository      *GitRepository              `json:"repository,omitempty"`
	Index           *project.ProjectIndex       `json:"index,omitempty"`
	Error           string                      `json:"error,omitempty"`
	Warnings        []string                    `json:"warnings"`
}

func ValidateNewBranch(ctx context.Context, root, branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || len(branch) > 240 {
		return fmt.Errorf("provide a new local branch name of at most 240 bytes")
	}
	if _, err := Read(ctx, root, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	_, err := Read(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return fmt.Errorf("branch %s already exists; choose a new branch name", branch)
	}
	if !ExitCode(err, 1) {
		return err
	}
	return nil
}

func ValidateNewWorktreePath(ctx context.Context, root, path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("choose an absolute path for the new worktree")
	}
	if !filepath.IsAbs(path) {
		cleaned := filepath.Clean(path)
		if filepath.Dir(cleaned) == ".worktrees" {
			path = filepath.Join(root, cleaned)
		} else {
			return "", fmt.Errorf("choose an absolute path for the new worktree")
		}
	}
	path = filepath.Clean(path)
	parent := filepath.Join(root, ".worktrees")
	if filepath.Dir(path) != parent {
		return "", fmt.Errorf("create the worktree directly inside %s", parent)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return "", fmt.Errorf("worktree path must not already exist")
	}
	if info, err := os.Lstat(parent); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf(".worktrees must be a directory, not a symlink")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := validateWorktreeOverlap(ctx, root, path); err != nil {
		return "", err
	}
	return path, nil
}

func validateWorktreeOverlap(ctx context.Context, root, path string) error {
	trees, err := ReadWorktrees(ctx, root)
	if err != nil {
		return err
	}
	for _, tree := range trees {
		if tree.Current {
			continue // The dedicated .worktrees directory belongs to this checkout.
		}
		canonical, err := filepath.EvalSymlinks(tree.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(canonical, path)
		if err != nil {
			return err
		}
		if relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			// A selected checkout may itself live in an ancestor's ignored
			// .worktrees directory. Its own .worktrees remains isolated too.
			selected, err := filepath.Rel(canonical, root)
			if err == nil && strings.HasPrefix(selected, ".worktrees"+string(filepath.Separator)) {
				continue
			}
			return fmt.Errorf("create the worktree outside existing worktrees")
		}
	}
	return nil
}

func PrepareWorktreeDirectory(ctx context.Context, root string) error {
	if err := AddLocalExclude(ctx, root, "/.worktrees/"); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(root, ".worktrees"), 0755)
}

func AddLocalExclude(ctx context.Context, root, pattern string) error {
	common, err := Read(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	exclude := filepath.Join(strings.TrimSpace(common), "info", "exclude")
	if info, err := os.Lstat(filepath.Dir(exclude)); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("git info must be a directory, not a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if info, err := os.Lstat(exclude); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("git info/exclude must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(exclude)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !strings.Contains("\n"+string(data)+"\n", "\n"+pattern+"\n") {
		if err := os.MkdirAll(filepath.Dir(exclude), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(exclude, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteString("\n" + pattern + "\n")
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
