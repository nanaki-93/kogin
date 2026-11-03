package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestWorktreeImplementationsRunConcurrentlyAndApplyInIsolation(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	service, root, _ := workflowService(t, [3]func() string{
		func() string { entered <- struct{}{}; <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	t.Cleanup(func() { _ = service.ShutdownChangeWorkflows(context.Background()) })
	t.Cleanup(func() { close(release) })
	initializeChangeGit(t, service, root)
	root = service.manager.Root()
	request := trustedGitRequest(t, service)
	children := make([]*Service, 3)
	sessions := make([]*ChangeSession, 3)
	for i := range children {
		// Each names its own files: tasks that name the same code take turns.
		child, opened := ownTask(t, service, ChangeCreateRequest{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, Kind: "feature", Title: fmt.Sprintf("Implement a bounded task %d", i), Paths: []string{fmt.Sprintf("new%d.go", i), fmt.Sprintf("new%d_test.go", i)}, AcceptanceCriteria: []string{"Preserve existing behavior."}})
		if child == service || child.manager == service.manager {
			t.Fatal("worktree reused the active project state")
		}
		again, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
		if err != nil || again != child {
			t.Fatal("reopening lost the worker owner")
		}
		session := opened.ChangeSession
		if trust, err := child.ExecutionTrust(context.Background(), session.ProjectRevision, ""); err != nil || !trust.Trusted {
			t.Fatalf("a task's worktree did not share the checkout's trust: %+v %v", trust, err)
		}
		sessions[i], err = child.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session))
		if err != nil {
			t.Fatal(err)
		}
		children[i] = child
	}
	for range children {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("implementations did not dispatch concurrently")
		}
	}
	for i, child := range children {
		store, err := agentstate.Open(child.manager.Root())
		if err != nil {
			t.Fatal(err)
		}
		records, err := store.Sessions(context.Background(), agentstate.StateApp, "local")
		if err != nil || len(records) != 3 {
			t.Fatalf("worktree journal is not isolated: %d records, %v", len(records), err)
		}
		parentStore, err := agentstate.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		graphs, completions, repairs := 0, 0, 0
		for _, record := range records {
			if record.State["mini_orca.repair_graph"] != nil {
				repairs++
			}
			if record.State["mini_orca.change_graph"] != nil {
				graphs++
			}
			if record.State["mini_orca.request"] != nil {
				completions++
			}
			if _, err := parentStore.Session(context.Background(), record.Key); !errors.Is(err, agentstate.ErrNotFound) {
				t.Fatalf("child session leaked into parent: %v", err)
			}
		}
		if graphs != 1 || completions != 1 || repairs != 1 {
			t.Fatalf("worktree lost graph/model journal separation: %d graphs, %d completions, %d repair controllers", graphs, completions, repairs)
		}
		polled, err := child.ChangeSession(context.Background(), sessions[i].ID)
		if err != nil || polled.Freshness != "current" || polled.Workflow == nil || polled.Workflow.Status != "running" {
			t.Fatalf("poll active session %d: %+v, %v", i, polled, err)
		}
	}
	// Reindexing the original project must neither capture child sources nor
	// cancel jobs in separate worktrees.
	index, err := service.Reindex()
	if err != nil || index.ProjectRevision != request.ProjectRevision {
		t.Fatalf("worktrees changed the original project revision: %+v, %v", index, err)
	}
	for range children {
		release <- struct{}{}
	}
	for i, child := range children {
		sessions[i] = finishedWorkflow(t, child, sessions[i].ID)
		if sessions[i].Workflow.Status != "awaiting_human_review" {
			t.Fatalf("implementation %d: %+v", i, sessions[i].Workflow)
		}
	}
	child, session := children[0], sessions[0]
	if _, err := child.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(session)}); err != nil {
		t.Fatal(err)
	}
	receipt, err := child.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{
		ChangeIdentity: changeIdentity(session), Confirm: true, CommitMessage: "Implement Helper",
	})
	if err != nil || receipt.Commit == nil || receipt.Commit.State != "committed" {
		t.Fatalf("apply: %+v, %v", receipt, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("accepting a task changed the main checkout")
	}
	for i := 1; i < len(children); i++ {
		if head := testGit(t, children[i].manager.Root(), "rev-parse", "HEAD"); head != sessions[i].Worktree.Base {
			t.Fatalf("accepting a task committed in another task's worktree: %s", children[i].manager.Root())
		}
	}
	if service.manager.Root() != root || testGit(t, root, "branch", "--show-current") != "main" || testGit(t, root, "rev-parse", "HEAD") != request.Head {
		t.Fatal("implementation switched the active project or branch")
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree metadata dirtied the original checkout: %s", got)
	}
}

func TestWorktreeServiceRejectsUnrelatedOrRemovedCheckout(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path := createWorkflowWorktree(t, service, request)
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := New(service.conf().config, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inactive.WorktreeService(context.Background(), path); !errors.Is(err, project.ErrNoActiveProject) {
		t.Fatalf("worktree scope bypassed active-project guard: %v", err)
	}
	for _, rejected := range []string{root, t.TempDir(), filepath.Join(root, "missing"), ".worktrees/fix"} {
		if _, _, err := service.WorktreeService(context.Background(), rejected); err == nil {
			t.Fatalf("accepted unrelated path %s", rejected)
		}
	}
	if _, _, err := service.WorktreeService(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "worktree", "remove", "--force", path)
	if _, _, err := service.WorktreeService(context.Background(), path); err == nil {
		t.Fatal("cached worker bypassed worktree membership")
	}
}

func TestWorktreeWorkersStopOnShutdownAndProjectReplacement(t *testing.T) {
	for _, action := range []string{"shutdown", "replace"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			service, root, _ := workflowService(t, [3]func() string{
				func() string { close(entered); <-release; return workflowImplementation },
				func() string { return workflowTests },
				func() string { return workflowApproval },
			})
			t.Cleanup(func() { close(release) })
			initializeChangeGit(t, service, root)
			path := createWorkflowWorktree(t, service, trustedGitRequest(t, service))
			child, _, err := service.WorktreeService(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			session := openChangeFixture(t, child, "new.go", "new_test.go")
			if _, err := child.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
				t.Fatal(err)
			}
			if _, err := child.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not start")
			}
			if action == "shutdown" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				err = service.ShutdownChangeWorkflows(ctx)
			} else {
				analysis, readErr := service.manager.Analysis()
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = service.RestoreActiveProject(root, analysis)
			}
			if err != nil {
				t.Fatal(err)
			}
			finished := finishedWorkflow(t, child, session.ID)
			if finished.Workflow.Status != "canceled" {
				t.Fatalf("worker survived %s: %+v", action, finished.Workflow)
			}
			if _, err := os.Stat(filepath.Join(path, "new.go")); !os.IsNotExist(err) {
				t.Fatal("canceled worker wrote source")
			}
		})
	}
}
