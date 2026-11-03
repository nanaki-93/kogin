package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// verifyChangeTargetsCurrent checks the active project and context policy,
// then the session's captured sources and freshness scope. Sessions captured
// before path-scoped freshness stay bound to their project revision.
func (s *Service) verifyChangeTargetsCurrent(root string, session *ChangeSession) error {
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	if s.manager.Root() != root || index.ProjectID != session.ProjectID || session.ScopeVersion == 0 && index.ProjectRevision != session.ProjectRevision {
		return project.ErrRevisionConflict
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return err
	}
	if policy.Version() != session.PolicyFingerprint {
		return fmt.Errorf("%w: the context policy changed since this proposal was made", project.ErrRevisionConflict)
	}
	if ownsWorktree(session) {
		// Its files hold the proposal and its base is a commit, so only the
		// worktree's position can make it out of date.
		return verifyWorktreeBase(context.Background(), root, session)
	}
	return verifyCapturedSources(root, policy, session)
}

// verifyCapturedSources compares the captured targets, read context, their
// instructions and the freshness scope with the files under root, which may
// be the project or a check copy of it.
func verifyCapturedSources(root string, policy *project.ContextPolicy, session *ChangeSession) error {
	for _, expected := range capturedSources(session) {
		current, err := captureChangeTarget(root, expected.Path)
		if err != nil {
			return err
		}
		instructions, err := project.ResolveInstructions(root, expected.Path)
		if err != nil {
			return err
		}
		if current.Exists != expected.Exists || current.Hash != expected.Hash {
			return fmt.Errorf("%w: %s changed since this proposal was made", project.ErrRevisionConflict, expected.Path)
		}
		if instructions.Fingerprint != session.Instructions[expected.Path] {
			return fmt.Errorf("%w: the instructions for %s changed since this proposal was made", project.ErrRevisionConflict, expected.Path)
		}
	}
	if session.ScopeVersion == 0 {
		return nil
	}
	return verifyFreshnessScope(root, policy, session)
}

func (s *Service) verifyChangeCurrent(ctx context.Context, root string, session *ChangeSession) error {
	if err := s.verifyChangeTargetsCurrent(root, session); err != nil {
		return err
	}
	if ownsWorktree(session) {
		return verifyWorktreeProposal(ctx, root, session)
	}
	if session.ScopeVersion != 0 {
		return verifySessionBranch(ctx, root, session.Git)
	}
	if err := verifySessionGitIdentity(ctx, root, session.Git); err != nil {
		return err
	}
	return (workspaceIdentity{fingerprint: session.WorkspaceHash}).verifyCurrent(ctx, root)
}
