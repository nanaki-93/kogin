package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func (s *Service) OpenChangeSession(ctx context.Context, request ChangeCreateRequest) (*ChangeSession, error) {
	return s.openChangeSession(ctx, request, false)
}

// openChangeSession captures a task. ownWorktree marks a task opened in the
// worktree created for it: its base is the worktree's current commit.
func (s *Service) openChangeSession(ctx context.Context, request ChangeCreateRequest, ownWorktree bool) (*ChangeSession, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if index.ProjectID != request.ProjectID || index.ProjectRevision != request.ProjectRevision {
		return nil, project.ErrRevisionConflict
	}
	if err := validateChangeCreateRequest(request); err != nil {
		return nil, err
	}
	root := s.manager.Root()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	session, err := s.captureChangeSession(ctx, root, index, request)
	if err != nil {
		return nil, err
	}
	session.ID = "change-" + hex.EncodeToString(nonce[:])
	if ownWorktree {
		if session.Git == nil || session.Git.Head == "" || session.Git.Branch == "" {
			return nil, fmt.Errorf("the task's worktree has no branch and commit to start from")
		}
		session.Worktree = &ChangeWorktree{Base: session.Git.Head, Branch: session.Git.Branch}
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	history, err := listChangeSessions(root)
	if err != nil {
		return nil, err
	}
	if len(history) >= 200 {
		return nil, fmt.Errorf("change history has reached its 200-session limit")
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return nil, err
	}
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	return session, nil
}

// captureChangeSession captures a new draft task, without an ID, from the
// files under root as they are now: its targets, read context, freshness
// scope, Git identity and prompt snapshot.
func (s *Service) captureChangeSession(ctx context.Context, root string, index *project.ProjectIndex, request ChangeCreateRequest) (*ChangeSession, error) {
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session := &ChangeSession{SchemaVersion: 1, ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: request.Kind, Title: strings.TrimSpace(request.Title), AcceptanceCriteria: request.AcceptanceCriteria, Targets: []ChangeTarget{}, Changes: []ChangeEdit{}, Messages: []ChangeMessage{}, Instructions: map[string]string{}, Checks: []CheckResult{}, State: "draft", Freshness: "current", PolicyFingerprint: policy.Version(), CreatedAt: now, UpdatedAt: now}
	session.FindingTargets = append([]project.FindingTarget(nil), request.FindingTargets...)
	if err := captureChangeScope(root, session, request.Paths); err != nil {
		return nil, err
	}
	session.Git, err = captureChangeGit(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := s.captureChangePromptSnapshot(ctx, root, session, request.Plan); err != nil {
		return nil, err
	}
	session.Hash = changeProposalHash(session.Changes)
	return session, nil
}

func validateChangeCreateRequest(request ChangeCreateRequest) error {
	if len(request.Paths) == 0 || len(request.Paths) > maxTaskPaths || len(request.Title) > 200 || strings.TrimSpace(request.Title) == "" || !validChangeKind(request.Kind) {
		return fmt.Errorf("provide a task title, kind and one to %d paths", maxTaskPaths)
	}
	return validateChangeCriteria(request.AcceptanceCriteria)
}

func captureChangeScope(root string, session *ChangeSession, paths []string) error {
	for _, path := range paths {
		if changeTarget(session, path) != nil {
			return fmt.Errorf("duplicate target path")
		}
		target, err := captureChangeTarget(root, path)
		if err != nil {
			return err
		}
		instructions, err := project.ResolveInstructions(root, path)
		if err != nil {
			return err
		}
		session.Targets = append(session.Targets, target)
		session.Instructions[path] = instructions.Fingerprint
	}
	return validateChangeFindingTargets(session)
}

func validChangeKind(kind string) bool {
	return kind == "fix" || kind == "performance" || kind == "security" || kind == "feature" || kind == "instructions"
}

func validateChangeCriteria(criteria []string) error {
	if len(criteria) > 16 {
		return fmt.Errorf("at most sixteen acceptance criteria are supported")
	}
	for _, criterion := range criteria {
		if strings.TrimSpace(criterion) == "" || len(criterion) > 1024 {
			return fmt.Errorf("acceptance criteria must be non-empty bounded text")
		}
	}
	return nil
}

func (s *Service) hasActiveWorkflowJob(root string, session *ChangeSession) bool {
	if session == nil || session.Workflow == nil || !isChangePipelineActive(session.Workflow) {
		return false
	}
	s.changeWorkflow.mu.Lock()
	defer s.changeWorkflow.mu.Unlock()
	job := s.changeWorkflow.getJob(session.ID)
	return job != nil && !job.finished && job.root == root && job.id == session.Workflow.ID
}

func (s *Service) ChangeSession(ctx context.Context, id string) (*ChangeSession, error) {
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if s.manager.Root() != root || session.ProjectID != index.ProjectID {
		return nil, project.ErrRevisionConflict
	}
	if ownsWorktree(session) && session.State == "draft" && !s.hasActiveWorkflowJob(root, session) {
		if session, err = s.adoptWorktreeEdits(ctx, root, session); err != nil {
			return nil, err
		}
	}
	if session.State == "draft" {
		var verifyErr error
		if s.hasActiveWorkflowJob(root, session) {
			verifyErr = s.verifyChangeTargetsCurrent(root, session)
		} else {
			verifyErr = s.verifyChangeCurrent(ctx, root, session)
		}
		if verifyErr != nil {
			if !errors.Is(verifyErr, project.ErrRevisionConflict) && !errors.Is(verifyErr, project.ErrExcludedFile) {
				return nil, verifyErr
			}
			session.Freshness, session.StaleReason = "stale", staleReason(verifyErr)
		} else if len(session.Checks) > 0 {
			session.ChecksOutdated = errors.Is(verifyChecksWorkspace(ctx, root, session), errChecksOutdated)
		}
	}
	s.restoreChangeWorkflow(session)
	s.estimateStageCosts(session)
	return session, nil
}

func (s *Service) ChangeHistory(ctx context.Context) ([]ChangeHistoryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.manager.Index(); err != nil {
		return nil, err
	}
	history, err := listChangeSessionsWith(s.manager.Root(), sessionListing{freshness: s.listedFreshness(s.manager.Root()), lenient: true})
	if err != nil {
		return nil, err
	}
	s.changeWorkflow.mu.Lock()
	defer s.changeWorkflow.mu.Unlock()
	for i := range history {
		entry := &history[i]
		if entry.WorkflowStatus == "running" || entry.WorkflowStatus == "canceling" {
			job := s.changeWorkflow.getJob(entry.ID)
			if job == nil || job.finished || job.sessionID != entry.ID || job.root != s.manager.Root() {
				entry.WorkflowStatus = "interrupted"
				entry.WorkflowReason = "The workflow stopped before completion. Restore the conversation to run again."
			}
		}
	}
	return history, nil
}

func (s *Service) loadChangeForAction(ctx context.Context, id string, identity ChangeIdentity) (*ChangeSession, string, error) {
	root := s.manager.Root()
	if err := ctx.Err(); err != nil {
		return nil, root, err
	}
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, root, err
	}
	if session.State != "draft" || identity.ProjectID != session.ProjectID || !s.acceptsChangeRevision(session, identity.ProjectRevision) || identity.Revision != session.Revision || identity.Hash != session.Hash {
		return nil, root, project.ErrRevisionConflict
	}
	if isChangePipelineActive(session.Workflow) && ctx.Value(changeWorkflowContextKey{}) != session.Workflow.ID {
		return nil, root, fmt.Errorf("%w: this proposal belongs to an active workflow", project.ErrRevisionConflict)
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return nil, root, err
	}
	return session, root, nil
}

// acceptsChangeRevision lets a request name the project revision the session
// was captured at or, for a path-scoped session, the project's current one:
// the scope check then decides whether the session is still current.
func (s *Service) acceptsChangeRevision(session *ChangeSession, revision string) bool {
	if revision == session.ProjectRevision {
		return true
	}
	if session.ScopeVersion == 0 {
		return false
	}
	index, err := s.manager.Index()
	return err == nil && revision == index.ProjectRevision
}
