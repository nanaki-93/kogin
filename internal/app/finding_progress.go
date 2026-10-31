package app

import (
	"context"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// reportFindingProgress reports each open issue a live task is fixing as in
// progress, so it leaves the open issues until the task is merged (fixed),
// deleted or discarded (open again). The tasks decide, not a stored flag: an
// issue stored in progress whose task is gone is open.
func (s *Service) reportFindingProgress(root, projectID string, findings []project.UnifiedFinding) {
	fixing := map[string]bool{}
	for _, target := range s.inProgressFindingTargets(context.Background(), root, projectID) {
		fixing[target.ID] = true
	}
	for i := range findings {
		finding := &findings[i]
		switch {
		case finding.Status == project.FindingStatusOpen && fixing[finding.ID]:
			finding.Status = project.FindingStatusInProgress
		case finding.Status == project.FindingStatusInProgress && !fixing[finding.ID]:
			finding.Status = project.FindingStatusOpen
		}
	}
}
