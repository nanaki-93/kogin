package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestRejectCommittedWorktreeReopensLinkedIssueAndRetainsBranch(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := store.ReconcileSource(project.FindingInput{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision}, project.FindingSourceAI, []project.UnifiedFinding{{Title: "Pending fix", Message: "Fix the captured issue", Severity: "high", Confidence: project.FindingConfidenceSuggested, Source: project.FindingSourceAI, Location: project.FindingLocation{Path: "main.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	session.FindingTargets[0].ID = findings[0].ID
	if err := storeChangeSession(path, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(findings[0].ID, project.FindingStatusInProgress); err != nil {
		t.Fatal(err)
	}
	request.Path, request.SourceHead, request.SourceBranch = path, session.Commit.OID, "codex/fix"
	result, err := service.DiscardGitWorktree(context.Background(), request)
	if err != nil || result.State != "discarded" || result.RemovedWorktree != path || result.RemovedBranch != "" || len(result.Warnings) != 0 {
		t.Fatalf("reject committed fix: %+v %v", result, err)
	}
	if got := testGit(t, root, "rev-parse", "codex/fix"); got != session.Commit.OID {
		t.Fatal("reject lost unmerged commits")
	}
	stored, err := store.Load(project.FindingInput{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision})
	if err != nil || len(stored) != 1 || stored[0].Status != project.FindingStatusOpen {
		t.Fatalf("rejected issue not reopened: %+v %v", stored, err)
	}
}

func TestMergedBranchCleanupFailureKeepsCompletionAndReportsPartialRemoval(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	path, session := recordedFindingFix(t, service, request)
	lock := filepath.Join(root, ".git", "refs", "heads", "codex", "fix.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	request.Path, request.SourceHead = path, session.Commit.OID
	result, err := service.MergeGitWorktree(context.Background(), request)
	if err != nil || result.State != "merged" || result.RemovedWorktree != path || result.RemovedBranch != "" || len(result.Completed) != 1 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "branch codex/fix was retained") {
		t.Fatalf("partial cleanup receipt: %+v %v", result, err)
	}
	if got := testGit(t, root, "rev-parse", "codex/fix"); got != session.Commit.OID {
		t.Fatal("failed deletion changed retained branch")
	}
	completed, err := service.FindingCompletions()
	if err != nil || len(completed) != 1 {
		t.Fatalf("cleanup failure lost Done state: %+v %v", completed, err)
	}
}
