package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nanaki-93/mini-orca/v2/internal/app/changes"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

type changeJournal struct {
	Version           int                 `json:"version"`
	Session           ChangeSession       `json:"session"`
	State             string              `json:"state"`
	PostWorkspaceHash string              `json:"post_workspace_hash"`
	Warnings          []string            `json:"warnings,omitempty"`
	Verification      *ChangeVerification `json:"verification,omitempty"`
	Commit            *ChangeCommit       `json:"commit,omitempty"`
}

func writeChangeJournal(root string, journal *changeJournal) error {
	path, err := changeMetadataPath(root, "last-mutation.json")
	if err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("change recovery journal exceeds 2 MiB")
	}
	return storage.WriteFile(path, data, 0600)
}

func readChangeJournal(root string) (*changeJournal, error) {
	path, err := changeMetadataPath(root, "last-mutation.json")
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	journal, err := decodeChangeJournal(file)
	if err != nil {
		return nil, recoveryJournalError(relativeMetadata(root, path), err)
	}
	if journal.Commit != nil && journal.Commit.State == "pending" {
		reconcileChangeCommit(context.Background(), root, journal.Commit)
	}
	return journal, nil
}

var errNewerRecoveryJournal = errors.New("written by a newer Kasuri")

func decodeChangeJournal(file *os.File) (*changeJournal, error) {
	if err := boundedChangeFile(file); err != nil {
		return nil, err
	}
	var journal changeJournal
	decoder := json.NewDecoder(io.LimitReader(file, 2*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		if journal.Version > 1 {
			return nil, errNewerRecoveryJournal
		}
		return nil, err
	}
	if journal.Version > 1 {
		return nil, errNewerRecoveryJournal
	}
	if decoder.Decode(new(any)) != io.EOF || journal.Version != 1 || validateStoredChange(journal.Session) != nil {
		return nil, fmt.Errorf("its records are inconsistent")
	}
	return &journal, nil
}

// recoveryJournalError names the change recovery journal and the remedy. It
// is never set aside: it may describe an Apply that did not finish.
func recoveryJournalError(path string, err error) error {
	if errors.Is(err, errNewerRecoveryJournal) {
		return fmt.Errorf("change recovery journal %s was written by a newer Kasuri; upgrade Kasuri to Apply or Undo in this checkout", path)
	}
	return fmt.Errorf("change recovery journal %s is unreadable (%v); check the files of your last Apply with git status and restore them if needed, then move %s away to Apply again", path, err, path)
}

func (s *Service) ApplyChange(ctx context.Context, id string, request ChangeApplyRequest) (*ChangeMutationResult, error) {
	return s.applyChangeWithWriter(ctx, id, request, atomicWrite)
}

func (s *Service) applyChangeWithWriter(ctx context.Context, id string, request ChangeApplyRequest, write func(string, []byte) error) (*ChangeMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if !request.Confirm {
		return nil, fmt.Errorf("explicit Apply confirmation is required")
	}
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	if !s.changeApproved(session) {
		return nil, fmt.Errorf("review and check the current proposal before Apply")
	}
	if err := verifyChecksWorkspace(ctx, root, session); err != nil {
		return nil, err
	}
	if ownsWorktree(session) {
		return s.acceptWorktreeChange(ctx, root, session, request)
	}
	previous, err := readChangeJournal(root)
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.State != "applied" && previous.State != "undone" {
		return nil, fmt.Errorf("recover the pending change before applying another proposal")
	}
	if err := s.verifyChangeCurrent(ctx, root, session); err != nil {
		return nil, err
	}
	journal := &changeJournal{Version: 1, Session: *session, State: "prepared"}
	journal.Commit, err = preflightChangeCommit(ctx, root, session, request)
	if err != nil {
		return nil, err
	}
	if err := writeChangeJournal(root, journal); err != nil {
		return nil, fmt.Errorf("save recovery journal; source was not changed: %w", err)
	}
	if err := writeChangeFiles(ctx, root, session, write); err != nil {
		return s.failedChangeMutation(root, journal, previous, err)
	}
	journal.State = "applied"
	if journal.Commit != nil {
		finishChangeCommit(ctx, root, journal)
	}
	return s.completeChangeMutation(root, journal, nil), nil
}

func (s *Service) changeApproved(session *ChangeSession) bool {
	return (session.Plan == nil || allChangeFilesReviewed(session)) && canHumanReviewProposal(session) && s.changeAuthority[session.ProjectID+"/"+session.ID] == "review:"+session.Hash && session.ReviewedHash == session.Hash && len(session.Checks) > 0 && requiredChecksPassed(session.Checks)
}

func (s *Service) failedChangeMutation(root string, journal, previous *changeJournal, cause error) (*ChangeMutationResult, error) {
	if rollbackErr := restoreChangeFiles(root, journal, atomicWrite); rollbackErr != nil {
		journal.State = "recovery_required"
		return s.completeChangeMutation(root, journal, []string{cause.Error(), "Recovery is required: " + rollbackErr.Error()}), nil
	}
	if previous != nil {
		if err := writeChangeJournal(root, previous); err != nil {
			return nil, fmt.Errorf("apply failed and original files restored; restoring previous recovery metadata failed: %w", err)
		}
	} else {
		path, err := changeMetadataPath(root, "last-mutation.json")
		if err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("original files restored; recovery journal cleanup failed: %w", err)
		}
	}
	return nil, fmt.Errorf("apply failed; original files restored: %w", cause)
}

func writeChangeFiles(ctx context.Context, root string, session *ChangeSession, write func(string, []byte) error) error {
	for _, edit := range session.Changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := changeTarget(session, edit.Path)
		current, err := captureChangeTarget(root, edit.Path)
		if err != nil {
			return err
		}
		if target == nil || current.Exists != target.Exists || current.Hash != target.Hash {
			return project.ErrRevisionConflict
		}
		if err := writeChangeSource(root, *target, edit.Content, write); err != nil {
			return err
		}
	}
	return nil
}

func writeChangeSource(root string, target ChangeTarget, content string, write func(string, []byte) error) error {
	path, err := project.ResolveWritePath(root, target.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if _, err := project.ResolveWritePath(root, target.Path); err != nil {
		return err
	}
	if err := write(path, []byte(content)); err != nil {
		return err
	}
	return os.Chmod(path, os.FileMode(target.Mode))
}

func validateChangeRestore(root string, journal *changeJournal) error {
	for _, edit := range journal.Session.Changes {
		target := changeTarget(&journal.Session, edit.Path)
		current, err := captureChangeTarget(root, edit.Path)
		if err != nil {
			return err
		}
		isBefore := current.Exists == target.Exists && current.Hash == target.Hash
		isAfter := current.Exists && current.Hash == edit.Hash
		if journal.State == "applied" && !isAfter || !isBefore && !isAfter {
			return project.ErrRevisionConflict
		}
	}
	return nil
}

// Preflight every path before recovery so unrelated edits prevent all writes.
func restoreChangeFiles(root string, journal *changeJournal, write func(string, []byte) error) error {
	if err := validateChangeRestore(root, journal); err != nil {
		return err
	}
	for _, edit := range journal.Session.Changes {
		target := changeTarget(&journal.Session, edit.Path)
		current, err := captureChangeTarget(root, edit.Path)
		if err != nil {
			return err
		}
		if current.Exists == target.Exists && current.Hash == target.Hash {
			continue
		}
		if !current.Exists || current.Hash != edit.Hash {
			return project.ErrRevisionConflict
		}
		if target.Exists {
			if err := writeChangeSource(root, *target, target.Content, write); err != nil {
				return err
			}
		} else {
			path, err := project.ResolveWritePath(root, edit.Path)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) UndoChange(ctx context.Context, id string, request ChangeApplyRequest) (*ChangeMutationResult, error) {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if !request.Confirm {
		return nil, fmt.Errorf("explicit Undo confirmation is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := s.manager.Root()
	journal, err := readChangeJournal(root)
	if err != nil {
		return nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if !matchesChangeUndo(journal, index, id, request) {
		return nil, project.ErrRevisionConflict
	}
	if err := verifyCommittedChange(ctx, root, journal); err != nil {
		return nil, err
	}
	if journal.State == "applied" && journal.PostWorkspaceHash != "" {
		if err := (workspaceIdentity{fingerprint: journal.PostWorkspaceHash}).verifyCurrent(ctx, root); err != nil {
			return nil, err
		}
	}
	if err := validateChangeRestore(root, journal); err != nil {
		return nil, err
	}
	journal.State = "undoing"
	journal.Verification = nil
	if err := writeChangeJournal(root, journal); err != nil {
		return nil, err
	}
	if err := restoreChangeFiles(root, journal, atomicWrite); err != nil {
		journal.State = "recovery_required"
		return s.completeChangeMutation(root, journal, []string{"Undo needs recovery: " + err.Error()}), nil
	}
	journal.State = "undone"
	return s.completeChangeMutation(root, journal, nil), nil
}

func matchesChangeUndo(journal *changeJournal, index *project.ProjectIndex, id string, request ChangeApplyRequest) bool {
	return journal != nil && journal.State != "undone" && journal.Session.ID == id && request.Hash == journal.Session.Hash && request.ProjectID == index.ProjectID && request.ProjectRevision == index.ProjectRevision && journal.Session.ProjectID == index.ProjectID
}

func (s *Service) completeChangeMutation(root string, journal *changeJournal, warnings []string) *ChangeMutationResult {
	result := &ChangeMutationResult{SessionID: journal.Session.ID, ProjectID: journal.Session.ProjectID, ProjectRevision: journal.Session.ProjectRevision, State: journal.State, Hash: journal.Session.Hash, UndoAvailable: journal.State != "undone", Warnings: append([]string{}, warnings...)}
	result.Commit, journal.Session.Commit = journal.Commit, journal.Commit
	s.clearExecutionTrust()
	index, err := s.reindexActiveProject(stopReasonApply)
	if err != nil {
		result.Warnings = append(result.Warnings, "Source changed; refresh the index: "+err.Error())
	} else {
		result.Index, result.ProjectRevision = index, index.ProjectRevision
	}
	journal.PostWorkspaceHash, err = workspaceFingerprint(context.Background(), root)
	if err != nil {
		result.Warnings = append(result.Warnings, "Source changed; workspace identity could not be captured: "+err.Error())
	}
	journal.Session.State = journal.State
	journal.Warnings = append([]string{}, result.Warnings...)
	if err := writeChangeJournal(root, journal); err != nil {
		result.Warnings = append(result.Warnings, "Source changed; the prepared recovery journal was retained: "+err.Error())
	}
	if err := storeChangeSession(root, &journal.Session); err != nil {
		result.Warnings = append(result.Warnings, "Source changed; history could not be updated: "+err.Error())
	}
	syncDirectChangeFindingCompletions(root, journal)
	delete(s.changeAuthority, journal.Session.ProjectID+"/"+journal.Session.ID)
	return result
}

func syncDirectChangeFindingCompletions(root string, journal *changeJournal) {
	if journal == nil || len(journal.Session.FindingTargets) == 0 {
		return
	}
	store, err := project.NewFindingStore(root)
	if err != nil {
		return
	}
	if journal.State == "applied" && journal.Commit != nil && journal.Commit.State == "committed" && journal.Commit.OID != "" {
		var completions []project.FindingCompletion
		for _, target := range journal.Session.FindingTargets {
			if target.ProjectID == journal.Session.ProjectID {
				completions = append(completions, project.FindingCompletion{
					FindingTarget: target,
					CommitOID:     journal.Commit.OID,
				})
			}
		}
		if len(completions) > 0 {
			_ = store.Complete(completions)
		}
		return
	}
	if journal.State == "undone" {
		for _, target := range journal.Session.FindingTargets {
			if target.ProjectID == journal.Session.ProjectID {
				_ = store.SetTargetStatus(target, project.FindingStatusOpen, "")
			}
		}
	}
}

func (s *Service) ChangeRecovery() (*ChangeMutationResult, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	root := s.manager.Root()
	journal, err := readChangeJournal(root)
	if err != nil || journal == nil {
		return nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	verification, err := s.currentChangeVerification(context.Background(), root, journal, index)
	if err != nil {
		return nil, err
	}
	return &ChangeMutationResult{SessionID: journal.Session.ID, ProjectID: journal.Session.ProjectID, ProjectRevision: index.ProjectRevision, State: journal.State, Hash: journal.Session.Hash, UndoAvailable: journal.State != "undone", Warnings: append([]string{}, journal.Warnings...), Verification: verification, Commit: journal.Commit}, nil
}

func atomicWrite(path string, content []byte) error {
	return changes.AtomicWriteSource(path, content)
}

func contentHash(content []byte) string {
	return changes.ContentHash(content)
}
