package app

import (
	"context"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// agentScope admits a request for agent records of the captured checkout.
func (s *Service) agentScope(ctx context.Context, query AgentRunQuery, root string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return s.agentHistoryScope(query, root)
}

func (s *Service) agentHistoryScope(query AgentRunQuery, root string) error {
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	if query.ProjectID == "" || query.ProjectRevision == "" || index.ProjectID != query.ProjectID || index.ProjectRevision != query.ProjectRevision || s.manager.Root() != root {
		return project.ErrRevisionConflict
	}
	return nil
}
