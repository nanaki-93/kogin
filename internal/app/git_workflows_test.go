package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app/gitops"
)

func gitWorkflowFixture(t *testing.T) (*Service, string, GitMutationRequest) {
	t.Helper()
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	initializeChangeGit(t, service, root)
	return service, service.manager.Root(), trustedGitRequest(t, service)
}
func trustedGitRequest(t *testing.T, service *Service) GitMutationRequest {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.GitRepository(context.Background(), index.ProjectID, index.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	return GitMutationRequest{GitIdentity: repository.GitIdentity, ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Confirm: true, ConfirmExecution: true}
}
func createWorkflowWorktree(t *testing.T, service *Service, request GitMutationRequest) string {
	t.Helper()
	request.TargetBranch = "codex/fix"
	request.Path = filepath.Join(service.manager.Root(), ".worktrees", "fix")
	result, err := service.CreateGitWorktree(context.Background(), request)
	if err != nil || result.State != "created" {
		t.Fatalf("create worktree: %+v %v", result, err)
	}
	return result.Path
}

func TestGitWorktreeCreationIsolationAndGuards(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	original, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// uncommitted user work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := createWorkflowWorktree(t, service, request)
	restored, err := service.RestoreProject(path)
	if err != nil || restored.Path != path || restored.AIStatus != "unavailable" {
		t.Fatalf("new worktree cannot reopen without a model: %+v %v", restored, err)
	}
	created, _ := os.ReadFile(filepath.Join(path, "main.go"))
	if string(created) != string(original) {
		t.Fatal("worktree copied dirty source")
	}
	if service.manager.Root() != root {
		t.Fatal("creation silently switched project")
	}
	workspace, err := service.GitWorkspace(context.Background(), request.ProjectID, request.ProjectRevision)
	if err != nil || len(workspace.Worktrees) != 2 {
		t.Fatalf("worktrees: %+v %v", workspace, err)
	}
	resolvedRel, err := gitops.ValidateNewWorktreePath(context.Background(), root, ".worktrees/rel-fix")
	if err != nil || resolvedRel != filepath.Join(root, ".worktrees", "rel-fix") {
		t.Fatalf("expected .worktrees/rel-fix to resolve inside root, got %q (%v)", resolvedRel, err)
	}
	for _, invalid := range []GitMutationRequest{
		{TargetBranch: "../bad", Path: filepath.Join(t.TempDir(), "bad")},
		{TargetBranch: "codex/inside", Path: filepath.Join(root, "nested")},
		{TargetBranch: "codex/rel-outside", Path: "nested/fix"},
		{TargetBranch: "codex/existing", Path: root},
		{TargetBranch: "codex/fix", Path: filepath.Join(t.TempDir(), "duplicate")},
	} {
		invalid.GitIdentity = request.GitIdentity
		invalid.ProjectID, invalid.ProjectRevision = request.ProjectID, request.ProjectRevision
		invalid.Confirm, invalid.ConfirmExecution = true, true
		if _, err := service.CreateGitWorktree(context.Background(), invalid); err == nil {
			t.Fatalf("accepted invalid request: %+v", invalid)
		}
	}
	request.ConfirmExecution = false
	request.Path = filepath.Join(t.TempDir(), "unconfirmed")
	request.TargetBranch = "codex/unconfirmed"
	if _, err := service.CreateGitWorktree(context.Background(), request); err == nil {
		t.Fatal("created without execution consent")
	}
}

func TestGitWorktreeRejectsSymlinkDirectoryWithoutSideEffects(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	request.TargetBranch = "codex/symlink"
	request.Path = filepath.Join(root, ".worktrees", "escape")
	if _, err := service.CreateGitWorktree(context.Background(), request); err == nil {
		t.Fatal("accepted symlinked worktree directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside project: %+v, %v", entries, err)
	}
	if got := testGit(t, root, "branch", "--list", request.TargetBranch); got != "" {
		t.Fatal("rejected request created a branch")
	}
}

func TestGitWorktreeRejectsSymlinkedGitExclusions(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	outside := t.TempDir()
	info := filepath.Join(root, ".git", "info")
	if err := os.Rename(info, filepath.Join(root, ".git", "info-backup")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	request.TargetBranch = "codex/symlink"
	request.Path = filepath.Join(root, ".worktrees", "fix")
	if _, err := service.CreateGitWorktree(context.Background(), request); err == nil {
		t.Fatal("accepted symlinked Git info directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside Git metadata: %+v, %v", entries, err)
	}
	if _, err := os.Stat(request.Path); !os.IsNotExist(err) {
		t.Fatal("failed exclusion guard created a worktree")
	}
}

func TestGitWorktreeCanBeCreatedFromAnExistingProjectWorktree(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, child)
	request.TargetBranch = "codex/second"
	request.Path = filepath.Join(path, ".worktrees", "second")
	created, err := child.CreateGitWorktree(context.Background(), request)
	if err != nil || created.State != "created" || created.Error != "" {
		t.Fatalf("create from linked checkout: %+v, %v", created, err)
	}
	if service.manager.Root() != root || child.manager.Root() != path || testGit(t, path, "branch", "--show-current") != "codex/fix" {
		t.Fatal("creating another worktree changed a selected checkout")
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("nested worktree dirtied the original checkout: %s", got)
	}
}

func TestGitMergePreviewSuccessAndConflictRecovery(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "conflict"}[conflict], func(t *testing.T) {
			service, root, request := gitWorkflowFixture(t)
			path := createWorkflowWorktree(t, service, request)
			if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main\nfunc Run() { println(\"worktree\") }\n"), 0644); err != nil {
				t.Fatal(err)
			}
			testGit(t, path, "add", "main.go")
			testGit(t, path, "commit", "-m", "Worktree fix")
			if conflict {
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() { println(\"current\") }\n"), 0644); err != nil {
					t.Fatal(err)
				}
				testGit(t, root, "add", "main.go")
				testGit(t, root, "commit", "-m", "Current branch change")
				if _, err := service.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			request = trustedGitRequest(t, service)
			request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
			preview, err := service.PreviewGitMerge(context.Background(), request)
			if err != nil || !strings.Contains(preview.Diff, "worktree") || preview.Target.Head != request.Head {
				t.Fatalf("preview: %+v %v", preview, err)
			}
			testGit(t, path, "commit", "--allow-empty", "-m", "New source commit")
			if _, err := service.MergeGitWorktree(context.Background(), request); err == nil {
				t.Fatal("merged a source head that changed after review")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("stale merge removed the worktree: %v", err)
			}
			request.SourceHead = testGit(t, path, "rev-parse", "HEAD")
			result, err := service.MergeGitWorktree(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !conflict {
				if result.State != "merged" || result.Repository.Head != request.SourceHead || result.RemovedWorktree != path || len(result.Warnings) != 0 {
					t.Fatalf("merge: %+v", result)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("merged worktree still exists: %v", err)
				}
				if _, err := gitRead(context.Background(), root, "show-ref", "--verify", "--quiet", "refs/heads/codex/fix"); !gitops.ExitCode(err, 1) || result.RemovedBranch != "codex/fix" {
					t.Fatalf("cleanup retained the source branch: %v", err)
				}
				trees, err := readGitWorktrees(context.Background(), root)
				if err != nil || len(trees) != 1 || !trees[0].Current {
					t.Fatalf("merged worktree remains registered: %+v, %v", trees, err)
				}
				return
			}
			if result.State != "conflicts" || len(result.Repository.Conflicts) != 1 || result.Repository.MergeHead == "" || result.RemovedWorktree != "" {
				t.Fatalf("conflict hidden: %+v", result)
			}
			if got := testGit(t, path, "rev-parse", "HEAD"); got != request.SourceHead {
				t.Fatal("conflict removed or changed the source worktree")
			}
			request = trustedGitRequest(t, service)
			request.MergeHead = result.Repository.MergeHead
			result, err = service.AbortGitMerge(context.Background(), request)
			if err != nil || result.State != "aborted" || result.Repository.MergeHead != "" || len(result.Repository.Conflicts) != 0 {
				t.Fatalf("abort: %+v %v", result, err)
			}
			restored, _ := os.ReadFile(filepath.Join(root, "main.go"))
			if !strings.Contains(string(restored), "current") {
				t.Fatal("abort lost pre-merge source")
			}
			if _, err := os.Stat(path); err != nil || result.RemovedWorktree != "" {
				t.Fatalf("abort removed the worktree: %v", err)
			}
		})
	}
}

func TestPassiveGitStatusDoesNotExecuteFilters(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	marker := filepath.Join(root, "filter-ran")
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.go filter=unsafe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "config", "filter.unsafe.clean", "touch '"+marker+"'; cat")
	testGit(t, root, "config", "filter.unsafe.required", "true")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// modified\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GitRepository(context.Background(), request.ProjectID, request.ProjectRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("passive status executed filter")
	}
	if gitops.StatusDirty("?? .mini-orca/index.json\x00?? .worktrees/fix-1/\x00") {
		t.Fatal("private metadata or worktree paths marked repository dirty")
	}
}

func TestGitWorktreeRequiresExecutionTrust(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	service.clearExecutionTrust()
	request.Path, request.TargetBranch = filepath.Join(t.TempDir(), "blocked"), "codex/blocked"
	if _, err := service.CreateGitWorktree(context.Background(), request); err == nil {
		t.Fatal("worktree bypassed project execution trust")
	}
	trees, err := readGitWorktrees(context.Background(), root)
	if err != nil || len(trees) != 1 {
		t.Fatalf("untrusted request created a worktree: %+v %v", trees, err)
	}
}

func TestGitWorktreeListsCompletedFixes(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	initializeChangeGit(t, service, root)
	path := createWorkflowWorktree(t, service, trustedGitRequest(t, service))
	analysis, err := service.RestoreProject(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RestoreActiveProject(path, analysis); err != nil {
		t.Fatal(err)
	}
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	receipt, err := service.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), GitIdentity: *session.Git, Confirm: true, CommitMessage: session.Title})
	if err != nil || receipt.Commit.State != "committed" {
		t.Fatalf("worktree apply: %+v %v", receipt, err)
	}
	workspace, err := service.GitWorkspace(context.Background(), receipt.ProjectID, receipt.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range workspace.Worktrees {
		if tree.Path == path {
			if len(tree.Completed) != 1 || tree.Completed[0].ID != session.ID {
				t.Fatalf("missing completed fix: %+v", tree)
			}
			return
		}
	}
	t.Fatal("active worktree missing")
}
