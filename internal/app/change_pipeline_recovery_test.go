package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestChangeWorkflowPersistenceFailureRemainsVisibleAndRestartDoesNotDispatch(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { t.Error("tests dispatched after storage failed"); return workflowTests },
		func() string { t.Error("review dispatched after storage failed"); return workflowApproval },
	})
	started, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	directory, backup := filepath.Join(root, ".kogin", "changes"), filepath.Join(root, "saved-changes")
	if err := os.Rename(directory, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, directory); err != nil {
		t.Fatal(err)
	}
	unblock()
	<-s.changeWorkflow.job.done
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, directory); err != nil {
		t.Fatal(err)
	}
	failed, err := s.ChangeSession(context.Background(), initial.ID)
	if err != nil || failed.Workflow.Status != "failed" || !strings.Contains(failed.Workflow.Reason, "could not be saved") {
		t.Fatalf("storage failure became success: %+v, %v", failed, err)
	}
	if _, err := s.ReviewChange(context.Background(), initial.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(failed)}); err == nil {
		t.Fatal("storage failure authorized review")
	}
	// A crash can leave the last admitted record on disk, without an owner.
	started.Workflow.Stages[0].Status = "running"
	if err := storeChangeSession(root, started); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{ProjectID: initial.ProjectID, ProjectRevision: initial.ProjectRevision}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(scopedTestConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	read, err := restarted.ChangeSession(context.Background(), initial.ID)
	if err != nil || read.Workflow.Status != "interrupted" || read.Workflow.Stages[0].Status != "interrupted" || read.Workflow.Stages[1].Status != "skipped" || restarted.changeWorkflow.job != nil {
		t.Fatalf("restart: %+v, %v", read, err)
	}
	history, err := restarted.ChangeHistory(context.Background())
	if err != nil || history[0].WorkflowStatus != "interrupted" {
		t.Fatalf("history: %+v, %v", history, err)
	}
	read.Workflow.Stages = nil
	if err := storeChangeSession(root, read); err == nil {
		t.Fatal("corrupt workflow was accepted")
	}
}

func TestWorkflowReviewRejectsMalformedAndContradictoryOutput(t *testing.T) {
	for _, output := range []string{
		`{}`, `{"verdict":"approve","summary":"x","findings":null}`,
		`{"verdict":"approve","summary":"x","findings":["Blocking bug"]}`,
		`{"verdict":"changes_requested","summary":"x","findings":[]}`,
		`{"verdict":"approve","summary":"x","findings":[],"proposal_hash":"forged"}`,
		workflowApproval + `{}`, `{"verdict":"approve","summary":" ","findings":[]}`,
	} {
		if _, err := parseWorkflowReview(output, nil); err == nil {
			t.Fatalf("accepted review: %s", output)
		}
	}
}
