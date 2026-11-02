package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// ErrChangeInUse reports a task that cannot be deleted or re-analyzed yet.
var ErrChangeInUse = errors.New("task is in use")

// DeleteChange removes a task's history from the checkout. The request names
// the task version the user saw (its revision and hash); a stale task can be
// deleted because the project revision is not part of that identity. A task
// with a running workflow, or whose latest Apply can still be undone, is kept:
// Undo restores the task from its recovery journal.
func (s *Service) DeleteChange(ctx context.Context, id string, request ChangeIdentity) error {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return err
	}
	if request.ProjectID != session.ProjectID || request.Revision != session.Revision || request.Hash != session.Hash {
		return project.ErrRevisionConflict
	}
	if err := requireDeletableChange(session); err != nil {
		return err
	}
	if err := s.removeChangeLocked(root, session); err != nil {
		return err
	}
	s.scheduleResume() // A task waiting for this one continues.
	return nil
}

// requireDeletableChange refuses a task with running agents.
func requireDeletableChange(session *ChangeSession) error {
	if isChangePipelineActive(session.Workflow) {
		return fmt.Errorf("%w: stop its agents before deleting it", ErrChangeInUse)
	}
	return nil
}

// removeChangeLocked deletes a task that requireDeletableChange admitted.
// Caller holds jobLifecycleMu and changesMu.
func (s *Service) removeChangeLocked(root string, session *ChangeSession) error {
	path, err := changeMetadataPath(root, session.ID+".json")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if ownsWorktree(session) {
		dropIssueStepsRef(context.Background(), root, session)
	}
	delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
	s.publishEvent(Event{Kind: "workflow", SessionID: session.ID, Title: session.Title, Status: "deleted"})
	return nil
}
