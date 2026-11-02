package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// A waiting task continues once the task it waits for is merged or deleted:
// Kasuri opens it again in a new worktree from the main checkout as it is
// now, carries over its fixed issues commit by commit until one no longer
// applies (the run fixes that one and the rest again), removes the old task
// and its worktree, and starts the agents with the waiting run's request.

// errResumeNeedsTrust is a waiting task whose blocker is merged while the
// main checkout's checks are not allowed: it continues once they are.
var errResumeNeedsTrust = errors.New("trust needed")

// scheduleResume continues, in the background, the waiting tasks whose
// blocker is merged or gone. The main checkout's service does it.
func (s *Service) scheduleResume() {
	main := s
	if s.owner != nil {
		main = s.owner
	}
	main.resumeWG.Add(1)
	go func() {
		defer main.resumeWG.Done()
		main.resumeWaitingTasks(context.Background())
	}()
}

// resumeWaitingTasks continues every waiting task that no longer has to wait.
func (s *Service) resumeWaitingTasks(ctx context.Context) {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	mainRoot := s.manager.Root()
	if mainRoot == "" {
		return
	}
	trees, err := readGitWorktrees(ctx, mainRoot)
	if err != nil {
		return
	}
	head, err := gitRead(ctx, mainRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return
	}
	for _, tree := range trees {
		root, err := project.CanonicalRoot(tree.Path)
		if err != nil || tree.Prunable || root == mainRoot {
			continue
		}
		for _, session := range waitingTasks(root) {
			if blockerOpen(ctx, strings.TrimSpace(head), session.Workflow.Waiting) {
				continue
			}
			if err := s.catchUpWaitingTask(ctx, root, session); err != nil {
				s.noteWaitingTask(ctx, root, session, err)
			}
		}
	}
}

func waitingTasks(root string) []*ChangeSession {
	history, err := listChangeSessions(root)
	if err != nil {
		return nil
	}
	var waiting []*ChangeSession
	for _, entry := range history {
		if entry.State != "draft" || entry.WorkflowStatus != "waiting" {
			continue
		}
		if session, err := loadChangeSession(root, entry.ID); err == nil && session.Workflow != nil && session.Workflow.Waiting != nil && session.Workflow.Pending != nil {
			waiting = append(waiting, session)
		}
	}
	return waiting
}

// blockerOpen reports whether the task a run waits for still claims its
// files: it exists and is not merged.
func blockerOpen(ctx context.Context, mainHead string, wait *ChangeWorkflowWait) bool {
	history, err := listChangeSessions(wait.WorktreePath)
	if err != nil {
		return false
	}
	for _, entry := range history {
		if entry.ID == wait.TaskID {
			return openForOverlap(ctx, wait.WorktreePath, mainHead, entry)
		}
	}
	return false
}

// catchUpWaitingTask opens a waiting task again on the main checkout as it is
// now and starts its agents.
func (s *Service) catchUpWaitingTask(ctx context.Context, oldRoot string, old *ChangeSession) error {
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	if err := s.requireProjectExecutionTrust(ctx, index.ProjectRevision); err != nil {
		return errResumeNeedsTrust
	}
	opened, err := s.OpenChangeInWorktree(ctx, ChangeOpenRequest{ChangeCreateRequest: waitingTaskRequest(s.manager.Root(), old, index), ConfirmExecution: true})
	if err != nil {
		return err
	}
	child, _, err := s.WorktreeService(ctx, opened.WorktreePath)
	if err != nil {
		return err
	}
	carried, err := child.carryIssueCommits(ctx, opened.ID, old)
	note := fmt.Sprintf("Continued on the updated code after “%s” was merged or deleted, from the task that waited for it on %s.", old.Workflow.Waiting.Title, old.Worktree.Branch)
	if fixed := len(slices.DeleteFunc(slices.Clone(old.Issues), func(issue ChangeIssue) bool { return issue.Status != IssueFixed })); fixed > 0 {
		note += fmt.Sprintf(" %d of its %d fixed issues carried over; the agents fix the rest.", carried, fixed)
	}
	if err != nil {
		note += " Carrying over its fixes stopped: " + err.Error() + "."
	}
	_ = s.noteChange(ctx, opened, note)
	if err := s.retireWaitingTask(ctx, oldRoot, old, opened); err != nil {
		_ = s.noteChange(ctx, opened, "The task it continues could not be removed: "+err.Error()+". Delete it from its task page.")
	}
	session, err := child.ChangeSession(ctx, opened.ID)
	if err != nil {
		return nil
	}
	if _, err := child.StartChangeWorkflow(ctx, opened.ID, resumedRun(session, old)); err != nil {
		_ = s.noteChange(ctx, opened, "The agents could not start by themselves: "+err.Error()+". Start them from this page.")
	}
	return nil
}

// waitingTaskRequest restates a waiting task for the main checkout as it is
// now. Issues it found do not hold are linked again: the run settles them on
// the updated code.
func waitingTaskRequest(root string, old *ChangeSession, index *project.ProjectIndex) ChangeCreateRequest {
	relinked := *old
	relinked.FindingTargets = append(slices.Clone(old.FindingTargets), old.DismissedTargets...)
	return retakeRequest(root, &relinked, index)
}

// resumedRun is the waiting run's request for the task that continues it.
func resumedRun(session, old *ChangeSession) ChangeWorkflowRequest {
	w, pending := old.Workflow, old.Workflow.Pending
	request := ChangeWorkflowRequest{ChangeIdentity: identityForChange(session), StreamOutput: w.StreamOutput, Message: pending.Message, Models: w.Models, ConfirmedProfiles: slices.Clone(pending.ConfirmedProfiles), ConfirmSecurity: pending.ConfirmSecurity, AutoRepair: w.AutoRepair}
	if session.Plan != nil {
		request.PlanHash = session.Plan.Hash
	}
	return request
}

// retireWaitingTask removes the task that waited, now continued by opened,
// and its worktree with what it held.
func (s *Service) retireWaitingTask(ctx context.Context, oldRoot string, old *ChangeSession, opened *ChangeOpened) error {
	child, _, err := s.WorktreeService(ctx, oldRoot)
	if err != nil {
		return err
	}
	if err := child.removeWaitingTask(old); err != nil {
		return err
	}
	return s.abandonTaskWorktree(ctx, oldRoot)
}

// removeWaitingTask deletes a task that still waits as it was read.
func (s *Service) removeWaitingTask(old *ChangeSession) error {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	root := s.manager.Root()
	stored, err := loadChangeSession(root, old.ID)
	if err != nil {
		return err
	}
	if stored.Revision != old.Revision || stored.Hash != old.Hash || stored.Workflow == nil || stored.Workflow.Status != "waiting" {
		return project.ErrRevisionConflict
	}
	return s.removeChangeLocked(root, stored)
}

// abandonTaskWorktree removes a worktree left without tasks, with its work
// and its branch.
func (s *Service) abandonTaskWorktree(ctx context.Context, path string) error {
	if history, err := listChangeSessions(path); err != nil || len(history) > 0 {
		return errors.Join(err, errors.New("the worktree still holds tasks"))
	}
	repository, err := readGitRepository(ctx, s.manager.Root())
	if err != nil {
		return err
	}
	head, err := gitRead(ctx, path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	source, err := gitops.AbandonedSource(ctx, repository, GitMutationRequest{Path: path, SourceHead: strings.TrimSpace(head)})
	if err != nil {
		return err
	}
	if err := s.removeGitWorktree(ctx, repository.Root, source, gitops.WorktreeAbandoned); err != nil {
		return err
	}
	return gitops.DeleteAbandonedBranch(ctx, repository.Root, source.GitIdentity)
}

// noteWaitingTask says on a waiting task why it did not continue: it waits
// for the main checkout's checks to be allowed, or it stopped waiting.
func (s *Service) noteWaitingTask(ctx context.Context, root string, session *ChangeSession, cause error) {
	child, _, err := s.WorktreeService(ctx, root)
	if err != nil {
		return
	}
	child.changesMu.Lock()
	defer child.changesMu.Unlock()
	current, err := loadChangeSession(root, session.ID)
	if err != nil || current.Revision != session.Revision || current.Workflow == nil || current.Workflow.Status != "waiting" {
		return
	}
	w := current.Workflow
	if errors.Is(cause, errResumeNeedsTrust) {
		w.Reason = fmt.Sprintf("“%s” is no longer in the way. Allow running checks again (Settings → Trust) and this task continues by itself on the updated code.", w.Waiting.Title)
	} else {
		w.Status, w.Reason = "failed", "Could not continue on the updated code: "+clip(cause.Error(), 600)+". Run the agents again to retry."
	}
	w.UpdatedAt, current.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if storeChangeSession(root, current) == nil {
		child.notifyWorkflow(ctx, current, "")
	}
}
