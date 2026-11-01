package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
)

func commitRecoveryEdit(t *testing.T, root, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "main.go")
	testGit(t, root, "commit", "-m", "Edit shared file")
	return testGit(t, root, "rev-parse", "HEAD")
}

func TestExternallyResolvedMergeCanCompleteFindingsAndCleanup(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	commitRecoveryEdit(t, root, "package main\n// First fix\n")
	head := commitRecoveryEdit(t, path, "package main\n// Second fix\n")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead, request.SourceBranch = path, head, "codex/fix"
	ws, err := service.GitWorkspace(context.Background(), request.ProjectID, request.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.Worktrees[1]
	if tree.Integration != "behind" || len(tree.OverlapPaths) != 1 || tree.OverlapPaths[0] != "main.go" {
		t.Fatalf("missing passive overlap evidence: %+v", tree)
	}
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "conflicts" || result.RemovedWorktree != "" {
		t.Fatalf("merge: %+v %v", result, err)
	}
	// The user resolves both fixes in their external tool and commits the merge.
	commitRecoveryEdit(t, root, "package main\n// First fix\n// Second fix\n")
	// Recovery explicitly updates local facts before granting trust for cleanup.
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead, request.SourceBranch = path, head, "codex/fix"
	ws, err = service.GitWorkspace(context.Background(), request.ProjectID, request.ProjectRevision)
	if err != nil || ws.Worktrees[1].Integration != "merged" || ws.Worktrees[1].Mergeable {
		t.Fatalf("external integration hidden: %+v %v", ws, err)
	}
	before := testGit(t, root, "rev-parse", "HEAD")
	result, err = service.CleanupMergedWorktree(context.Background(), request)
	if err != nil || result.RemovedWorktree != path || len(result.Warnings) != 0 {
		t.Fatalf("cleanup: %+v %v", result, err)
	}
	if result.Index == nil || result.Index.ProjectRevision != request.ProjectRevision {
		t.Fatal("cleanup did not return current project facts")
	}
	if testGit(t, root, "rev-parse", "HEAD") != before {
		t.Fatal("cleanup changed source history")
	}
	if _, err := gitRead(context.Background(), root, "show-ref", "--verify", "--quiet", "refs/heads/codex/fix"); err == nil || result.RemovedBranch != "codex/fix" {
		t.Fatal("cleanup retained the merged branch")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("merged checkout retained")
	}
	if len(service.worktreeServices()) != 0 {
		t.Fatal("removed worktree worker retained")
	}
	completed, err := service.FindingCompletions()
	if err != nil || len(completed) != 1 || completed[0].CommitOID != session.Commit.OID {
		t.Fatalf("findings not reconciled: %+v %v", completed, err)
	}
	// A subsequent task starts from the merged content, including both fixes.
	next := createWorkflowWorktreeWithBranch(t, service, "codex/next", "next")
	content, err := os.ReadFile(filepath.Join(next, "main.go"))
	if err != nil || string(content) != "package main\n// First fix\n// Second fix\n" {
		t.Fatalf("next task has stale base: %s %v", content, err)
	}
}

func TestCleanupRetainsUnfinishedPlanEvenWhenBaseIsIntegrated(t *testing.T) {
	service, _, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, child, "main.go")
	request.Path, request.SourceHead, request.SourceBranch = path, request.Head, "codex/fix"
	if _, err := service.CleanupMergedWorktree(context.Background(), request); err == nil {
		t.Fatal("cleanup discarded a pending task")
	}
	if _, err := loadChangeSession(path, session.ID); err != nil {
		t.Fatalf("pending task was lost: %v", err)
	}
	result, err := service.DiscardGitWorktree(context.Background(), request)
	if err != nil || result.RemovedWorktree != path || result.State != "discarded" {
		t.Fatalf("explicit discard of unfinished task was blocked: %+v %v", result, err)
	}
	// The branch had no commits of its own: the current branch keeps them all.
	if _, err := gitRead(context.Background(), service.manager.Root(), "show-ref", "--verify", "--quiet", "refs/heads/codex/fix"); !gitops.ExitCode(err, 1) || result.RemovedBranch != "codex/fix" {
		t.Fatalf("discard kept a branch without commits of its own: %+v %v", result, err)
	}
}

func TestCleanupMergedRejectsUnsafeOrUnintegratedWorktrees(t *testing.T) {
	for _, state := range []string{"unmerged", "stale source", "stale target", "stale branch", "dirty source", "dirty target", "locked", "unconfirmed", "untrusted", "active merge", "canceled"} {
		t.Run(state, func(t *testing.T) {
			service, root, request := gitWorkflowFixture(t)
			path, session := recordedFindingFix(t, service, request)
			if state != "unmerged" {
				testGit(t, root, "merge", "--ff-only", "codex/fix")
			}
			request = trustedGitRequest(t, service)
			request.Path, request.SourceHead, request.SourceBranch = path, session.Commit.OID, "codex/fix"
			ctx := context.Background()
			switch state {
			case "stale source":
				testGit(t, path, "commit", "--allow-empty", "-m", "new source")
			case "stale target":
				testGit(t, root, "commit", "--allow-empty", "-m", "new target")
			case "stale branch":
				request.SourceBranch = "other"
			case "dirty source":
				if err := os.WriteFile(filepath.Join(path, "user.txt"), []byte("retain"), 0644); err != nil {
					t.Fatal(err)
				}
			case "dirty target":
				if err := os.WriteFile(filepath.Join(root, "user.txt"), []byte("retain"), 0644); err != nil {
					t.Fatal(err)
				}
			case "locked":
				testGit(t, root, "worktree", "lock", path)
			case "unconfirmed":
				request.Confirm = false
			case "untrusted":
				service.clearExecutionTrust()
			case "active merge":
				if err := os.WriteFile(filepath.Join(root, ".git", "MERGE_HEAD"), []byte(session.Commit.OID+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := testGit(t, root, "rev-parse", "HEAD")
			if _, err := service.CleanupMergedWorktree(ctx, request); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("protected worktree removed")
			}
			if testGit(t, root, "rev-parse", "HEAD") != before {
				t.Fatal("rejected cleanup changed target")
			}
			if completions, err := service.FindingCompletions(); err != nil || len(completions) != 0 {
				t.Fatalf("rejected cleanup completed findings: %+v %v", completions, err)
			}
		})
	}
}
