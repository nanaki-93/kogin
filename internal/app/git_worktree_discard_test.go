package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// abandonedWorktree is a worktree left with a commit the main branch lacks
// and uncommitted changes, as after a merge was aborted and its task deleted.
func abandonedWorktree(t *testing.T) (*Service, string, string, GitMutationRequest) {
	t.Helper()
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, path, "commit", "-am", "Work the main branch does not have")
	if err := os.WriteFile(filepath.Join(path, "notes.txt"), []byte("uncommitted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	request.Path, request.SourceHead, request.SourceBranch = path, testGit(t, path, "rev-parse", "HEAD"), "codex/fix"
	return service, root, path, request
}

func TestDiscardingAWorktreeWithItsWorkRemovesItAndItsBranch(t *testing.T) {
	service, root, path, request := abandonedWorktree(t)
	if _, err := service.DiscardGitWorktree(context.Background(), request); err == nil || !strings.Contains(err.Error(), "commit the worktree's remaining changes") {
		t.Fatalf("a plain discard took uncommitted changes: %v", err)
	}
	request.DiscardWork = true
	result, err := service.DiscardGitWorktree(context.Background(), request)
	if err != nil || result.State != "discarded" || result.RemovedWorktree != path || result.RemovedBranch != "codex/fix" || len(result.Warnings) != 0 {
		t.Fatalf("discard with its work = %+v, %v", result, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there: %v", err)
	}
	if branches := testGit(t, root, "branch", "--list", "codex/fix"); branches != "" {
		t.Fatalf("the branch is still there: %q", branches)
	}
}

func TestAWorktreeWithTasksKeepsItsWork(t *testing.T) {
	service, _, path, request := abandonedWorktree(t)
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	openChangeFixture(t, child, "main.go")
	request.DiscardWork = true
	if _, err := service.DiscardGitWorktree(context.Background(), request); err == nil || !strings.Contains(err.Error(), "delete the worktree's tasks") {
		t.Fatalf("a worktree with a task was discarded with its work: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the worktree is gone: %v", err)
	}
}

// The flow behind it: a task's committed fix conflicts with the main branch,
// the merge is aborted and the task deleted; the worktree and its branch,
// which main does not contain, then go only with its work.
func TestAWorktreeWhoseMergeWasAbortedIsDiscardedAfterItsTaskIsDeleted(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	for _, checkout := range []string{root, path} {
		if err := os.WriteFile(filepath.Join(checkout, "main.go"), []byte("package main\n// "+checkout+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		testGit(t, checkout, "commit", "-am", "Conflicting edit")
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
	merged, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || merged.State != "conflicts" {
		t.Fatalf("merge = %+v, %v", merged, err)
	}
	abort := trustedGitRequest(t, service)
	abort.MergeHead = merged.Repository.MergeHead
	if result, err := service.AbortGitMerge(context.Background(), abort); err != nil || result.State != "aborted" {
		t.Fatalf("abort = %+v, %v", result, err)
	}
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadChangeSession(path, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.DeleteChange(context.Background(), session.ID, changeIdentity(stored)); err != nil {
		t.Fatalf("delete the task: %v", err)
	}
	discard := trustedGitRequest(t, service)
	discard.Path, discard.SourceHead, discard.SourceBranch, discard.DiscardWork = path, testGit(t, path, "rev-parse", "HEAD"), "codex/fix", true
	result, err := service.DiscardGitWorktree(context.Background(), discard)
	if err != nil || result.State != "discarded" || result.RemovedWorktree != path || result.RemovedBranch != "codex/fix" || len(result.Warnings) != 0 {
		t.Fatalf("discard = %+v, %v", result, err)
	}
	if branches := testGit(t, root, "branch", "--list", "codex/fix"); branches != "" {
		t.Fatalf("the branch is still there: %q", branches)
	}
}
