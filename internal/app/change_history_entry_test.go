package app

import (
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
)

func phaseWorkflow(status string, running int) *ChangeWorkflow {
	w := &ChangeWorkflow{Status: status, Stages: []ChangeWorkflowStage{{Name: "create", Status: "pending"}, {Name: "test", Status: "pending"}, {Name: "review", Status: "pending"}, {Name: "human_review", Status: "pending"}}}
	for i := 0; i < running; i++ {
		w.Stages[i].Status = "completed"
	}
	if running >= 0 && running < len(w.Stages) {
		w.Stages[running].Status = "running"
	}
	return w
}

func TestChangeHistoryEntryNamesTheRunningPhase(t *testing.T) {
	issues := []ChangeIssue{{FindingID: "a", Status: IssueFixed}, {FindingID: "b", Status: IssueFixing}, {FindingID: "c", Status: IssuePending}}
	worktree := &ChangeWorktree{Branch: "kasuri/fix"}
	for _, tc := range []struct {
		name    string
		session ChangeSession
		want    *gitops.ChangeWorkflowPhase
	}{
		{"issue step", ChangeSession{Worktree: worktree, Issues: issues, Workflow: phaseWorkflow("running", 0)}, &gitops.ChangeWorkflowPhase{Stage: "create", Issue: 2, Issues: 3}},
		{"one issue has no steps", ChangeSession{Worktree: worktree, Issues: issues[1:2], Workflow: phaseWorkflow("running", 0)}, &gitops.ChangeWorkflowPhase{Stage: "create"}},
		{"testing after the steps", ChangeSession{Worktree: worktree, Issues: []ChangeIssue{{Status: IssueFixed}, {Status: IssueFixed}}, Workflow: phaseWorkflow("running", 1)}, &gitops.ChangeWorkflowPhase{Stage: "test"}},
		{"repair", ChangeSession{Workflow: func() *ChangeWorkflow { w := phaseWorkflow("running", 0); w.AutoRepairAttempts = 1; return w }()}, &gitops.ChangeWorkflowPhase{Stage: "create", Repair: 1}},
		{"checks start", ChangeSession{Workflow: func() *ChangeWorkflow { w := phaseWorkflow("running", 1); w.ResumeFrom = "checks"; return w }()}, &gitops.ChangeWorkflowPhase{Stage: "test", ChecksOnly: true}},
		{"stopping in review", ChangeSession{Workflow: phaseWorkflow("canceling", 2)}, &gitops.ChangeWorkflowPhase{Stage: "review"}},
		{"no stage started yet", ChangeSession{Workflow: phaseWorkflow("running", -1)}, nil},
		{"waiting for another task", ChangeSession{Workflow: phaseWorkflow("waiting", 0)}, nil},
		{"no run", ChangeSession{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := changeHistoryEntry(&tc.session).WorkflowPhase
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("phase = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestChangeHistoryEntryTellsReviewFromAccept(t *testing.T) {
	passed := []CheckResult{{Name: "test", Required: true, State: CheckPassed}}
	failed := []CheckResult{{Name: "test", Required: true, State: CheckFailed}}
	changes := []ChangeEdit{{Path: "a.go"}, {Path: "b.go"}}
	ready := phaseWorkflow("awaiting_human_review", 3)
	ready.Stages[3].Status = "waiting"
	for _, tc := range []struct {
		name    string
		session ChangeSession
		want    *gitops.ChangeReviewProgress
	}{
		{"nothing viewed", ChangeSession{State: "draft", Hash: "h", Changes: changes, Checks: passed, Workflow: ready}, &gitops.ChangeReviewProgress{Step: gitops.ReviewViewFiles, Files: 2}},
		{"one viewed", ChangeSession{State: "draft", Hash: "h", Changes: changes, Checks: passed, Workflow: ready, FileReviewHash: "h", ReviewedFiles: []string{"b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewViewFiles, Files: 2, Viewed: 1}},
		{"views of an earlier version", ChangeSession{State: "draft", Hash: "h", Changes: changes, Checks: passed, Workflow: ready, FileReviewHash: "old", ReviewedFiles: []string{"a.go", "b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewViewFiles, Files: 2}},
		{"checks failed", ChangeSession{State: "draft", Hash: "h", Changes: changes, Checks: failed, Workflow: ready, FileReviewHash: "h", ReviewedFiles: []string{"a.go", "b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewRunChecks, Files: 2, Viewed: 2}},
		{"no checks", ChangeSession{State: "draft", Hash: "h", Changes: changes, FileReviewHash: "h", ReviewedFiles: []string{"a.go", "b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewRunChecks, Files: 2, Viewed: 2}},
		{"ready to accept", ChangeSession{State: "draft", Hash: "h", Changes: changes, Checks: passed, Workflow: ready, FileReviewHash: "h", ReviewedFiles: []string{"a.go", "b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewAccept, Files: 2, Viewed: 2}},
		{"approved, not committed", ChangeSession{State: "draft", Hash: "h", ReviewedHash: "h", Changes: changes, Checks: passed, Workflow: ready, FileReviewHash: "h", ReviewedFiles: []string{"a.go", "b.go"}}, &gitops.ChangeReviewProgress{Step: gitops.ReviewAccept, Files: 2, Viewed: 2}},
		{"agents still working", ChangeSession{State: "draft", Hash: "h", Changes: changes, Workflow: phaseWorkflow("running", 1)}, nil},
		{"reviewers requested changes", ChangeSession{State: "draft", Hash: "h", Changes: changes, Workflow: phaseWorkflow("changes_requested", 3)}, nil},
		{"no changes yet", ChangeSession{State: "draft"}, nil},
		{"accepted", ChangeSession{State: "applied", Hash: "h", Changes: changes}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := changeHistoryEntry(&tc.session).Review
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("review = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestChangeHistoryListsReviewProgress(t *testing.T) {
	s, root, proposal := artifactChangeFixture(t)
	history, err := s.ChangeHistory(t.Context())
	if err != nil || len(history) != 1 || history[0].Review == nil || history[0].Review.Step != gitops.ReviewViewFiles || history[0].Review.Files != 1 {
		t.Fatalf("history before review: %+v, %v", history, err)
	}
	proposal.FileReviewHash, proposal.ReviewedFiles = proposal.Hash, []string{"main.go"}
	proposal.Checks = []CheckResult{{Name: "test", Required: true, State: CheckPassed}}
	if err := storeChangeSession(root, proposal); err != nil {
		t.Fatal(err)
	}
	listed, err := listChangeSessions(root)
	if err != nil || len(listed) != 1 || listed[0].Review == nil || *listed[0].Review != (gitops.ChangeReviewProgress{Step: gitops.ReviewAccept, Files: 1, Viewed: 1}) {
		t.Fatalf("history after review: %+v, %v", listed, err)
	}
}
