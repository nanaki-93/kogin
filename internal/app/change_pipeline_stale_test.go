package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestChangeWorkflowCancellationAndStalePublication(t *testing.T) {
	for _, action := range []string{"cancel", "source", "scope"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			s, root, initial := workflowService(t, [3]func() string{
				func() string { close(entered); <-release; return workflowImplementation },
				func() string { t.Error("test dispatched after revocation"); return workflowTests },
				func() string { t.Error("review dispatched after revocation"); return workflowApproval },
			})
			started, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial))
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			progress, err := s.ChangeSession(context.Background(), initial.ID)
			if err != nil || progress.Workflow.Stages[0].Status != "running" {
				t.Fatalf("progress: %+v, %v", progress, err)
			}
			if _, err := s.SendChangeMessage(context.Background(), initial.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(progress), Message: "Racing change"}); !errors.Is(err, project.ErrRevisionConflict) {
				t.Fatalf("manual edit bypassed workflow ownership: %v", err)
			}
			switch action {
			case "cancel":
				control := ChangeWorkflowControl{ProjectID: initial.ProjectID, ProjectRevision: initial.ProjectRevision, WorkflowID: "wrong"}
				if _, err := s.CancelChangeWorkflow(context.Background(), initial.ID, control); !errors.Is(err, project.ErrRevisionConflict) {
					t.Fatal(err)
				}
				control.WorkflowID = started.Workflow.ID
				if _, err := s.CancelChangeWorkflow(context.Background(), initial.ID, control); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("New guidance"), 0644); err != nil {
					t.Fatal(err)
				}
			case "scope":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			result := finishedWorkflow(t, s, initial.ID)
			if result.Workflow.Status != "canceled" && result.Workflow.Status != "stale" {
				t.Fatalf("terminal: %+v", result.Workflow)
			}
			if result.Revision != 1 || len(result.Changes) != 0 || result.ReviewedHash != "" {
				t.Fatalf("late publication: %+v", result)
			}
		})
	}
}

func TestReindexStopsOnlyWorkflowsItMadeStale(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte("# Unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := finishedWorkflow(t, s, initial.ID)
	if result.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("an unrelated change stopped the workflow: %s %s", result.Workflow.Status, result.Workflow.Reason)
	}
	// The waiting input admitted before the reindex can still be answered.
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ReviewChange(context.Background(), initial.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(result)})
	if err != nil || approved.Workflow.Input.Status != "approved" {
		t.Fatalf("approval after reindexing: %+v %v", approved, err)
	}
}

func TestStoppedWorkflowNamesTheChangedFile(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/workflow\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := finishedWorkflow(t, s, initial.ID)
	if result.Workflow.Status == "awaiting_human_review" || !strings.Contains(result.Workflow.Reason, "go.mod changed") {
		t.Fatalf("stop reason: %s %q", result.Workflow.Status, result.Workflow.Reason)
	}
}
