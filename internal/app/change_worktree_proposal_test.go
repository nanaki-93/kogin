package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	worktreeMain   = "package main\n\nimport \"fmt\"\n\nfunc Run() error {\n\t_, err := fmt.Println(\"run\")\n\treturn err\n}\n"
	worktreeHelper = "package main\n\n// Twice doubles n.\nfunc Twice(n int) int {\n\treturn n * 2\n}\n\n// Half halves n.\nfunc Half(n int) int {\n\treturn n / 2\n}\n"
)

// worktreeTask opens a task in its own worktree in a repository with
// util.go (renamed by the proposal) and old.go (deleted by it).
func worktreeTask(t *testing.T, reply string) (*Service, *Service, string, *ChangeSession) {
	t.Helper()
	service, root := newSemanticAnalysisService(t, changeProvider(t, func() string { return reply }).URL, 0)
	for name, content := range map[string]string{
		"go.mod":  "module example.test/worktree\n\ngo 1.22\n",
		"util.go": worktreeHelper,
		"old.go":  "package main\n\n// Legacy is no longer used.\nfunc Legacy() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
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
	if opened.Worktree == nil || opened.Worktree.Base != testGit(t, opened.WorktreePath, "rev-parse", "HEAD") || opened.Worktree.Branch != opened.Branch {
		t.Fatalf("worktree base = %+v", opened.Worktree)
	}
	proposed, err := child.SendChangeMessage(context.Background(), opened.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(opened.ChangeSession), Message: "Return the print error and tidy up."})
	if err != nil {
		t.Fatal(err)
	}
	return service, child, opened.WorktreePath, proposed
}

func reshapingReply() string {
	return `{"explanation":"Return the print error, add a version, drop Legacy and move the helpers.","changes":[` +
		`{"path":"main.go","content":` + strconv.Quote(worktreeMain) + `,"summary":"Run returns the print error."},` +
		`{"path":"version.go","content":"package main\n\nconst Version = \"1.0\"\n"},` +
		`{"path":"old.go","content":"","delete":true},` +
		`{"path":"helpers.go","renamed_from":"util.go","content":` + strconv.Quote(worktreeHelper) + `}]}`
}

func editByPath(edits []ChangeEdit, path string) *ChangeEdit {
	for i := range edits {
		if edits[i].Path == path {
			return &edits[i]
		}
	}
	return nil
}

func TestATaskInItsOwnWorktreeAddsRenamesAndDeletesFilesAndIsAcceptedAsACommit(t *testing.T) {
	service, child, worktree, session := worktreeTask(t, reshapingReply())
	for path, status := range map[string]string{"main.go": EditModified, "version.go": EditAdded, "old.go": EditDeleted, "helpers.go": EditRenamed} {
		if edit := editByPath(session.Changes, path); edit == nil || edit.Status != status {
			t.Fatalf("%s: %+v in %+v", path, edit, session.Changes)
		}
	}
	if renamed := editByPath(session.Changes, "helpers.go"); renamed.OldPath != "util.go" || renamed.Diff.OldPath != "util.go" || len(session.Changes) != 4 {
		t.Fatalf("rename = %+v", renamed)
	}
	if main := editByPath(session.Changes, "main.go"); main.Summary != "Run returns the print error." {
		t.Fatalf("the agent's summary was lost: %+v", main)
	}
	for path, want := range map[string]bool{"old.go": false, "util.go": false, "helpers.go": true, "version.go": true} {
		if _, err := os.Stat(filepath.Join(worktree, path)); (err == nil) != want {
			t.Fatalf("%s in the worktree: %v, want present=%v", path, err, want)
		}
	}
	if got := testGit(t, service.manager.Root(), "status", "--porcelain"); got != "" {
		t.Fatalf("the main checkout changed: %s", got)
	}

	checked, err := child.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session), CheckOptions: CheckOptions{RunTests: true, RunLint: true}})
	if err != nil || !requiredChecksPassed(checked.Checks) {
		t.Fatalf("checks = %+v, %v", checked, err)
	}
	reviewed, err := child.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := child.ApplyChange(context.Background(), reviewed.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(reviewed), Confirm: true, CommitMessage: "Return the print error"})
	if err != nil || result.State != "applied" || result.Commit == nil || result.Commit.State != "committed" {
		t.Fatalf("accept = %+v, %v", result, err)
	}
	if head := testGit(t, worktree, "rev-parse", "HEAD"); head != result.Commit.OID || testGit(t, worktree, "log", "-1", "--format=%s") != "Return the print error" {
		t.Fatalf("the task branch is at %s, want the accepted commit %s", head, result.Commit.OID)
	}
	if changed := testGit(t, worktree, "show", "--name-status", "-M", "--format=", "HEAD"); !strings.Contains(changed, "D\told.go") || !strings.Contains(changed, "A\tversion.go") || !strings.Contains(changed, "M\tmain.go") || !strings.Contains(changed, "util.go\thelpers.go") {
		t.Fatalf("committed changes:\n%s", changed)
	}
	if status := testGit(t, worktree, "status", "--porcelain"); status != "" {
		t.Fatalf("the worktree is not clean after accepting: %s", status)
	}
	accepted, err := child.ChangeSession(context.Background(), session.ID)
	if err != nil || accepted.State != "applied" || accepted.Commit == nil || accepted.Commit.State != "committed" || accepted.Freshness == "stale" {
		t.Fatalf("accepted task = %+v, %v", accepted, err)
	}
}

func TestEditsMadeInTheWorktreeBecomePartOfTheProposal(t *testing.T) {
	_, child, worktree, session := worktreeTask(t, reshapingReply())
	if err := os.WriteFile(filepath.Join(worktree, "NOTES.md"), []byte("# Notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := child.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)}); err == nil {
		t.Fatal("an action ran on a proposal the worktree no longer holds")
	}
	adopted, err := child.ChangeSession(context.Background(), session.ID)
	if err != nil || adopted.Revision != session.Revision+1 || adopted.Freshness != "current" || editByPath(adopted.Changes, "NOTES.md") == nil {
		t.Fatalf("adopted = %+v, %v", adopted, err)
	}
	if last := adopted.Messages[len(adopted.Messages)-1]; !strings.Contains(last.Content, "picked up edits") {
		t.Fatalf("no note about the adopted edits: %+v", last)
	}
	if _, err := child.ReanalyzeChange(context.Background(), adopted.ID, ChangeReanalysisRequest{ChangeIdentity: changeIdentity(adopted)}); err == nil || !strings.Contains(err.Error(), "not re-analyzed") {
		t.Fatalf("re-analysis = %v", err)
	}

	testGit(t, worktree, "commit", "--allow-empty", "-m", "Someone else's commit")
	moved, err := child.ChangeSession(context.Background(), session.ID)
	if err != nil || moved.Freshness != "stale" || !strings.Contains(moved.StaleReason, "moved off its base commit") {
		t.Fatalf("after a commit in the worktree: %+v, %v", moved, err)
	}
}

func TestTheDiffPolicyKeepsRulesAndMetadataOutOfAProposal(t *testing.T) {
	for name, reply := range map[string]string{
		"rules":    `{"explanation":"Loosen the budget.","changes":[{"path":"AGENTS.md","content":"Max file lines: 9999\n"}]}`,
		"metadata": `{"explanation":"Edit history.","changes":[{"path":".kasuri/changes/x.json","content":"{}"}]}`,
		"escape":   `{"explanation":"Escape.","changes":[{"path":"../outside.go","content":"package main\n"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			service, root := newSemanticAnalysisService(t, changeProvider(t, func() string { return reply }).URL, 0)
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
			if _, err := child.SendChangeMessage(context.Background(), opened.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(opened.ChangeSession), Message: "Go."}); err == nil {
				t.Fatal("the proposal was accepted")
			}
		})
	}
}

func TestATaskInTheMainCheckoutCannotDeleteFiles(t *testing.T) {
	reply := `{"explanation":"Drop main.","changes":[{"path":"main.go","content":"","delete":true}]}`
	service, _ := newSemanticAnalysisService(t, changeProvider(t, func() string { return reply }).URL, 0)
	session := openChangeFixture(t, service, "main.go")
	_, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Remove it."})
	if err == nil || !strings.Contains(err.Error(), "needs a task in its own worktree") {
		t.Fatalf("delete in the main checkout = %v", err)
	}
}

func TestAgentRunsLockTheTaskWorktreeAndStaleLocksAreReleased(t *testing.T) {
	service, child, worktree, session := worktreeTask(t, reshapingReply())
	unlock := child.lockTaskWorktree(&changeWorkflowJob{root: worktree, sessionID: session.ID})
	if reason, locked := worktreeLock(context.Background(), worktree); !locked || reason != runLockReason {
		t.Fatalf("lock during a run = %q, %v", reason, locked)
	}
	unlock()
	if _, locked := worktreeLock(context.Background(), worktree); locked {
		t.Fatal("the worktree stayed locked after the run")
	}

	// A lock left by a daemon that stopped mid-run is released when the
	// branches are listed; a lock someone else set is not.
	testGit(t, worktree, "worktree", "lock", "--reason", runLockReason, worktree)
	index, _ := service.manager.Index()
	workspace, err := service.GitWorkspace(context.Background(), index.ProjectID, index.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range workspace.Worktrees {
		if tree.Path == worktree && tree.Locked {
			t.Fatalf("a stale run lock was kept: %+v", tree)
		}
	}
	testGit(t, worktree, "worktree", "lock", "--reason", "release audit", worktree)
	if unlock := child.lockTaskWorktree(&changeWorkflowJob{root: worktree, sessionID: session.ID}); unlock != nil {
		unlock()
	}
	if reason, locked := worktreeLock(context.Background(), worktree); !locked || reason != "release audit" {
		t.Fatalf("someone else's lock = %q, %v", reason, locked)
	}
}
