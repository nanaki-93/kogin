package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestGitMergeCompletesLinkedFindingsAfterApplyCommitAndRestart(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	initializeChangeGit(t, service, root)
	request := trustedGitRequest(t, service)
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := store.ReconcileSource(project.FindingInput{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision}, project.FindingSourceAI, []project.UnifiedFinding{
		{Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Severity: "high", Title: "Target", Message: "Fix Run", Location: project.FindingLocation{Path: "main.go"}},
		{Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Severity: "low", Title: "Unrelated", Message: "Other concern", Location: project.FindingLocation{Path: "main.go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := []project.FindingTarget{
		{ProjectID: request.ProjectID, Kind: "semantic", ID: findings[0].ID, Path: "main.go"},
		{ProjectID: request.ProjectID, Kind: "performance", ID: "performance-risk", Path: "main.go"},
		{ProjectID: request.ProjectID, Kind: "security", Source: "ai", ID: "security-risk", Path: "main.go"},
	}
	child, opened := ownTask(t, service, ChangeCreateRequest{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision, Kind: "fix", Title: "Fix Run", Paths: []string{"main.go", "docs/change.md"}, FindingTargets: targets})
	path := opened.WorktreePath
	session, err := child.SendChangeMessage(context.Background(), opened.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(opened.ChangeSession), Message: "Fix Run"})
	if err != nil {
		t.Fatal(err)
	}
	session = approveChangeFixture(t, child, session)
	receipt, err := child.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true, CommitMessage: "Fix Run"})
	if err != nil || receipt.Commit == nil || receipt.Commit.State != "committed" {
		t.Fatalf("apply and commit: %+v, %v", receipt, err)
	}
	if completed, err := service.FindingCompletions(); err != nil || len(completed) != 0 {
		t.Fatalf("completed before merge: %+v, %v", completed, err)
	}
	// Merge after restart, with an independent target commit: no UI or worker
	// memory can supply the finding link, and the fix is behind the merge HEAD.
	testGit(t, root, "commit", "--allow-empty", "-m", "Independent target work")
	restarted, err := New(scopedTestConfig(server.URL), service.manager)
	if err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, restarted)
	request.Path, request.SourceHead = path, receipt.Commit.OID
	if _, err := restarted.PreviewGitMerge(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if completed, _ := restarted.FindingCompletions(); len(completed) != 0 {
		t.Fatal("preview completed findings")
	}
	result, err := restarted.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != path || result.RemovedBranch != opened.Branch || len(result.Completed) != len(targets) || len(result.Warnings) != 0 {
		t.Fatalf("merge: %+v, %v", result, err)
	}
	completed, err := restarted.FindingCompletions()
	if err != nil || len(completed) != len(targets) {
		t.Fatalf("completed findings: %+v, %v", completed, err)
	}
	for i, completion := range completed {
		if completion.FindingTarget != targets[i] || completion.CommitOID != receipt.Commit.OID {
			t.Fatalf("wrong finding or commit: %+v", completion)
		}
	}
	listed, err := restarted.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range listed {
		if (finding.Status == project.FindingStatusFixed) != (finding.ID == targets[0].ID) {
			t.Fatalf("merge/refresh changed the wrong finding status: %+v", finding)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}

func recordedFindingFix(t *testing.T, service *Service, request GitMutationRequest) (string, *ChangeSession) {
	t.Helper()
	path := createWorkflowWorktree(t, service, request)
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, child, "main.go")
	testGit(t, path, "commit", "--allow-empty", "-m", "Recorded fix")
	session.State = "applied"
	session.Commit = &ChangeCommit{State: "committed", OID: testGit(t, path, "rev-parse", "HEAD")}
	session.FindingTargets = []project.FindingTarget{{ProjectID: request.ProjectID, Kind: "semantic", ID: "target", Path: "main.go"}}
	if err := storeChangeSession(path, session); err != nil {
		t.Fatal(err)
	}
	return path, session
}

func TestGitMergeDoesNotCompleteUncommittedUndoneUnmergedOrForeignTargets(t *testing.T) {
	for _, state := range []string{"draft", "failed commit", "undone", "unmerged commit", "foreign project", "legacy"} {
		t.Run(state, func(t *testing.T) {
			service, root, request := gitWorkflowFixture(t)
			path, session := recordedFindingFix(t, service, request)
			switch state {
			case "draft", "undone":
				session.State = state
			case "failed commit":
				session.Commit.State = "failed"
			case "unmerged commit":
				testGit(t, root, "commit", "--allow-empty", "-m", "Different target commit")
				session.Commit.OID = testGit(t, root, "rev-parse", "HEAD")
			case "foreign project":
				session.FindingTargets[0].ProjectID = "another-project"
			case "legacy":
				session.FindingTargets = nil
			}
			if err := storeChangeSession(path, session); err != nil {
				t.Fatal(err)
			}
			request = trustedGitRequest(t, service)
			request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
			result, err := service.MergeGitWorktree(context.Background(), request)
			unfinished := state == "draft" || state == "failed commit" || state == "undone"
			if unfinished {
				if err == nil || testGit(t, root, "rev-parse", "HEAD") != request.Head {
					t.Fatalf("unfinished worktree was merged: %+v %v", result, err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("unfinished worktree removed: %v", err)
				}
			} else if err != nil || result.State != "merged" || result.RemovedWorktree != path {
				t.Fatalf("merge: %+v, %v", result, err)
			}
			if completed, err := service.FindingCompletions(); err != nil || len(completed) != 0 {
				t.Fatalf("completed %s target: %+v, %v", state, completed, err)
			}
		})
	}
}

func TestGitMergeFindingCompletionFailureRetainsHistoryForRetry(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	file := filepath.Join(root, ".kasuri", "findings.json")
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	request.Path, request.SourceHead = path, session.Commit.OID
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != "" || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "could not be marked Done") {
		t.Fatalf("completion failure: %+v, %v", result, err)
	}
	if _, err := loadChangeSession(path, session.ID); err != nil {
		t.Fatalf("lost finding link after storage failure: %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead = path, session.Commit.OID
	result, err = service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != path || len(result.Warnings) != 0 {
		t.Fatalf("retry completion: %+v, %v", result, err)
	}
	if completed, err := service.FindingCompletions(); err != nil || len(completed) != 1 {
		t.Fatalf("retry did not persist completion: %+v, %v", completed, err)
	}
}

func TestGitMergeCompletesFindingsEvenWhenCleanupIsBlocked(t *testing.T) {
	service, _, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	child, _, err := service.WorktreeService(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	createWorkflowWorktreeWithBranch(t, child, "codex/nested", "nested")
	request.Path, request.SourceHead = path, session.Commit.OID
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != "" || len(result.Warnings) != 1 {
		t.Fatalf("merge with cleanup warning: %+v, %v", result, err)
	}
	if completed, err := service.FindingCompletions(); err != nil || len(completed) != 1 {
		t.Fatalf("cleanup failure lost finding completion: %+v, %v", completed, err)
	}
}

func TestGitMergeRejectedOrFailedDoesNotCompleteFindings(t *testing.T) {
	for _, state := range []string{"stale", "unconfirmed", "corrupt history", "failed"} {
		t.Run(state, func(t *testing.T) {
			service, root, request := gitWorkflowFixture(t)
			path, session := recordedFindingFix(t, service, request)
			if state == "failed" {
				testGit(t, root, "commit", "--allow-empty", "-m", "Divergent target")
				testGit(t, root, "config", "merge.ff", "only")
				request = trustedGitRequest(t, service)
			}
			request.Path, request.SourceHead = path, session.Commit.OID
			switch state {
			case "stale":
				testGit(t, path, "commit", "--allow-empty", "-m", "Source changed after review")
			case "unconfirmed":
				request.Confirm = false
			case "corrupt history":
				if err := os.WriteFile(filepath.Join(path, ".kasuri", "changes", session.ID+".json"), []byte("{corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.MergeGitWorktree(context.Background(), request)
			if state == "failed" {
				if err != nil || result.State != "failed" || result.RemovedWorktree != "" {
					t.Fatalf("failed merge: %+v, %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s merge: %+v", state, result)
			}
			if completed, err := service.FindingCompletions(); err != nil || len(completed) != 0 {
				t.Fatalf("completed after %s: %+v, %v", state, completed, err)
			}
			if got := testGit(t, root, "rev-parse", "HEAD"); got != request.Head {
				t.Fatal("rejected merge changed the target branch")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("rejected merge deleted the source: %v", err)
			}
		})
	}
}

func TestGitMergeConflictsAbortAndDiscardDoNotCompleteFindings(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, _ := recordedFindingFix(t, service, request)
	for _, checkout := range []string{root, path} {
		if err := os.WriteFile(filepath.Join(checkout, "main.go"), []byte("package main\n// "+checkout+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		testGit(t, checkout, "add", "main.go")
		testGit(t, checkout, "commit", "-m", "Conflicting edit")
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "conflicts" {
		t.Fatalf("conflicts: %+v, %v", result, err)
	}
	if completed, err := service.FindingCompletions(); err != nil || len(completed) != 0 {
		t.Fatalf("conflict completed findings: %+v, %v", completed, err)
	}
	request = trustedGitRequest(t, service)
	request.MergeHead = result.Repository.MergeHead
	result, err = service.AbortGitMerge(context.Background(), request)
	if err != nil || result.State != "aborted" {
		t.Fatalf("abort: %+v, %v", result, err)
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead, request.SourceBranch = path, testGit(t, path, "rev-parse", "HEAD"), "codex/fix"
	result, err = service.DiscardGitWorktree(context.Background(), request)
	if err != nil || result.State != "discarded" {
		t.Fatalf("discard: %+v, %v", result, err)
	}
	if completed, err := service.FindingCompletions(); err != nil || len(completed) != 0 {
		t.Fatalf("abort/discard completed findings: %+v, %v", completed, err)
	}
}

func TestGitMergeDismissesTheIssuesATaskFoundDoNotHold(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	_, _, input, err := service.workspaceState()
	if err != nil {
		t.Fatal(err)
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := store.ReconcileSource(input, project.FindingSourceAI, []project.UnifiedFinding{
		{Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Severity: "high", Title: "Run ignores errors", Message: "Fix Run", Location: project.FindingLocation{Path: "main.go", StartLine: 5, EndLine: 5}},
		{Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Severity: "low", Title: "Run leaks", Message: "Run leaks a file", Location: project.FindingLocation{Path: "main.go", StartLine: 6, EndLine: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixed := project.FindingTarget{ProjectID: input.ProjectID, Kind: "semantic", ID: findings[0].ID, Path: "main.go"}
	notReal := project.FindingTarget{ProjectID: input.ProjectID, Kind: "semantic", ID: findings[1].ID, Path: "main.go"}
	session.FindingTargets, session.DismissedTargets = []project.FindingTarget{fixed}, []project.FindingTarget{notReal}
	session.Issues = []ChangeIssue{
		{FindingID: fixed.ID, Path: "main.go", Title: "Run ignores errors", Status: IssueFixed, Commit: session.Commit.OID},
		{FindingID: notReal.ID, Path: "main.go", Title: "Run leaks", Status: IssueDoesNotHold, Reason: "Run closes the file it opens."},
	}
	if err := storeChangeSession(path, session); err != nil {
		t.Fatal(err)
	}
	// Until the task merges, the issue that does not hold stays with it, in progress.
	if !slices.Contains(service.inProgressFindingTargets(context.Background(), root, input.ProjectID), notReal) {
		t.Fatal("the issue that does not hold is open again before the merge")
	}
	request = trustedGitRequest(t, service)
	request.Path, request.SourceHead = path, testGit(t, path, "rev-parse", "HEAD")
	if result, err := service.MergeGitWorktree(context.Background(), request); err != nil || result.State != "merged" || len(result.Warnings) != 0 || len(result.Completed) != 1 {
		t.Fatalf("merge: %+v, %v", result, err)
	}
	loaded, err := store.Load(input)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, finding := range loaded {
		statuses[finding.ID] = finding.Status
	}
	if statuses[fixed.ID] != project.FindingStatusFixed || statuses[notReal.ID] != project.FindingStatusDismissed {
		t.Fatalf("after the merge: %+v", statuses)
	}
}
