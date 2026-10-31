package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type ChangeVerification struct {
	SchemaVersion   int           `json:"schema_version"`
	SessionID       string        `json:"session_id"`
	ProjectID       string        `json:"project_id"`
	ProjectRevision string        `json:"project_revision"`
	ProposalHash    string        `json:"proposal_hash"`
	WorkspaceHash   string        `json:"workspace_hash"`
	Status          string        `json:"status"`
	Reason          string        `json:"reason,omitempty"`
	Checks          []CheckResult `json:"checks"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// onlyUnavailableRequiredChecks reports whether every unmet required check
// could not execute, as opposed to having run and failed.
func onlyUnavailableRequiredChecks(checks []CheckResult) bool {
	for _, check := range checks {
		if check.Required && check.State != CheckPassed && check.State != CheckUnavailable {
			return false
		}
	}
	return true
}

func (s *Service) currentChangeVerification(ctx context.Context, root string, journal *changeJournal, index *project.ProjectIndex) (*ChangeVerification, error) {
	if journal.Verification == nil {
		return nil, nil
	}
	evidence := *journal.Verification
	if evidence.SchemaVersion != 1 || evidence.SessionID != journal.Session.ID || evidence.ProposalHash != journal.Session.Hash || evidence.WorkspaceHash != journal.PostWorkspaceHash {
		return nil, fmt.Errorf("invalid stored verification identity")
	}
	if evidence.ProjectID != index.ProjectID || evidence.ProjectRevision != index.ProjectRevision || journal.State != "applied" {
		evidence.Status, evidence.Reason = "stale", "Project identity changed after verification."
		return &evidence, nil
	}
	if err := (workspaceIdentity{fingerprint: evidence.WorkspaceHash}).verifyCurrent(ctx, root); err != nil {
		if !errors.Is(err, project.ErrRevisionConflict) {
			return nil, err
		}
		evidence.Status, evidence.Reason = "stale", "Source changed after verification."
	}
	return &evidence, nil
}
