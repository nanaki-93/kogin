package app

import (
	"slices"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
)

// changeHistoryEntry summarizes a task for lists: what it is, where its
// workflow stands, and how far your review of its current version got.
func changeHistoryEntry(session *ChangeSession) ChangeHistoryEntry {
	entry := ChangeHistoryEntry{ID: session.ID, ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Kind: session.Kind, Title: session.Title, Revision: session.Revision, Hash: session.Hash, State: session.State, Freshness: session.Freshness, UpdatedAt: session.UpdatedAt}
	entry.Commit = session.Commit
	for _, target := range session.Targets {
		entry.Paths = append(entry.Paths, target.Path)
	}
	entry.FindingTargets = session.FindingTargets
	entry.DismissedTargets = session.DismissedTargets
	if session.Workflow != nil {
		entry.WorkflowStatus = session.Workflow.Status
		entry.WorkflowReason = session.Workflow.Reason
		entry.WorkflowPhase = workflowPhase(session)
	}
	entry.Review = reviewProgress(session)
	return entry
}

// workflowPhase is the stage a running workflow is in, with the issue a
// task that fixes its issues one at a time is on; nil when no stage runs.
func workflowPhase(session *ChangeSession) *gitops.ChangeWorkflowPhase {
	w := session.Workflow
	if !isChangePipelineActive(w) {
		return nil
	}
	running := slices.IndexFunc(w.Stages, func(stage ChangeWorkflowStage) bool { return stage.Status == "running" })
	if running < 0 {
		return nil
	}
	phase := &gitops.ChangeWorkflowPhase{Stage: w.Stages[running].Name, Repair: w.AutoRepairAttempts, ChecksOnly: w.ResumeFrom == "checks" && w.AutoRepairAttempts == 0}
	if issue := stepIssue(session); issue >= 0 && phase.Stage == "create" {
		phase.Issue, phase.Issues = issue+1, len(session.Issues)
	}
	return phase
}

// reviewProgress is where your review of a draft's current version stands
// once it waits for you: no run, or a run that reached your review. The
// daemon still checks every condition when you accept.
func reviewProgress(session *ChangeSession) *gitops.ChangeReviewProgress {
	if session.State != "draft" || len(session.Changes) == 0 || session.Hash == "" {
		return nil
	}
	if w := session.Workflow; w != nil && w.Status != "awaiting_human_review" {
		return nil
	}
	progress := &gitops.ChangeReviewProgress{Files: len(session.Changes)}
	if session.FileReviewHash == session.Hash {
		for _, edit := range session.Changes {
			if slices.Contains(session.ReviewedFiles, edit.Path) {
				progress.Viewed++
			}
		}
	}
	switch {
	case progress.Viewed < progress.Files:
		progress.Step = gitops.ReviewViewFiles
	case len(session.Checks) == 0 || !requiredChecksPassed(session.Checks):
		progress.Step = gitops.ReviewRunChecks
	default:
		progress.Step = gitops.ReviewAccept
	}
	return progress
}
