package app

import (
	"context"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func listedStatuses(t *testing.T, service *Service) map[string]string {
	t.Helper()
	findings, err := service.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, finding := range findings {
		statuses[finding.ID] = finding.Status
	}
	return statuses
}

// An issue is in progress while a task fixes it, not open: it returns to the
// open issues when the task is deleted. A stored in-progress flag without a
// task does not keep it out of them.
func TestAnIssueIsInProgressWhileATaskFixesIt(t *testing.T) {
	service, root, request := gitWorkflowFixture(t)
	store, err := project.NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	reported := []project.UnifiedFinding{
		{Title: "Run ignores errors", Message: "Run drops the print error.", Severity: "high", Rule: "errcheck", Source: project.FindingSourceLint, Confidence: project.FindingConfidenceToolReported, Location: project.FindingLocation{Path: "main.go", StartLine: 5, EndLine: 5}},
		{Title: "Run has no doc comment", Message: "Exported Run has no comment.", Severity: "low", Rule: "doc", Source: project.FindingSourceLint, Confidence: project.FindingConfidenceToolReported, Location: project.FindingLocation{Path: "main.go", StartLine: 5, EndLine: 5}},
	}
	findings, err := store.ReconcileSource(project.FindingInput{ProjectID: request.ProjectID, ProjectRevision: request.ProjectRevision}, project.FindingSourceLint, reported)
	if err != nil || len(findings) != 2 {
		t.Fatalf("findings = %+v, %v", findings, err)
	}
	fixed, other := findings[0].ID, findings[1].ID
	if err := store.SetStatus(other, project.FindingStatusInProgress); err != nil {
		t.Fatal(err)
	}
	if got := listedStatuses(t, service); got[fixed] != project.FindingStatusOpen || got[other] != project.FindingStatusOpen {
		t.Fatalf("before any task: %v", got)
	}

	open := worktreeTaskRequest(t, service, "main.go")
	open.FindingTargets[0].ID = fixed
	opened, err := service.OpenChangeInWorktree(context.Background(), open)
	if err != nil {
		t.Fatal(err)
	}
	if got := listedStatuses(t, service); got[fixed] != project.FindingStatusInProgress || got[other] != project.FindingStatusOpen {
		t.Fatalf("while a task fixes %s: %v", fixed, got)
	}
	inProgress, err := service.ListFindings(FindingFilter{Status: project.FindingStatusInProgress})
	if err != nil || len(inProgress) != 1 || inProgress[0].ID != fixed {
		t.Fatalf("in-progress filter = %+v, %v", inProgress, err)
	}

	child, _, err := service.WorktreeService(context.Background(), opened.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.DeleteChange(context.Background(), opened.ID, changeIdentity(opened.ChangeSession)); err != nil {
		t.Fatal(err)
	}
	if got := listedStatuses(t, service); got[fixed] != project.FindingStatusOpen {
		t.Fatalf("after the task was deleted: %v", got)
	}
}
