package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// newTaskIn finds the task titled title in the worktrees of service.
func newTaskIn(t *testing.T, service *Service, title string) (*Service, *ChangeSession) {
	t.Helper()
	trees, err := readGitWorktrees(context.Background(), service.manager.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		history, _ := listChangeSessions(tree.Path)
		for _, entry := range history {
			if entry.Title == title {
				child, _, err := service.WorktreeService(context.Background(), tree.Path)
				if err != nil {
					t.Fatal(err)
				}
				session, err := child.ChangeSession(context.Background(), entry.ID)
				if err != nil {
					t.Fatal(err)
				}
				return child, session
			}
		}
	}
	t.Fatalf("no task %q", title)
	return nil, nil
}

func TestATaskWaitsForAnotherChangingTheSameCodeAndContinuesAfterIt(t *testing.T) {
	service, root, _ := workflowService(t, [3]func() string{
		func() string { return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	t.Cleanup(func() { _ = service.ShutdownChangeWorkflows(context.Background()) })
	initializeChangeGit(t, service, root)
	request := trustedGitRequest(t, service)
	create := func(title string) (*Service, *ChangeOpened) {
		return ownTask(t, service, ChangeCreateRequest{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, Kind: "feature", Title: title, Paths: []string{"new.go", "new_test.go"}, AcceptanceCriteria: []string{"Preserve existing behavior."}})
	}
	first, a := create("Add Helper")
	if _, err := first.StartChangeWorkflow(context.Background(), a.ID, workflowRequest(a.ChangeSession)); err != nil {
		t.Fatal(err)
	}
	ahead := finishedWorkflow(t, first, a.ID)
	if ahead.Workflow.Status != "awaiting_human_review" || len(ahead.Changes) == 0 {
		t.Fatalf("first task = %s, %d changes", ahead.Workflow.Status, len(ahead.Changes))
	}

	second, b := create("Add Helper too")
	waiting, err := second.StartChangeWorkflow(context.Background(), b.ID, workflowRequest(b.ChangeSession))
	if err != nil {
		t.Fatal(err)
	}
	w := waiting.Workflow
	if w.Status != "waiting" || w.Waiting == nil || w.Waiting.TaskID != a.ID || !slices.Equal(w.Waiting.Files, []string{"new.go"}) || w.Pending == nil || w.Pending.Message != workflowRequest(b.ChangeSession).Message {
		t.Fatalf("second task = %s (%s), wait %+v, pending %+v", w.Status, w.Reason, w.Waiting, w.Pending)
	}
	if !strings.HasPrefix(w.Reason, "Waiting for “Add Helper”, which is changing new.go.") {
		t.Fatalf("reason = %s", w.Reason)
	}
	// A waiting task never holds up another: the first may run again.
	if wait, err := first.blockingTask(context.Background(), a.ID, []string{"new.go"}); err != nil || wait != nil {
		t.Fatalf("the waiting task blocked the one it waits for: %+v %v", wait, err)
	}

	// Deleting the first task lets the second continue on its own.
	if err := first.DeleteChange(context.Background(), a.ID, changeIdentity(ahead)); err != nil {
		t.Fatal(err)
	}
	service.resumeWG.Wait()
	if _, err := loadChangeSession(b.WorktreePath, b.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the waiting task was kept: %v", err)
	}
	if _, err := os.Stat(b.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("the waiting task's worktree was kept: %v", err)
	}
	child, continued := newTaskIn(t, service, "Add Helper too")
	finished := finishedWorkflow(t, child, continued.ID)
	if finished.Workflow.Status != "awaiting_human_review" || !strings.Contains(allMessages(finished), "Continued on the updated code after “Add Helper” was merged or deleted") {
		t.Fatalf("continued task = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
}

func allMessages(session *ChangeSession) string {
	var text strings.Builder
	for _, message := range session.Messages {
		text.WriteString(message.Content + "\n")
	}
	return text.String()
}

func TestAContinuedTaskCarriesItsFixedIssuesUntilOneNoLongerApplies(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		coder := []fakePiStep{
			{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
			{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
			{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub."}},
			{Text: "Done.", Next: true},
			{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepSubSource, "new_text": stepMulSource}},
			{Tool: "create_file", Arguments: map[string]any{"path": "calc_mul_test.go", "content": stepMulTest}},
			{Tool: "finish", Arguments: map[string]any{"summary": "Added Mul."}},
			{Text: "Done."},
		}
		tester := []fakePiStep{{Tool: "finish", Arguments: map[string]any{"summary": "Covered."}}, {Text: "Done."}}
		flow := startToolFlowWith(t, coder, tester, nil, stepIssues("Sub is missing", "Mul is missing"))
		old := flow.finished(t)
		main := flow.service.owner
		mainRoot := main.manager.Root()
		// The main checkout moved on while the task waited.
		change, file := "# Notes\n", "NOTES.md"
		if conflict {
			change, file = strings.Replace(toolCalcSource, "// Add adds.", "// Add adds two numbers.", 1)+"\n// Div divides.\nfunc Div(a, b int) int { return a / b }\n", "calc.go"
		}
		if err := os.WriteFile(filepath.Join(mainRoot, file), []byte(change), 0o644); err != nil {
			t.Fatal(err)
		}
		testGit(t, mainRoot, "add", file)
		testGit(t, mainRoot, "commit", "-q", "-m", "Main moves on")
		if _, err := main.Reindex(); err != nil {
			t.Fatal(err)
		}
		trustedGitRequest(t, main)
		index, err := main.manager.Index()
		if err != nil {
			t.Fatal(err)
		}
		opened, err := main.OpenChangeInWorktree(context.Background(), ChangeOpenRequest{ChangeCreateRequest: waitingTaskRequest(mainRoot, old, index), ConfirmExecution: true})
		if err != nil {
			t.Fatal(err)
		}
		child, _, err := main.WorktreeService(context.Background(), opened.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		carried, err := child.carryIssueCommits(context.Background(), opened.ID, old)
		session, loadErr := child.ChangeSession(context.Background(), opened.ID)
		if err != nil || loadErr != nil {
			t.Fatalf("carry: %v %v", err, loadErr)
		}
		calc, _ := os.ReadFile(filepath.Join(opened.WorktreePath, "calc.go"))
		if conflict {
			if carried != 0 || len(session.Changes) != 0 || session.Issues[0].Status != IssuePending || string(calc) != change {
				t.Fatalf("a fix that no longer applies was carried: %d, %+v, %q", carried, session.Issues, calc)
			}
			continue
		}
		if carried != 2 || session.Issues[0].Status != IssueFixed || session.Issues[1].Status != IssueFixed || string(calc) != strings.Replace(toolCalcSource, stepAddSource, stepMulSource, 1) {
			t.Fatalf("carried %d, issues %+v, calc %q", carried, session.Issues, calc)
		}
		if log := testGit(t, opened.WorktreePath, "log", "--format=%s", session.Worktree.Base+".."+issueStepsRef(session)); log != "fix: mul is missing\nfix: sub is missing" {
			t.Fatalf("carried commits:\n%s", log)
		}
		if _, err := os.Stat(filepath.Join(opened.WorktreePath, "NOTES.md")); err != nil {
			t.Fatalf("the continued task is not on the updated code: %v", err)
		}
		if head := testGit(t, opened.WorktreePath, "rev-parse", "HEAD"); head != session.Worktree.Base {
			t.Fatalf("the worktree left its base: %s", head)
		}
	}
}

func TestAFixWaitsWhenItsScopeWouldReachAnotherTasksCode(t *testing.T) {
	session, run := scopeTask(t)
	main := run.s.owner
	other, opened := ownTask(t, main, ChangeCreateRequest{ProjectID: session.FindingTargets[0].ProjectID, ProjectRevision: mustIndex(t, main).ProjectRevision, Kind: "feature", Title: "Rework the vault key", Paths: []string{"vault/vault.go"}, AcceptanceCriteria: []string{"Keys are derived once."}})
	// The other task was accepted on its branch and is not merged yet.
	testGit(t, opened.WorktreePath, "commit", "-q", "--allow-empty", "-m", "Rework the vault key")
	accepted := opened.ChangeSession
	accepted.State, accepted.Commit = "applied", &ChangeCommit{State: "committed", OID: testGit(t, opened.WorktreePath, "rev-parse", "HEAD")}
	if err := storeChangeSession(other.manager.Root(), accepted); err != nil {
		t.Fatal(err)
	}
	refusedTool(t, run, "widen_scope", map[string]any{"paths": []string{"vault/vault.go"}, "reason": "The key is derived there."}, "another task, “Rework the vault key”, is changing it")
	if wait := run.waiting(); wait == nil || wait.TaskID != opened.ID || !slices.Equal(wait.Files, []string{"vault/vault.go"}) {
		t.Fatalf("wait = %+v", wait)
	}
	if len(run.grantsMade()) != 0 {
		t.Fatal("a file another task changes was granted")
	}
}

func mustIndex(t *testing.T, service *Service) *project.ProjectIndex {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestARunThatMustWaitEndsWaitingWithItsRequest(t *testing.T) {
	wait := &ChangeWorkflowWait{TaskID: "change-a", Title: "Rework the vault key", WorktreePath: "/w/a", Files: []string{"vault/vault.go"}}
	err := errors.Join(&taskWaitError{wait: wait}, nil)
	if status, reason := workflowFailure(err, nil); status != "waiting" || !strings.HasPrefix(reason, "Waiting for “Rework the vault key”, which is changing vault/vault.go.") {
		t.Fatalf("%s: %s", status, reason)
	}
	w := &ChangeWorkflow{Status: "waiting", Waiting: wait, Pending: pendingRun(ChangeWorkflowRequest{Message: "Fix it.", ConfirmedProfiles: []string{"bug"}})}
	if err := validateWorkflowWait(w); err != nil {
		t.Fatal(err)
	}
	w.Status = "failed"
	settleWorkflowWait(w)
	if w.Waiting != nil || w.Pending != nil || validateWorkflowWait(w) != nil {
		t.Fatalf("a run that no longer waits kept its wait: %+v", w)
	}
	if validateWorkflowWait(&ChangeWorkflow{Status: "waiting"}) == nil {
		t.Fatal("a waiting run without its wait was accepted")
	}
}
