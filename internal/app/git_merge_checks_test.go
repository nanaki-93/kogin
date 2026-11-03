package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// mergedBranch merges a worktree branch whose commit writes files, and
// returns the request that checks the merged result.
func mergedBranch(t *testing.T, files map[string]string) (*Service, MergeChecksRequest) {
	t.Helper()
	service, root := toolchainService(t, func() string { return "{}" }, map[string]string{
		"go.mod": "module example.test/merged\n\ngo 1.22\n", "add.go": strings.Replace(addSource, "a - b", "a + b", 1), "add_test.go": addTest, "broken_test.go": brokenTest,
		"web/Makefile": "test:\n\t@echo web tests ran\n", "web/app.ts": "export const answer = 41;\n",
	})
	initializeChangeGit(t, service, root)
	worktree := createWorkflowWorktree(t, service, trustedGitRequest(t, service))
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(worktree, filepath.FromSlash(name)), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, worktree, "add", "--", ".")
	testGit(t, worktree, "commit", "-m", "Branch work")
	request := trustedGitRequest(t, service)
	request.Path, request.SourceHead = worktree, testGit(t, worktree, "rev-parse", "HEAD")
	merged, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || merged.State != "merged" || merged.PreviousHead != request.Head || merged.Index == nil || merged.Repository == nil {
		t.Fatalf("merge = %+v, %v", merged, err)
	}
	return service, MergeChecksRequest{ProjectID: merged.Index.ProjectID, ProjectRevision: merged.Index.ProjectRevision, From: merged.PreviousHead, To: merged.Repository.Head}
}

func TestChecksAfterAMergeRunOnlyTheChangedSubprojects(t *testing.T) {
	service, request := mergedBranch(t, map[string]string{"web/app.ts": "export const answer = 42;\n"})
	result, err := service.CheckMerge(context.Background(), request)
	if err != nil || result.Outcome != "passed" || result.ChangedPaths != 1 {
		t.Fatalf("merge checks = %+v, %v", result, err)
	}
	if web := checkNamed(result.Checks, "tests: web"); web == nil || web.State != CheckPassed {
		t.Fatalf("web tests = %+v", result.Checks)
	}
	if root := checkNamed(result.Checks, "tests"); root != nil {
		t.Fatalf("the root tests ran for a merge that did not touch them: %+v", root)
	}
	if other := checkNamed(result.Checks, "other subprojects"); other == nil || !strings.Contains(other.Output, "the root project") {
		t.Fatalf("checks = %+v", result.Checks)
	}
}

func TestChecksAfterAMergeBlockOnlyOnFailuresTheMergeBrought(t *testing.T) {
	t.Run("known failure", func(t *testing.T) {
		service, request := mergedBranch(t, map[string]string{"add.go": "// Package main adds.\n" + strings.Replace(addSource, "a - b", "a + b", 1)})
		result, err := service.CheckMerge(context.Background(), request)
		tests := checkNamed(result.Checks, "tests")
		if err != nil || result.Outcome != "passed" || tests == nil || tests.Outcome != "known_failures" || !strings.Contains(tests.Output, "TestBroken") {
			t.Fatalf("merge checks = %+v, %v", result, err)
		}
	})
	t.Run("new failure", func(t *testing.T) {
		service, request := mergedBranch(t, map[string]string{"add.go": strings.Replace(addSource, "a - b", "a * b + 1", 1)})
		result, err := service.CheckMerge(context.Background(), request)
		tests := checkNamed(result.Checks, "tests")
		if err != nil || result.Outcome != "failed" || !strings.Contains(result.Summary, "Failed on the merged result: tests.") || tests == nil || !strings.Contains(tests.Output, "New failures: example.test/merged.TestAdd") {
			t.Fatalf("merge checks = %+v, %v", result, err)
		}
		if strings.Contains(testGit(t, service.manager.Root(), "status", "--porcelain"), "add.go") {
			t.Fatal("checking the merge changed the checkout")
		}
	})
}

func TestChecksAfterAMergeNeedTheCheckoutOnTheMergedCommit(t *testing.T) {
	service, request := mergedBranch(t, map[string]string{"web/app.ts": "export const answer = 42;\n"})
	swapped := request
	swapped.From, swapped.To = request.To, request.From
	if _, err := service.CheckMerge(context.Background(), swapped); err == nil {
		t.Fatal("checked a merge the checkout is not on")
	}
	if _, err := service.CheckMerge(context.Background(), MergeChecksRequest{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, From: "--output=x", To: request.To}); err == nil {
		t.Fatal("accepted an option as a commit")
	}
	testGit(t, service.manager.Root(), "commit", "--allow-empty", "-m", "Later work")
	if _, err := service.CheckMerge(context.Background(), request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("after another commit = %v", err)
	}
}
