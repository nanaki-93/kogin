package app

import (
	"github.com/nanaki-93/kogin/v2/internal/project"
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
	index, err := s.manager.Reindex()
	if err == nil {
		s.resetExecutionTrust(true)
		s.refreshAnalysisRunAfterReindex()
	}
	s.stopAffectedChangeWorkflows(reason)
	s.notifyEvent("project_index")
	return index, err
}

// OnProjectActivated registers a callback run whenever the active main
// project changes through import or restore.
func (s *Service) OnProjectActivated(fn func(string)) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.onProjectActivated = fn
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
	root := s.manager.Root()
	s.loadProjectConfiguration(root)
	s.resetExecutionTrust(true)
	if s.owner == nil && s.onProjectActivated != nil {
		s.onProjectActivated(root)
	}
	s.notifyEvent("project_index")
	s.scheduleResume() // Tasks whose blocker was merged or deleted meanwhile continue.
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
	s.cancelDocsRefresh()
	s.clearExecutionTrust()
}

func isSemanticAnalysisCandidate(file project.IndexFile) bool {
	return !file.Binary && file.Language != "Text" && file.Language != "Markdown"
}
