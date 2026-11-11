package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
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

func TestActivatingOrRestoringAProjectNotifiesListenersAndEvents(t *testing.T) {
	s, first := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	var activated []string
	s.OnProjectActivated(func(root string) {
		activated = append(activated, root)
	})
	events, unsubscribe := s.SubscribeEvents()
	defer unsubscribe()

	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateProject(second, &project.Analysis{Name: "second", Path: second}); err != nil {
		t.Fatal(err)
	}
	secondRoot := s.manager.Root()
	select {
	case event := <-events:
		if event.Kind != "project_index" {
			t.Fatalf("activate event = %+v", event)
		}
	default:
		t.Fatal("ActivateProject did not publish a project_index event")
	}

	if err := s.RestoreActiveProject(first, &project.Analysis{Name: "fixture", Path: first}); err != nil {
		t.Fatal(err)
	}
	firstRoot := s.manager.Root()
	select {
	case event := <-events:
		if event.Kind != "project_index" {
			t.Fatalf("restore event = %+v", event)
		}
	default:
		t.Fatal("RestoreActiveProject did not publish a project_index event")
	}
	if !slices.Equal(activated, []string{secondRoot, firstRoot}) {
		t.Fatalf("activated = %v, want [%s %s]", activated, secondRoot, firstRoot)
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
