package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const mainChangedNewGo = "main changed new.go since this task started. This task's work is built on that file as it was: re-analyze it against main and, if the issue is still there, start it again from main."

// commitOnMain commits new.go on the main checkout at root, as when another
// task on the same file is merged.
func commitOnMain(root string) error {
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n\nfunc Helper() int { return 3 }\n"), 0644); err != nil {
		return err
	}
	if _, err := gitRead(context.Background(), root, "add", "--", "new.go"); err != nil {
		return err
	}
	_, err := gitRead(context.Background(), root, "commit", "-m", "Merge another task on new.go")
	return err
}

// newGoTask opens a feature task on new.go in its own worktree.
func newGoTask(t *testing.T, service *Service, root string) (*Service, *ChangeOpened) {
	t.Helper()
	initializeChangeGit(t, service, root)
	request := trustedGitRequest(t, service)
	return ownTask(t, service, ChangeCreateRequest{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, Kind: "feature", Title: "Add Helper", Paths: []string{"new.go", "new_test.go"}, AcceptanceCriteria: []string{"Preserve existing behavior."}})
}

func TestAgentsDoNotStartOnATaskWhoseFilesTheMainBranchChanged(t *testing.T) {
	var calls atomic.Int32
	count := func(output string) func() string {
		return func() string { calls.Add(1); return output }
	}
	service, root, _ := workflowService(t, [3]func() string{count(workflowImplementation), count(workflowTests), count(workflowApproval)})
	t.Cleanup(func() { _ = service.ShutdownChangeWorkflows(context.Background()) })
	child, opened := newGoTask(t, service, root)
	if err := commitOnMain(root); err != nil {
		t.Fatal(err)
	}

	_, err := child.StartChangeWorkflow(context.Background(), opened.ID, workflowRequest(opened.ChangeSession))
	var changed *mainChangedError
	if !errors.Is(err, project.ErrRevisionConflict) || !errors.As(err, &changed) || changed.Error() != mainChangedNewGo {
		t.Fatalf("start = %v", err)
	}
	// The daemon's 409 carries the sentence after the conflict's prefix.
	if err.Error() != project.ErrRevisionConflict.Error()+": "+mainChangedNewGo {
		t.Fatalf("refusal reads %q", err.Error())
	}
	read, err := child.ChangeSession(context.Background(), opened.ID)
	if err != nil || read.Workflow != nil || read.MainChanged == nil || calls.Load() != 0 {
		t.Fatalf("refused start left workflow %+v, main change %+v, %d model calls, %v", read.Workflow, read.MainChanged, calls.Load(), err)
	}
}

func TestARunStopsAtItsNextStageWhenTheMainBranchChangesItsFiles(t *testing.T) {
	var mainRoot atomic.Value
	var commitOnce sync.Once
	var commitErr atomic.Value
	var reviews atomic.Int32
	create := func() string {
		// While the change is being written, another task on new.go is merged.
		commitOnce.Do(func() {
			if err := commitOnMain(mainRoot.Load().(string)); err != nil {
				commitErr.Store(err)
			}
		})
		return workflowImplementation
	}
	review := func() string { reviews.Add(1); return workflowApproval }
	service, root, _ := workflowService(t, [3]func() string{create, func() string { return workflowTests }, review})
	t.Cleanup(func() { _ = service.ShutdownChangeWorkflows(context.Background()) })
	mainRoot.Store(root)
	child, opened := newGoTask(t, service, root)

	if _, err := child.StartChangeWorkflow(context.Background(), opened.ID, workflowRequest(opened.ChangeSession)); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, child, opened.ID)
	if err, _ := commitErr.Load().(error); err != nil {
		t.Fatal(err)
	}
	w := finished.Workflow
	if w.Status != "stale" || w.Reason != mainChangedNewGo {
		t.Fatalf("run ended %s: %s", w.Status, w.Reason)
	}
	// The change was written; testing and review never started on it.
	if w.Stages[0].Status != "completed" || w.Stages[1].Status != "skipped" || w.Stages[2].Status != "skipped" || reviews.Load() != 0 {
		t.Fatalf("stages after the main branch moved: %+v, %d reviews", w.Stages, reviews.Load())
	}
	if finished.MainChanged == nil || finished.MainChanged.Paths[0] != "new.go" {
		t.Fatalf("main change = %+v", finished.MainChanged)
	}
}

func TestAMainBranchChangeNamesEachFileAndEndsTheRunAsOutOfDate(t *testing.T) {
	err := &mainChangedError{change: ChangeMainChange{Branch: "main", Paths: []string{"cmd/master.go", "cmd/root.go"}}}
	want := "main changed cmd/master.go, cmd/root.go since this task started. This task's work is built on those files as they were: re-analyze it against main and, if the issue is still there, start it again from main."
	if err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
	if status, reason := workflowFailure(errors.Join(project.ErrRevisionConflict, err), nil); status != "stale" || reason != want {
		t.Fatalf("%s: %s", status, reason)
	}
}

func TestAnIssueStepDoesNotStartOnCodeTheMainBranchReplaced(t *testing.T) {
	_, child, opened, calls, _ := overtakenTask(t, `{"still_valid":true,"reason":"unused"}`, true)
	session := opened.ChangeSession
	session.Issues = []ChangeIssue{{FindingID: "a", Path: "main.go", Title: "Run drops the error", Status: IssuePending}, {FindingID: "b", Path: "main.go", Title: "Run ignores its context", Status: IssuePending}}
	_, err := child.runIssueSteps(context.Background(), &changeWorkflowJob{root: opened.WorktreePath, sessionID: session.ID}, session, modelRuntime{})
	var changed *mainChangedError
	if !errors.As(err, &changed) || changed.change.Paths[0] != "main.go" || calls.Load() != 0 {
		t.Fatalf("step = %v after %d model calls", err, calls.Load())
	}
}
