package app

import (
	"context"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// ReadAnalysisRun guards the project even when it has no saved run yet.
func (s *Service) ReadAnalysisRun(ctx context.Context, projectID, revision string) (*AnalysisRun, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	analysis, err := s.manager.Analysis()
	if err != nil {
		return nil, err
	}
	if analysis.ProjectID != projectID || analysis.ProjectRevision != revision {
		return nil, project.ErrRevisionConflict
	}
	return s.currentAnalysisRunLocked(ctx)
}
