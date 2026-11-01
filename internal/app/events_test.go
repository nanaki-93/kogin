package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// The Web UI listens to the active project only, so a task's progress in a
// worktree reaches it there; the worktree's other events stay its own.
func TestWorkflowEventsOfAWorktreeReachTheActiveProject(t *testing.T) {
	service, _, request := gitWorkflowFixture(t)
	child, manager, err := service.WorktreeService(context.Background(), createWorkflowWorktree(t, service, request))
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := service.SubscribeEvents()
	defer unsubscribe()
	child.notifyEvent("project_index")
	child.notifyWorkflow(context.Background(), &ChangeSession{ID: "change-1", Title: "Fix", Workflow: &ChangeWorkflow{ID: "run-1", Status: "awaiting_human_review"}}, "")
	select {
	case event := <-events:
		if event.Kind != "workflow" || event.SessionID != "change-1" || event.Status != "awaiting_human_review" || event.WorktreePath != manager.Root() || event.WorktreePath == "" {
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

// A client tells when an analysis run ends from its events alone: each names
// the run and its status.
func TestAnalysisEventsNameTheRunAndItsStatus(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	events, unsubscribe := s.SubscribeEvents()
	defer unsubscribe()
	var mu sync.Mutex
	var seen []Event
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case event := <-events:
				if event.Kind == "analysis" {
					mu.Lock()
					seen = append(seen, event)
					mu.Unlock()
				}
			case <-stop:
				return
			}
		}
	}()
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := slices.Clone(seen)
		mu.Unlock()
		if len(got) > 0 && got[len(got)-1].Status == string(run.Status) {
			for _, event := range got {
				if event.WorkflowID != run.Identity.ID || event.Status == "" {
					t.Fatalf("analysis event %+v does not name run %s", event, run.Identity.ID)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no event with the final status %s: %+v", run.Status, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
