package app

import (
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) Reindex() (*project.ProjectIndex, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	return s.reindexActiveProject(stopReasonReindex)
}

// reindexActiveProject stops work bound to the old source; reason tells the
// stopped tasks why.
// reindexActiveProject refreshes project facts after source changed and
// stops only the change workflows whose tasks the change made stale.
func (s *Service) reindexActiveProject(reason string) (*project.ProjectIndex, error) {
	s.cancelGoScan()
	index, err := s.manager.Reindex()
	if err == nil {
		s.resetExecutionTrust(true)
		s.refreshAnalysisRunAfterReindex()
	}
	s.stopAffectedChangeWorkflows(reason)
	s.notifyEvent("project_index")
	return index, err
}

func (s *Service) ActivateProject(root string, analysis *project.Analysis) error {
	return s.replaceActiveProject(func() error {
		if err := s.manager.Set(root, analysis); err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) RestoreActiveProject(root string, analysis *project.Analysis) error {
	return s.replaceActiveProject(func() error { return s.manager.Restore(root, analysis) })
}

func (s *Service) replaceActiveProject(activate func() error) error {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	if err := s.closeWorktreeServices(); err != nil {
		return err
	}
	s.invalidateJobsForProjectChange(stopReasonProjectSwitch)
	if err := activate(); err != nil {
		return err
	}
	s.loadProjectConfiguration(s.manager.Root())
	s.resetExecutionTrust(true)
	return nil
}

// invalidateJobsForProjectChange stops every job: the project is replaced,
// removed or shut down.
func (s *Service) invalidateJobsForProjectChange(reason string) {
	s.cancelChangeWorkflowsBecause(reason)
	s.invalidateWholeTreeJobs()
}

// invalidateWholeTreeJobs stops the jobs that depend on the whole tree and
// revokes execution trust before Git rewrites source. Change workflows keep
// running; the reindex that follows stops those the rewrite made stale.
func (s *Service) invalidateWholeTreeJobs() {
	s.interruptAnalysisRunForProjectChange()
	s.cancelGoScan()
	s.clearExecutionTrust()
}

func (s *Service) cancelGoScan() {
	s.goScan.mu.Lock()
	cancel := s.goScan.cancel
	s.goScan.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func isSemanticAnalysisCandidate(file project.IndexFile) bool {
	return !file.Binary && file.Language != "Text" && file.Language != "Markdown"
}
