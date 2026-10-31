package app

import (
	"context"
	"testing"
)

// The Web UI listens to the active project only, so a task's progress in a
// worktree reaches it there; the worktree's other events stay its own.
func TestWorkflowEventsOfAWorktreeReachTheActiveProject(t *testing.T) {
	service, _, request := gitWorkflowFixture(t)
	child, _, err := service.WorktreeService(context.Background(), createWorkflowWorktree(t, service, request))
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := service.SubscribeEvents()
	defer unsubscribe()
	child.notifyEvent("project_index")
	child.notifyWorkflow(context.Background(), &ChangeSession{ID: "change-1", Title: "Fix", Workflow: &ChangeWorkflow{ID: "run-1", Status: "awaiting_human_review"}}, "")
	select {
	case event := <-events:
		if event.Kind != "workflow" || event.SessionID != "change-1" || event.Status != "awaiting_human_review" {
			t.Fatalf("event = %+v", event)
		}
	default:
		t.Fatal("the worktree's workflow event did not reach the active project")
	}
	select {
	case event := <-events:
		t.Fatalf("another worktree event reached the active project: %+v", event)
	default:
	}
}
