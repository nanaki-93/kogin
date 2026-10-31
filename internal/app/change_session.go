package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
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

func (s *Service) SendChangeMessage(ctx context.Context, id string, request ChangeMessageRequest) (*ChangeSession, error) {
	if len(request.Changes) > 0 {
		return s.updateChangeProposal(ctx, id, request)
	}
	if len(request.Message) > 8192 || strings.TrimSpace(request.Message) == "" && !request.IncludeComments {
		return nil, fmt.Errorf("provide a message of at most 8192 bytes")
	}
	runtime, err := s.changeMessageRuntime(ctx, request)
	if err != nil {
		return nil, err
	}
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	request.Message, request.sentComments, err = withRepairBrief(session, request.Message, request.IncludeComments)
	if err != nil {
		return nil, err
	}
	if len(session.Messages) >= maxChangeMessages {
		return nil, fmt.Errorf("conversation limit reached; start another task")
	}
	if request.Repair {
		return nil, fmt.Errorf("run current proposal checks before requesting repair")
	}
	return s.generateChangeWithRuntime(ctx, root, session, request, runtime)
}

func (s *Service) updateChangeProposal(ctx context.Context, id string, request ChangeMessageRequest) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	edits, err := proposalEdits(root, session, request.Changes)
	if err != nil {
		return nil, err
	}
	if edits, err = setProposal(ctx, root, session, edits); err != nil {
		return nil, err
	}
	session.Revision++
	session.Changes, session.Hash = edits, changeProposalHash(edits)
	session.Checks, session.PinnedTests = []CheckResult{}, nil
	revokeApproval(session)
	invalidateChangeWorkflow(session, "The proposal changed. Run the workflow again before human review.")
	delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
	now := time.Now().UTC()
	note := strings.TrimSpace(request.Message)
	if note != "" && len(session.Messages) < maxChangeMessages {
		session.Messages = append(session.Messages, ChangeMessage{Role: "user", Content: note, CreatedAt: now})
	}
	session.UpdatedAt = now
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	return session, nil
}

// proposalEdits validates file changes from an agent or a person: against
// the diff policy for a task in its own worktree, against the captured
// targets for a task in the main checkout.
func proposalEdits(root string, session *ChangeSession, inputs []ChangeEditInput) ([]ChangeEdit, error) {
	if ownsWorktree(session) {
		return buildWorktreeEdits(root, session, inputs)
	}
	return buildChangeEdits(session, inputs)
}

func buildChangeEdits(session *ChangeSession, inputs []ChangeEditInput) ([]ChangeEdit, error) {
	if len(inputs) == 0 || len(inputs) > maxStoredTaskPaths {
		return nil, fmt.Errorf("provide one to sixty-four file changes")
	}
	edits := []ChangeEdit{}
	seen := map[string]bool{}
	for _, input := range inputs {
		if input.Delete || input.RenamedFrom != "" {
			return nil, fmt.Errorf("deleting or renaming %s needs a task in its own worktree", boundedProposalPath(input.Path))
		}
		target, err := proposalTarget(session, input.Path)
		if err != nil {
			return nil, err
		}
		if seen[target.Path] {
			return nil, fmt.Errorf("proposal repeated %s", target.Path)
		}
		seen[target.Path] = true
		edit, err := makeChangeEdit(session, target.Path, input.Content)
		if err != nil {
			return nil, err
		}
		if edit != nil {
			edit.Summary = sanitizeEditSummary(input.Summary)
			if len(input.Criteria) > 16 {
				return nil, fmt.Errorf("at most 16 criterion references per file")
			}
			for _, criterion := range input.Criteria {
				if criterion < 1 || criterion > len(session.AcceptanceCriteria) {
					return nil, fmt.Errorf("file criterion must reference an acceptance criterion")
				}
			}
			edit.Criteria = input.Criteria
			edits = append(edits, *edit)
		}
	}
	return edits, nil
}

func (s *Service) changeMessageRuntime(ctx context.Context, request ChangeMessageRequest) (modelRuntime, error) {
	profile := request.Profile
	if profile == "" {
		profile = "code"
	}
	if !validModelSelection(profile) {
		return modelRuntime{}, fmt.Errorf("choose a catalog model ID or configured analyze, bug or code profile")
	}
	if err := s.validateModelSelections(ctx, []string{profile}); err != nil {
		return modelRuntime{}, err
	}
	runtime, _ := s.selectedRuntime(config.CodeModelScope, profile)
	if genTimeout := duration(s.conf().runtimes.function.effective.Timeout); genTimeout > duration(runtime.effective.Timeout) {
		runtime.effective.Timeout = genTimeout.String()
	}
	return runtime, requireModelRuntimeConfirmation(runtime, request.ConfirmRemoteProvider)
}

func (s *Service) generateChangeWithRuntime(ctx context.Context, root string, session *ChangeSession, request ChangeMessageRequest, runtime modelRuntime) (*ChangeSession, error) {
	if err := requireApprovedChangePlan(session); err != nil {
		return nil, err
	}
	stageZeroWorkflow := ctx.Value(changeWorkflowContextKey{}) != nil
	timed, cancel := context.WithTimeout(revisionScope(ctx, session), duration(runtime.effective.Timeout))
	defer cancel()
	prepared, err := s.prepareChangeContext(timed, root, session, request.Message, runtime, stageZeroWorkflow)
	if err != nil {
		return nil, err
	}
	defer prepared.release()
	schema := changeResponseSchema()
	authorize := func(ctx context.Context) error {
		return s.verifyChangeCurrent(ctx, root, session)
	}
	result, err := s.retryRequestAuthorized(timed, runtime, prepared.messages, &schema, authorize)
	if err != nil {
		return nil, err
	}
	explanation, edits, err := parseChangeResponse(root, result.Content, session)
	if err != nil {
		return nil, err
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, _, err := s.loadChangeForAction(timed, session.ID, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	// Comments written during generation are kept; the sent ones are marked.
	session.Comments = current.Comments
	markCommentsSent(session.Comments, request.sentComments, session.Revision)
	if request.Repair && len(session.Changes) > 0 {
		edits = preservePinnedTests(edits, session.Changes)
	} else if len(session.PinnedTests) > 0 {
		edits = preservePinnedTests(edits, session.PinnedTests)
	} else {
		edits = preservePinnedTests(edits, changeTestEdits(session))
	}
	if edits, err = setProposal(timed, root, session, edits); err != nil {
		return nil, err
	}
	session.Revision++
	session.Changes, session.Hash = edits, changeProposalHash(edits)
	session.Checks, session.PinnedTests = []CheckResult{}, nil
	revokeApproval(session)
	if ctx.Value(changeWorkflowContextKey{}) == nil {
		invalidateChangeWorkflow(session, "The proposal changed. Run the workflow again before human review.")
	}
	delete(s.changeAuthority, session.ProjectID+"/"+session.ID)
	session.ContextManifest = s.contextManifestForRuntime(prepared.manifest, runtime)
	now := time.Now().UTC()
	session.Messages = append(session.Messages, ChangeMessage{Role: "user", Content: request.Message, CreatedAt: now}, ChangeMessage{Role: "assistant", Content: explanation, CreatedAt: now})
	session.UpdatedAt = now
	if err := storeChangeSession(root, session); err != nil {
		return nil, err
	}
	return session, nil
}

func preservePinnedTests(edits, pinned []ChangeEdit) []ChangeEdit {
	for _, test := range pinned {
		found := false
		for _, edit := range edits {
			if edit.Path == test.Path {
				found = true
				break
			}
		}
		if !found {
			edits = append(edits, test)
		}
	}
	return edits
}
