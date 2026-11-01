package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitDiscardRemovesUnignoredMetadataAfterStoppingImplementation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	service, root, _ := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	t.Cleanup(func() { close(release) })
	initializeChangeGit(t, service, root)
	root = service.manager.Root()
	testGit(t, root, "rm", ".gitignore")
	testGit(t, root, "commit", "-m", "Leave private metadata untracked")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	request := trustedGitRequest(t, service)
	path := createWorkflowWorktree(t, service, request)
	testGit(t, path, "commit", "--allow-empty", "-m", "Completed fix")
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, child, "new.go", "new_test.go")
	if _, err := child.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := child.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("implementation did not start")
	}
	if got := testGit(t, path, "status", "--porcelain"); got != "?? .kasuri/" {
		t.Fatalf("expected unignored private metadata: %s", got)
	}
	request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
	if _, err := service.MergeGitWorktree(context.Background(), request); err == nil {
		t.Fatal("merge accepted a running task")
	}
	request.SourceBranch = "codex/fix"
	result, err := service.DiscardGitWorktree(context.Background(), request)
	if err != nil || result.State != "discarded" || result.RemovedWorktree != path || len(result.Warnings) != 0 {
		t.Fatalf("discard and close: %+v, %v", result, err)
	}
	child.changeWorkflow.mu.Lock()
	job := child.changeWorkflow.job
	child.changeWorkflow.mu.Unlock()
	select {
	case <-job.done:
	default:
		t.Fatal("worktree removed before worker cleanup completed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree directory survived cleanup: %v", err)
	}
	if len(service.worktreeServices()) != 0 {
		t.Fatal("removed worktree retained its worker")
	}
	if _, _, err := service.WorktreeService(context.Background(), path); err == nil {
		t.Fatal("removed implementation can still be opened")
	}
}

func TestGitMergeRetainsCheckoutContainingAnotherWorktree(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	testGit(t, path, "commit", "--allow-empty", "-m", "Completed fix")
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	nested := createWorkflowWorktreeWithBranch(t, child, "codex/nested", "nested")
	request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.Repository.Head != request.SourceHead || result.RemovedWorktree != "" {
		t.Fatalf("merge with nested checkout: %+v, %v", result, err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "contains another registered worktree") {
		t.Fatalf("missing cleanup warning: %+v", result)
	}
	for _, retained := range []string{root, path, nested} {
		if got := testGit(t, retained, "rev-parse", "HEAD"); got != request.SourceHead {
			t.Fatalf("changed retained checkout %s: %s", retained, got)
		}
	}
}

func createWorkflowWorktreeWithBranch(t *testing.T, service *Service, branch, name string) string {
	t.Helper()
	request := trustedGitRequest(t, service)
	request.TargetBranch, request.Path = branch, filepath.Join(service.manager.Root(), ".worktrees", name)
	result, err := service.CreateGitWorktree(context.Background(), request)
	if err != nil || result.State != "created" || result.Error != "" {
		t.Fatalf("create worktree: %+v, %v", result, err)
	}
	return result.Path
}

func TestGitMergeCleanupRechecksChangesMadeDuringMerge(t *testing.T) {
	for _, change := range []struct {
		name, script, warning string
	}{
		{"untracked", "printf 'user work' > .worktrees/fix/user.txt", "remaining changes"},
		{"tracked", "printf 'user work' >> .worktrees/fix/main.go", "remaining changes"},
		{"head", "git -C .worktrees/fix commit --allow-empty -m 'New source commit'", "state changed"},
		{"locked", "git worktree lock .worktrees/fix", "state changed"},
	} {
		t.Run(change.name, func(t *testing.T) {
			service, root, _ := gitWorkflowFixture(t)
			if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("main.go merge=change-source\n"), 0644); err != nil {
				t.Fatal(err)
			}
			testGit(t, root, "add", ".gitattributes")
			testGit(t, root, "commit", "-m", "Configure merge driver")
			if _, err := service.Reindex(); err != nil {
				t.Fatal(err)
			}
			path := createWorkflowWorktree(t, service, trustedGitRequest(t, service))
			for _, checkout := range []string{root, path} {
				content := fmt.Sprintf("package main\nfunc Run() { println(%q) }\n", filepath.Base(checkout))
				if err := os.WriteFile(filepath.Join(checkout, "main.go"), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				testGit(t, checkout, "add", "main.go")
				testGit(t, checkout, "commit", "-m", "Change Run")
			}
			testGit(t, root, "config", "merge.change-source.driver", change.script)
			if _, err := service.Reindex(); err != nil {
				t.Fatal(err)
			}
			request := trustedGitRequest(t, service)
			request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
			result, err := service.MergeGitWorktree(context.Background(), request)
			if err != nil || result.State != "merged" || result.Error != "" || result.RemovedWorktree != "" {
				t.Fatalf("merge: %+v, %v", result, err)
			}
			if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], change.warning) {
				t.Fatalf("missing cleanup warning: %+v", result)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("changed worktree was deleted: %v", err)
			}
			testGit(t, root, "merge-base", "--is-ancestor", request.SourceHead, "HEAD")
		})
	}
}
