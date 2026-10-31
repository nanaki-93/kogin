package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

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
