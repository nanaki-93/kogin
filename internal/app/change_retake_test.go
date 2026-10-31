package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// overtakenTask opens a fix of main.go in its own worktree; with merged, the
// main branch then commits its own change to main.go, as when another task
// on the same file is merged first.
func overtakenTask(t *testing.T, reply string, merged bool) (*Service, *Service, *ChangeOpened, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var calls atomic.Int32
	var prompt atomic.Value
	service, root := newSemanticAnalysisService(t, reanalysisProvider(t, &calls, &prompt, nil, reply), 0)
	initializeChangeGit(t, service, root)
	trustedGitRequest(t, service)
	opened, err := service.OpenChangeInWorktree(context.Background(), worktreeTaskRequest(t, service, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if merged {
		editMain(t, root, editedMain)
		testGit(t, root, "commit", "-am", "Merge another task on main.go")
		if _, err := service.Reindex(); err != nil {
			t.Fatal(err)
		}
	}
	return service, child, opened, &calls, &prompt
}

func retakeRequestFor(session *ChangeSession) ChangeRetakeRequest {
	return ChangeRetakeRequest{ChangeIdentity: changeIdentity(session), ConfirmExecution: true}
}

func TestATaskOnlyTheMainBranchLeftBehindIsRetaken(t *testing.T) {
	_, child, opened, calls, _ := overtakenTask(t, `{"still_valid":true,"reason":"Run still drops the error."}`, false)
	read, err := child.ChangeSession(context.Background(), opened.ID)
	if err != nil || read.MainChanged != nil {
		t.Fatalf("a task the main branch did not change = %+v, %v", read.MainChanged, err)
	}
	if _, err := child.RetakeChange(context.Background(), opened.ID, retakeRequestFor(read)); err == nil || !strings.Contains(err.Error(), "whose files the main branch changed") || calls.Load() != 0 {
		t.Fatalf("retake of a current task = %v after %d calls", err, calls.Load())
	}
}

func TestARetakenTaskThatIsStillValidStartsAgainFromTheMainBranch(t *testing.T) {
	service, child, opened, calls, prompt := overtakenTask(t, `{"still_valid":true,"reason":"Run still drops the error."}`, true)
	read, err := child.ChangeSession(context.Background(), opened.ID)
	if err != nil || read.MainChanged == nil || read.MainChanged.Branch != "main" || !slices.Equal(read.MainChanged.Paths, []string{"main.go"}) {
		t.Fatalf("main change = %+v, %v", read.MainChanged, err)
	}
	if stored, err := loadChangeSession(child.manager.Root(), opened.ID); err != nil || stored.MainChanged != nil {
		t.Fatalf("the main change was stored: %+v, %v", stored, err)
	}
	unconfirmed := retakeRequestFor(read)
	unconfirmed.ConfirmExecution = false
	if _, err := child.RetakeChange(context.Background(), opened.ID, unconfirmed); err == nil || calls.Load() != 0 {
		t.Fatalf("a retake without consent to the Git action = %v after %d calls", err, calls.Load())
	}

	result, err := child.RetakeChange(context.Background(), opened.ID, retakeRequestFor(read))
	if err != nil || result.Outcome != ReanalysisStillValid || result.Deleted != opened.ID || result.Opened == nil || len(result.Warnings) != 0 {
		t.Fatalf("retake = %+v, %v", result, err)
	}
	if text, _ := prompt.Load().(string); !strings.Contains(text, "main changed main.go since the task started") {
		t.Fatalf("the model was not told what the main branch changed: %s", text)
	}
	fresh := result.Opened
	if fresh.WorktreePath == opened.WorktreePath || fresh.Worktree.Base != testGit(t, service.manager.Root(), "rev-parse", "HEAD") {
		t.Fatalf("the task did not start again from the main branch: %+v", fresh.Worktree)
	}
	if fresh.Title != opened.Title || !slices.Equal(fresh.FindingTargets, opened.FindingTargets) || !strings.Contains(fresh.Messages[len(fresh.Messages)-1].Content, "Retaken from "+opened.Branch) {
		t.Fatalf("the retaken task lost its intent: %+v", fresh.ChangeSession)
	}
	if target := changeTarget(fresh.ChangeSession, "main.go"); target == nil || target.Content != editedMain {
		t.Fatalf("the retaken task did not capture the main branch's main.go: %+v", target)
	}
	if _, err := loadChangeSession(child.manager.Root(), opened.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old task remains: %v", err)
	}
	if _, err := os.Stat(opened.WorktreePath); err != nil {
		t.Fatalf("the old worktree, left for the user to discard, is gone: %v", err)
	}
}

func TestARetakenTaskThatIsNoLongerValidIsDeletedWithItsIssuesDismissed(t *testing.T) {
	service, child, opened, _, _ := overtakenTask(t, `{"still_valid":false,"reason":"The merged change already returns the error."}`, true)
	read, err := child.ChangeSession(context.Background(), opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	worktrees := testGit(t, service.manager.Root(), "worktree", "list")
	result, err := child.RetakeChange(context.Background(), opened.ID, retakeRequestFor(read))
	if err != nil || result.Outcome != ReanalysisNotValid || result.Deleted != opened.ID || result.Opened != nil || result.DismissedIssues == 0 {
		t.Fatalf("retake = %+v, %v", result, err)
	}
	if after := testGit(t, service.manager.Root(), "worktree", "list"); after != worktrees {
		t.Fatalf("a task that is no longer valid got a worktree:\n%s", after)
	}
	if _, err := loadChangeSession(child.manager.Root(), opened.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the task remains: %v", err)
	}
}
