package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// ExecutionTrust binds remembered project consent to the current revision and
// workspace snapshot. It is unrelated to model-provider confirmation.
type ExecutionTrust struct {
	ProjectID       string     `json:"project_id"`
	ProjectRevision string     `json:"project_revision"`
	WorkspaceHash   string     `json:"workspace_hash,omitempty"`
	Trusted         bool       `json:"trusted"`
	Commands        [][]string `json:"commands"`
}

type executionTrustStore struct {
	mu             sync.Mutex
	project        string
	revision       string
	workspaceHash  string
	restorePending bool
}

func newExecutionTrustStore() *executionTrustStore {
	return &executionTrustStore{restorePending: true}
}

func (s *Service) projectCodeCommands(root, taskName string) ([][]string, error) {
	if taskName == "" {
		if commands := profileCommands(s.projectProfiles(root)); len(commands) > 0 {
			return commands, nil
		}
		return [][]string{{"go", "test", "./..."}}, nil
	}
	if !validGoTestName(taskName) {
		return nil, fmt.Errorf("task test name is invalid")
	}
	return [][]string{{"go", "test", "./...", "-run", "^" + regexp.QuoteMeta(taskName) + "$"}}, nil
}

func (s *Service) ExecutionTrust(ctx context.Context, revision, taskName string) (*ExecutionTrust, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, trust, err := s.executionTrustScope(revision, taskName)
	if err != nil {
		return nil, err
	}
	s.executionTrust.mu.Lock()
	defer s.executionTrust.mu.Unlock()
	if s.executionTrust.restorePending {
		if err := s.restoreExecutionTrustLocked(ctx, root, trust); err != nil {
			return nil, err
		}
	}
	trust.Trusted = s.executionTrust.project == trust.ProjectID && s.executionTrust.revision == trust.ProjectRevision && s.executionTrust.workspaceHash != ""
	if trust.Trusted {
		trust.WorkspaceHash = s.executionTrust.workspaceHash
	}
	return trust, nil
}

func (s *Service) executionTrustScope(revision, taskName string) (string, *ExecutionTrust, error) {
	root := s.manager.Root()
	analysis, err := s.manager.Analysis()
	if err != nil {
		return "", nil, err
	}
	if revision == "" || revision != analysis.ProjectRevision {
		return "", nil, project.ErrRevisionConflict
	}
	commands, err := s.projectCodeCommands(root, taskName)
	if err != nil {
		return "", nil, err
	}
	return root, &ExecutionTrust{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Commands: commands}, nil
}

// TrustProjectExecution records or revokes the user's remembered project consent
// after the client has shown the fixed command scope returned by ExecutionTrust.
func (s *Service) TrustProjectExecution(ctx context.Context, revision string, confirm bool) (*ExecutionTrust, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, trust, err := s.executionTrustScope(revision, "")
	if err != nil {
		return nil, err
	}
	if !confirm {
		current, err := s.ExecutionTrust(ctx, revision, "")
		if err != nil {
			return nil, err
		}
		if !current.Trusted {
			return nil, fmt.Errorf("trusted local execution requires explicit confirmation")
		}
	} else {
		trust.WorkspaceHash, err = s.executionTrustWorkspace(ctx, root, trust)
		if err != nil {
			return nil, err
		}
	}
	s.executionTrust.mu.Lock()
	defer s.executionTrust.mu.Unlock()
	if err := s.checkExecutionTrustIdentity(root, trust); err != nil {
		return nil, err
	}
	if err := writeRememberedExecutionTrust(ctx, root, trust.ProjectID, trust.Commands, confirm); err != nil {
		return nil, err
	}
	s.executionTrust.project = trust.ProjectID
	s.executionTrust.revision = trust.ProjectRevision
	s.executionTrust.workspaceHash = trust.WorkspaceHash
	s.executionTrust.restorePending = false
	trust.Trusted = confirm
	return trust, nil
}

func (s *Service) checkExecutionTrustIdentity(root string, trust *ExecutionTrust) error {
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	if s.manager.Root() != root || index.ProjectID != trust.ProjectID || index.ProjectRevision != trust.ProjectRevision {
		return project.ErrRevisionConflict
	}
	return nil
}

func (s *Service) executionTrustWorkspace(ctx context.Context, root string, trust *ExecutionTrust) (string, error) {
	if err := s.checkExecutionTrustIdentity(root, trust); err != nil {
		return "", err
	}
	index, err := s.manager.Index()
	if err != nil {
		return "", err
	}
	if err := project.VerifyProjectSources(ctx, root, index); err != nil {
		return "", err
	}
	workspaceHash, err := workspaceFingerprint(ctx, root)
	if err != nil {
		return "", err
	}
	if err := s.checkExecutionTrustIdentity(root, trust); err != nil {
		return "", err
	}
	return workspaceHash, nil
}

func (s *Service) requireProjectExecutionTrust(ctx context.Context, revision string) error {
	trust, err := s.ExecutionTrust(ctx, revision, "")
	if err != nil {
		return err
	}
	if !trust.Trusted {
		return fmt.Errorf("trusted local execution is required before running project code")
	}
	return nil
}

// requireChangeExecutionTrust checks trust for running a change session's
// checks. A path-scoped session stays current by its own scope, so trust is
// checked for the project as it is now; a legacy session needs its revision.
func (s *Service) requireChangeExecutionTrust(ctx context.Context, session *ChangeSession) error {
	revision, err := s.changeExecutionRevision(session)
	if err != nil {
		return err
	}
	return s.requireProjectExecutionTrust(ctx, revision)
}

// changeExecutionRevision is the project revision a session's checks run
// under: the current one for a path-scoped session, else its captured one.
func (s *Service) changeExecutionRevision(session *ChangeSession) (string, error) {
	if session.ScopeVersion == 0 {
		return session.ProjectRevision, nil
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		return "", err
	}
	return analysis.ProjectRevision, nil
}

func (s *Service) clearExecutionTrust() {
	s.resetExecutionTrust(false)
}

func (s *Service) resetExecutionTrust(restore bool) {
	s.executionTrust.mu.Lock()
	s.executionTrust.project = ""
	s.executionTrust.revision = ""
	s.executionTrust.workspaceHash = ""
	s.executionTrust.restorePending = restore
	s.executionTrust.mu.Unlock()
}

func validGoTestName(name string) bool {
	runes := []rune(name)
	return len(runes) > len("Test") && strings.HasPrefix(name, "Test") && unicode.IsUpper(runes[len("Test")])
}
