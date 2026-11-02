package app

import (
	"context"
	"errors"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// ChangeClosed reports a task closed after its agents found nothing to fix.
type ChangeClosed struct {
	Deleted         string `json:"deleted"`
	DismissedIssues int    `json:"dismissed_issues"`
}

var errNotNothingToFix = errors.New("only a task whose agents found nothing to fix can be closed; delete it instead")

// CloseChange ends a task whose run found nothing to fix: the issues it found
// do not hold are dismissed (their reasons stay on the task page until then,
// and they can be reopened in Issues) and the task is deleted. It holds no
// change, so its worktree holds no work of it.
func (s *Service) CloseChange(ctx context.Context, id string, request ChangeIdentity) (*ChangeClosed, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, err
	}
	if request.ProjectID != session.ProjectID || request.Revision != session.Revision || request.Hash != session.Hash {
		return nil, project.ErrRevisionConflict
	}
	if session.Workflow == nil || session.Workflow.Status != "nothing_to_fix" || len(session.Changes) > 0 {
		return nil, errNotNothingToFix
	}
	if err := s.dismissTargets(session.DismissedTargets); err != nil {
		return nil, err
	}
	if err := s.removeChangeLocked(root, session); err != nil {
		return nil, err
	}
	s.notifyEvent("findings")
	s.scheduleResume()
	return &ChangeClosed{Deleted: session.ID, DismissedIssues: len(session.DismissedTargets)}, nil
}

// dismissTargets dismisses issues in the checkout that holds them: the main
// checkout of a task in its own worktree.
func (s *Service) dismissTargets(targets []project.FindingTarget) error {
	if len(targets) == 0 {
		return nil
	}
	root := s.manager.Root()
	if s.owner != nil {
		root = s.owner.manager.Root()
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := store.SetTargetStatus(target, project.FindingStatusDismissed, ""); err != nil {
			return err
		}
	}
	return nil
}
