package app

import (
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func validateChangeFindingTargets(session *ChangeSession) error {
	if len(session.FindingTargets) > 16 {
		return fmt.Errorf("at most sixteen finding targets are supported")
	}
	seen := make(map[project.FindingTarget]bool)
	for _, target := range session.FindingTargets {
		if err := target.Validate(); err != nil {
			return err
		}
		if changeTarget(session, target.Path) == nil || seen[target] {
			return fmt.Errorf("finding targets must be distinct and within the captured paths")
		}
		seen[target] = true
	}
	return nil
}

func (s *Service) FindingCompletions() ([]project.FindingCompletion, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		return nil, err
	}
	return store.Completions(index.ProjectID)
}
