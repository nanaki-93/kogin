package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
)

// A task can live in its own Git worktree: a new branch from the current HEAD,
// checked out under .worktrees/. Tasks in separate worktrees run, apply and
// commit without touching each other's files, and come back through a merge.

// ChangeOpenRequest captures a task in this checkout or, with Worktree, in a
// new branch and worktree created for it.
type ChangeOpenRequest struct {
	ChangeCreateRequest
	Worktree bool `json:"worktree,omitempty"`
	// ConfirmExecution accepts that checking out the new worktree can run
	// the repository's filters, as for any Git mutation.
	ConfirmExecution bool `json:"confirm_execution,omitempty"`
}

// ChangeOpened is a task captured in the worktree created for it. The task
// carries the worktree's project identity.
type ChangeOpened struct {
	*ChangeSession
	WorktreePath string   `json:"worktree_path"`
	Branch       string   `json:"branch"`
	Warnings     []string `json:"warnings"`
}

const taskBranchPrefix = "orca/"

// OpenChangeInWorktree creates a branch and worktree from the current HEAD and
// captures the task there. The worktree inherits this checkout's execution
// trust when it runs the same commands, and its saved agent settings. A task
// that cannot be captured leaves no worktree or branch behind, even when the
// request is canceled.
func (s *Service) OpenChangeInWorktree(ctx context.Context, request ChangeOpenRequest) (*ChangeOpened, error) {
	repository, err := s.taskWorktreeBase(ctx, request)
	if err != nil {
		return nil, err
	}
	path, branch, err := s.createTaskWorktree(ctx, repository, request)
	if err != nil {
		return nil, err
	}
	opened := &ChangeOpened{WorktreePath: path, Branch: branch, Warnings: []string{}}
	opened.ChangeSession, err = s.openChangeInTaskWorktree(ctx, opened, request.ChangeCreateRequest)
	if err != nil {
		if cleanup := s.discardTaskWorktree(ctx, repository.Root, path); cleanup != nil {
			return nil, fmt.Errorf("%w (the new worktree %s was kept: %v)", err, path, cleanup)
		}
		return nil, err
	}
	return opened, nil
}

// taskWorktreeBase admits the request: a valid task in a Git repository on a
// branch with a commit, whose files have no uncommitted changes here, since
// the new worktree starts from HEAD without them.
func (s *Service) taskWorktreeBase(ctx context.Context, request ChangeOpenRequest) (*GitRepository, error) {
	if err := validateChangeCreateRequest(request.ChangeCreateRequest); err != nil {
		return nil, err
	}
	repository, err := s.GitRepository(ctx, request.ProjectID, request.ProjectRevision)
	if err != nil {
		return nil, err
	}
	if !repository.Available || repository.Branch == "" || repository.Head == "" {
		return nil, fmt.Errorf("a task gets its own worktree only in a Git repository on a branch with a commit; create it in this checkout")
	}
	// Task paths are relative to the project root; the Git runner reads
	// every pathspec literally.
	status, err := gitRead(ctx, s.manager.Root(), append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=normal", "--"}, request.Paths...)...)
	if err != nil {
		return nil, fmt.Errorf("read the status of the task's files: %w", err)
	}
	if entry, _, _ := strings.Cut(status, "\x00"); len(entry) > 3 {
		return nil, fmt.Errorf("%s has uncommitted changes that a new worktree would not have; commit them, or create the task in this checkout", entry[3:])
	}
	return repository, nil
}

// createTaskWorktree names the branch after the task, with a random suffix
// that keeps names unique, and checks it out under .worktrees/.
func (s *Service) createTaskWorktree(ctx context.Context, repository *GitRepository, request ChangeOpenRequest) (string, string, error) {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", "", err
	}
	name := taskWorktreeSlug(request.Title) + "-" + hex.EncodeToString(suffix[:])
	branch := taskBranchPrefix + name
	result, err := s.CreateGitWorktree(ctx, GitMutationRequest{
		GitIdentity: repository.GitIdentity, ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision,
		Confirm: true, ConfirmExecution: request.ConfirmExecution, TargetBranch: branch, Path: filepath.Join(repository.Root, ".worktrees", name),
	})
	if err != nil {
		return "", "", err
	}
	if result.State != "created" {
		// git worktree add creates the branch before checking it out.
		if cleanup := deleteUnusedTaskBranch(ctx, repository.Root, branch, repository.Head); cleanup != nil {
			return "", "", fmt.Errorf("create the task's worktree: %s (branch %s was kept: %v)", result.Error, branch, cleanup)
		}
		return "", "", fmt.Errorf("create the task's worktree: %s", result.Error)
	}
	if result.Error != "" {
		if cleanup := s.discardTaskWorktree(ctx, repository.Root, result.Path); cleanup != nil {
			return "", "", fmt.Errorf("%s (the new worktree %s was kept: %v)", result.Error, result.Path, cleanup)
		}
		return "", "", fmt.Errorf("%s", result.Error)
	}
	return result.Path, branch, nil
}

// taskWorktreeSlug turns a task title into a short branch-safe name: its first
// words in lower-case letters and digits, joined by dashes, within 24 bytes.
func taskWorktreeSlug(title string) string {
	const limit = 24
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	slug := ""
	for _, word := range words {
		next := word
		if slug != "" {
			next = slug + "-" + word
		}
		if len(next) > limit {
			if slug == "" {
				slug = word[:limit]
			}
			break
		}
		slug = next
	}
	if slug == "" {
		return "task"
	}
	return slug
}

// openChangeInTaskWorktree captures the task in the new checkout under its
// own project identity, after sharing execution trust with it.
func (s *Service) openChangeInTaskWorktree(ctx context.Context, opened *ChangeOpened, request ChangeCreateRequest) (*ChangeSession, error) {
	child, _, err := s.WorktreeService(ctx, opened.WorktreePath)
	if err != nil {
		return nil, err
	}
	index, err := child.manager.Index()
	if err != nil {
		return nil, err
	}
	shared, err := s.shareExecutionTrust(ctx, child, request.ProjectRevision, index.ProjectRevision)
	if err != nil {
		return nil, fmt.Errorf("trust the new worktree to run checks: %w", err)
	}
	if !shared {
		opened.Warnings = append(opened.Warnings, "The new worktree runs other check commands than this checkout; allow them for it before starting the agents.")
	}
	if shared, err := s.shareAgentSettings(ctx, child); err != nil || !shared {
		opened.Warnings = append(opened.Warnings, "The new worktree uses the configured agent roles and budget, not the ones saved in this checkout's Settings; set them there before starting the agents.")
	}
	request.ProjectID, request.ProjectRevision = index.ProjectID, index.ProjectRevision
	return child.openChangeSession(ctx, request, true)
}

// shareExecutionTrust trusts a worktree created from this trusted checkout to
// run checks when it runs exactly the commands this checkout's trust covers.
// A different command scope needs its own consent.
func (s *Service) shareExecutionTrust(ctx context.Context, child *Service, revision, childRevision string) (bool, error) {
	trust, err := s.ExecutionTrust(ctx, revision, "")
	if err != nil || !trust.Trusted {
		return false, err
	}
	scope, err := child.ExecutionTrust(ctx, childRevision, "")
	if err != nil || !slices.EqualFunc(trust.Commands, scope.Commands, slices.Equal) {
		return false, err
	}
	_, err = child.TrustProjectExecution(ctx, childRevision, true)
	return err == nil, err
}

// shareAgentSettings gives a new task worktree the agent roles and budget
// saved in this checkout, so its runs use what Settings shows.
// Settings saved for another configuration are not shared.
func (s *Service) shareAgentSettings(ctx context.Context, child *Service) (bool, error) {
	store, _, err := s.agentJournal(s.manager.Root())
	if err != nil {
		return false, err
	}
	saved, err := store.Settings(ctx)
	if err != nil || saved.Values == nil {
		return err == nil, err
	}
	configuration, err := child.agentConfiguration(child.conf().agentRuntime)
	if err != nil || configuration != saved.Configuration {
		return false, err
	}
	child.jobLifecycleMu.Lock()
	defer child.jobLifecycleMu.Unlock()
	target, _, err := child.agentJournal(child.manager.Root())
	if err != nil {
		return false, err
	}
	current, err := target.Settings(ctx)
	if err != nil {
		return false, err
	}
	if _, err := target.SaveSettings(ctx, current.Version, configuration, saved.Values); err != nil {
		return false, err
	}
	return true, nil
}

// cleanupContext keeps a rollback running after the request that needed it
// was canceled, within a bound.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

// deleteUnusedTaskBranch deletes a branch made for a task worktree that was
// not created; it still points at the HEAD it was made from.
func deleteUnusedTaskBranch(ctx context.Context, root, branch, head string) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	if _, err := gitRead(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); gitops.ExitCode(err, 1) {
		return nil
	} else if err != nil {
		return err
	}
	return gitops.DeleteMergedBranch(ctx, root, gitops.GitIdentity{Branch: branch, Head: head})
}

// discardTaskWorktree removes a worktree created for a task that could not be
// captured, and its branch, which has no commits of its own.
func (s *Service) discardTaskWorktree(ctx context.Context, root, path string) error {
	ctx, cancel := cleanupContext(ctx)
	defer cancel()
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	trees, err := readGitWorktrees(ctx, root)
	if err != nil {
		return err
	}
	for i := range trees {
		if trees[i].Path == path {
			if err := s.removeGitWorktree(ctx, root, &trees[i], gitops.WorktreeDiscarded); err != nil {
				return err
			}
			return gitops.DeleteMergedBranch(ctx, root, trees[i].GitIdentity)
		}
	}
	return fmt.Errorf("the worktree is not registered")
}
