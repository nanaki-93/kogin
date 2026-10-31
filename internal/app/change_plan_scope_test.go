package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// plannedWorktreeTask opens a task with a plan in its own worktree, reading
// helper.go for context, and approves the plan as starting a run does.
func plannedWorktreeTask(t *testing.T) (*Service, string, *ChangeSession) {
	t.Helper()
	service, root := newSemanticAnalysisServiceWithHelper(t, "http://127.0.0.1:1", 0)
	initializeChangeGit(t, service, root)
	trustedGitRequest(t, service)
	request := worktreeTaskRequest(t, service, "main.go")
	request.Plan = &ChangePlanInput{Problem: "Run ignores print errors.", Approach: "Return the error fmt.Println reports.", ReadPaths: []string{"helper.go"}}
	opened, err := service.OpenChangeInWorktree(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	root = child.manager.Root()
	session, err := loadChangeSession(root, opened.ID)
	if err != nil || session.Plan == nil {
		t.Fatalf("planned task = %+v, %v", session, err)
	}
	if err := approveWorkflowPlan(session, session.Plan.Hash); err != nil {
		t.Fatal(err)
	}
	if err := storeChangeSession(root, session); err != nil {
		t.Fatal(err)
	}
	return child, root, session
}

func writeWorktreeFiles(t *testing.T, worktree string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// The agents of a planned task changed a file the plan only read, added a
// file and wrote a test the task did not name: the task takes them all and
// keeps its plan, still approved, instead of refusing every later save.
func TestWorktreeEditsOutsideAPlannedTaskKeepItsPlanApproved(t *testing.T) {
	child, worktree, session := plannedWorktreeTask(t)
	writeWorktreeFiles(t, worktree, map[string]string{
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc Run() error {\n\t_, err := fmt.Println(\"run\")\n\treturn err\n}\n",
		"helper.go":    "package main\n\nfunc Helper() {}\n",
		"version.go":   "package main\n\nconst Version = \"1.0\"\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {\n\tif err := Run(); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n",
	})
	adopted, err := child.ChangeSession(context.Background(), session.ID)
	if err != nil || adopted.Freshness != "current" || adopted.Revision != session.Revision+1 {
		t.Fatalf("adopted = %+v, %v", adopted, err)
	}
	for _, path := range []string{"main.go", "helper.go", "version.go", "main_test.go"} {
		if editByPath(adopted.Changes, path) == nil || changeTarget(adopted, path) == nil {
			t.Fatalf("%s is not part of the task: %+v", path, adopted.Changes)
		}
	}
	plan := adopted.Plan
	if plan == nil || plan.Hash != changePlanHash(adopted) || plan.ApprovedHash != plan.Hash || len(plan.ReadPaths) != 0 || len(plan.ReadTargets) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if err := requireApprovedChangePlan(adopted); err != nil {
		t.Fatalf("the grown task's plan: %v", err)
	}
}

// A plan that no longer matches its task is not re-bound: growing the scope
// never makes a mismatched plan valid.
func TestAMismatchedPlanIsNotReboundWhenTheScopeGrows(t *testing.T) {
	_, worktree, session := plannedWorktreeTask(t)
	session.Plan.Hash = "tampered"
	writeWorktreeFiles(t, worktree, map[string]string{"version.go": "package main\n\nconst Version = \"1.0\"\n"})
	read, err := readWorktreeProposal(context.Background(), worktree, session, session.Changes)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureWorktreeTargets(context.Background(), worktree, session, read); err != nil {
		t.Fatal(err)
	}
	if session.Plan.Hash != "tampered" || validateStoredPlan(session) == nil {
		t.Fatalf("a mismatched plan was re-bound: %+v", session.Plan)
	}
}

// When the worktree's edits cannot become the proposal, or cannot even be
// read, the task still opens: it is out of date and says why.
func TestATaskWhoseWorktreeEditsCannotBeAdoptedStaysReadable(t *testing.T) {
	for files, reason := range map[int]string{
		maxStoredTaskPaths:     "could not become its proposal (a task changes at most 64 files)",
		maxStoredTaskPaths + 1: "the task's worktree changes 65 files",
	} {
		child, worktree, session := plannedWorktreeTask(t)
		extra := map[string]string{}
		for i := range files {
			extra[fmt.Sprintf("extra%02d.go", i)] = "package main\n"
		}
		writeWorktreeFiles(t, worktree, extra)
		stored, err := child.ChangeSession(context.Background(), session.ID)
		if err != nil {
			t.Fatalf("%d files: the task could not be opened: %v", files, err)
		}
		if stored.Revision != session.Revision || stored.Freshness != "stale" || !strings.Contains(stored.StaleReason, reason) {
			t.Fatalf("%d files: task = revision %d, %s: %q", files, stored.Revision, stored.Freshness, stored.StaleReason)
		}
		if slices.ContainsFunc(stored.Targets, func(target ChangeTarget) bool { return strings.HasPrefix(target.Path, "extra") }) {
			t.Fatalf("%d files: a failed adoption changed the task: %+v", files, stored.Targets)
		}
	}
}
