package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func worktreeTaskRequest(t *testing.T, service *Service, paths ...string) ChangeOpenRequest {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return ChangeOpenRequest{
		ChangeCreateRequest: ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "fix", Title: "Fix: Run ignores errors!", Paths: paths, AcceptanceCriteria: []string{"Run reports print errors."},
			FindingTargets: []project.FindingTarget{{ProjectID: index.ProjectID, Kind: "semantic", ID: "finding:run", Path: "main.go"}}},
		ConfirmExecution: true,
	}
}

// ownTask opens a task in a new worktree of its own from service's checkout,
// as the API does, and returns the worktree's service and the task.
func ownTask(t *testing.T, service *Service, request ChangeCreateRequest) (*Service, *ChangeOpened) {
	t.Helper()
	opened, err := service.OpenChangeInWorktree(context.Background(), ChangeOpenRequest{ChangeCreateRequest: request, ConfirmExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	return child, opened
}

func TestTaskInItsOwnWorktreeStartsFromHEADWithTrustAndLinkedIssues(t *testing.T) {
	service, root, _ := gitWorkflowFixture(t)
	request := worktreeTaskRequest(t, service, "main.go", "main_test.go")
	opened, err := service.OpenChangeInWorktree(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(opened.Branch, "orca/fix-run-ignores-errors-") || filepath.Dir(opened.WorktreePath) != filepath.Join(root, ".worktrees") || len(opened.Warnings) != 0 {
		t.Fatalf("worktree = %+v", opened)
	}
	if got := testGit(t, opened.WorktreePath, "branch", "--show-current"); got != opened.Branch {
		t.Fatalf("worktree branch = %q", got)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	index, _ := child.manager.Index()
	session := opened.ChangeSession
	if session.ProjectID != index.ProjectID || session.ProjectRevision != index.ProjectRevision || session.Title != request.Title || len(session.FindingTargets) != 1 || session.FindingTargets[0] != request.FindingTargets[0] {
		t.Fatalf("task = %+v", session)
	}
	if stored, err := child.ChangeSession(context.Background(), session.ID); err != nil || stored.Freshness != "current" {
		t.Fatalf("task in the worktree = %+v, %v", stored, err)
	}
	if _, err := service.ChangeSession(context.Background(), session.ID); err == nil {
		t.Fatal("the task was also captured in the main checkout")
	}
	trust, err := child.ExecutionTrust(context.Background(), index.ProjectRevision, "")
	if err != nil || !trust.Trusted {
		t.Fatalf("the worktree did not inherit execution trust: %+v, %v", trust, err)
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("creating the task changed the main checkout: %s", got)
	}

	second, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go"))
	if err != nil || second.WorktreePath == opened.WorktreePath || second.Branch == opened.Branch {
		t.Fatalf("a second task on the same file did not get its own worktree: %+v, %v", second, err)
	}
}

func TestTaskWorktreeIsRefusedForUncommittedFilesAndLeavesNothingOnFailure(t *testing.T) {
	service, root, _ := gitWorkflowFixture(t)
	editMain(t, root, "package main\n// uncommitted user work\n")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go")); err == nil || !strings.Contains(err.Error(), "main.go has uncommitted changes") {
		t.Fatalf("a dirty task file was accepted: %v", err)
	}
	request := worktreeTaskRequest(t, service, "README.md")
	request.ConfirmExecution = false
	if _, err := service.OpenChangeInWorktree(context.Background(), request); err == nil {
		t.Fatal("a worktree was created without execution consent")
	}
	// The finding target names a file outside the scope, so capture fails
	// after the worktree exists: it and its branch are removed again.
	request = worktreeTaskRequest(t, service, "README.md")
	if _, err := service.OpenChangeInWorktree(context.Background(), request); err == nil || !strings.Contains(err.Error(), "finding targets") {
		t.Fatalf("an invalid task was captured: %v", err)
	}
	trees, err := readGitWorktrees(context.Background(), root)
	if err != nil || len(trees) != 1 {
		t.Fatalf("a failed task left a worktree: %+v, %v", trees, err)
	}
	if got := testGit(t, root, "branch", "--list", "orca/*"); got != "" {
		t.Fatalf("a failed task left its branch: %s", got)
	}
	if len(service.worktreeServices()) != 0 {
		t.Fatal("a failed task left its worktree service")
	}
}

func TestTaskWorktreeSlug(t *testing.T) {
	for title, want := range map[string]string{
		"Fix: Run ignores errors!": "fix-run-ignores-errors",
		"  ¿¿ ":                    "task",
		"Résumé parser":            "r-sum-parser",
		strings.Repeat("a", 60):    strings.Repeat("a", 24),
		"Fix: Request body read without a size limit": "fix-request-body-read",
	} {
		if got := taskWorktreeSlug(title); got != want {
			t.Fatalf("slug(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestMergeCanKeepTheWorktreeAndDiscardDropsABranchWithoutCommits(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	if err := os.WriteFile(filepath.Join(path, "main.go"), []byte("package main\nfunc Run() { println(\"worktree\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, path, "add", "main.go")
	testGit(t, path, "commit", "-m", "Worktree fix")
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead, request.KeepWorktree = path, testGit(t, path, "rev-parse", "HEAD"), true
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != "" || result.RemovedBranch != "" {
		t.Fatalf("merge = %+v, %v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the kept worktree was removed: %v", err)
	}
	if got := testGit(t, root, "rev-parse", "refs/heads/codex/fix"); got != request.SourceHead {
		t.Fatal("the kept branch was deleted")
	}

	// A discarded worktree whose branch the current branch contains loses
	// nothing: its branch goes too.
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead, request.SourceBranch = path, testGit(t, path, "rev-parse", "HEAD"), "codex/fix"
	discarded, err := service.DiscardGitWorktree(context.Background(), request)
	if err != nil || discarded.State != "discarded" || discarded.RemovedWorktree != path || discarded.RemovedBranch != "codex/fix" || len(discarded.Warnings) != 0 {
		t.Fatalf("discard = %+v, %v", discarded, err)
	}
	if _, err := gitRead(context.Background(), root, "show-ref", "--verify", "--quiet", "refs/heads/codex/fix"); !gitops.ExitCode(err, 1) {
		t.Fatalf("the integrated branch was kept: %v", err)
	}
}

func TestTaskWorktreeSharesSavedAgentSettings(t *testing.T) {
	service, _, _ := gitWorkflowFixture(t)
	control := settingsControl(t, service)
	control.Values.Roles.Review = "bug"
	control.Values.Limits.ModelCalls = 3
	if _, err := service.ControlAgentSettings(context.Background(), control); err != nil {
		t.Fatal(err)
	}
	opened, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go"))
	if err != nil || len(opened.Warnings) != 0 {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	view, err := child.AgentSettings(context.Background(), settingsQuery(t, child))
	if err != nil || view.State != "overridden" || view.Roles.Review != "bug" || view.Limits.ModelCalls != 3 {
		t.Fatalf("worktree settings = %+v, %v", view, err)
	}
}

func TestTaskWorktreeRollbackSurvivesACanceledRequestAndAFailedCheckout(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.discardTaskWorktree(canceled, root, path); err != nil {
		t.Fatalf("rollback stopped with the request: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the worktree was kept: %v", err)
	}

	// A required smudge filter that fails stops the checkout after Git made
	// the branch; the branch goes too.
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.go filter=broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".gitattributes")
	testGit(t, root, "commit", "-m", "Filter Go files")
	testGit(t, root, "config", "filter.broken.smudge", "false")
	testGit(t, root, "config", "filter.broken.required", "true")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	trustedGitRequest(t, service)
	if _, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go")); err == nil {
		t.Fatal("a failed checkout created the task")
	}
	if got := testGit(t, root, "branch", "--list", "orca/*"); got != "" {
		t.Fatalf("a failed checkout left its branch: %s", got)
	}
}
