package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// WorktreeService keeps proposal authority, jobs and execution trust local to a
// registered checkout without replacing the currently active project.
func (s *Service) WorktreeService(ctx context.Context, path string) (*Service, *project.Manager, error) {
	canonical, root, err := s.validateRegisteredWorktreePath(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	if child, manager, err := s.cachedWorktreeService(canonical, root); err != nil || child != nil {
		return child, manager, err
	}
	return s.openWorktreeService(canonical, root)
}

func (s *Service) validateRegisteredWorktreePath(ctx context.Context, path string) (string, string, error) {
	if _, err := s.manager.Index(); err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("choose an absolute worktree path")
	}
	canonical, err := project.CanonicalRoot(path)
	if err != nil {
		return "", "", err
	}
	root := s.manager.Root()
	trees, err := readGitWorktrees(ctx, root)
	if err != nil {
		return "", "", err
	}
	for _, tree := range trees {
		if tree.Path == canonical && !tree.Prunable && canonical != root {
			return canonical, root, nil
		}
	}
	return "", "", fmt.Errorf("choose another registered worktree in the current repository")
}

func (s *Service) cachedWorktreeService(canonical, root string) (*Service, *project.Manager, error) {
	s.worktreesMu.Lock()
	defer s.worktreesMu.Unlock()
	if s.manager.Root() != root {
		return nil, nil, project.ErrRevisionConflict
	}
	if child := s.worktrees[canonical]; child != nil {
		return child, child.manager, nil
	}
	return nil, nil, nil
}

func (s *Service) openWorktreeService(canonical, root string) (*Service, *project.Manager, error) {
	manager, err := project.NewManager(canonical)
	if err != nil {
		return nil, nil, err
	}
	child, err := New(s.conf().config, manager)
	if err != nil {
		return nil, nil, err
	}
	analysis, err := child.RestoreProject(canonical)
	if err != nil {
		return nil, nil, err
	}
	if err := manager.Restore(canonical, analysis); err != nil {
		return nil, nil, err
	}
	s.worktreesMu.Lock()
	defer s.worktreesMu.Unlock()
	if s.manager.Root() != root {
		return nil, nil, project.ErrRevisionConflict
	}
	if existing := s.worktrees[canonical]; existing != nil {
		return existing, existing.manager, nil
	}
	child.owner = s
	s.worktrees[canonical] = child
	return child, manager, nil
}

func (s *Service) worktreeServices() []*Service {
	s.worktreesMu.Lock()
	defer s.worktreesMu.Unlock()
	children := make([]*Service, 0, len(s.worktrees))
	for _, child := range s.worktrees {
		children = append(children, child)
	}
	return children
}

func (s *Service) closeWorktreeServices() error {
	s.worktreesMu.Lock()
	entries := make(map[string]*Service, len(s.worktrees))
	for path, child := range s.worktrees {
		entries[path] = child
	}
	s.worktreesMu.Unlock()
	for _, child := range entries {
		child.invalidateJobsForProjectChange(stopReasonProjectSwitch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var firstErr error
	for path, child := range entries {
		if err := child.ShutdownChangeWorkflows(ctx); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("wait for worktree cleanup before switching projects: %w", err)
			}
			continue
		}
		s.worktreesMu.Lock()
		if s.worktrees[path] == child {
			delete(s.worktrees, path)
		}
		s.worktreesMu.Unlock()
	}
	return firstErr
}
