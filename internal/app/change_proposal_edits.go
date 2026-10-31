package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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
