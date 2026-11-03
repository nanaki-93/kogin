package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/app/changes"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

var changeIDPattern = regexp.MustCompile(`^change-[a-f0-9]{32}$`)

// changeMetadataPath disallows metadata-directory aliases before either reads
// or writes. Source-write path policy deliberately does not admit metadata.
func changeMetadataPath(root, name string) (string, error) {
	return changes.MetadataPath(root, name)
}

func loadChangeSession(root, id string) (*ChangeSession, error) {
	if !changeIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid change session id")
	}
	path, err := changeMetadataPath(root, id+".json")
	if err != nil {
		return nil, err
	}
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read change session: %w", err)
	}
	defer input.Close()
	session, err := readChangeSessionFile(input, id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, &changeFileError{path: relativeMetadata(root, path), newer: errors.Is(err, errNewerTask), cause: err}
	}
	return session, err
}

func readChangeSessionFile(input *os.File, id string) (*ChangeSession, error) {
	if err := boundedChangeFile(input); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(input, (2<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read change history: %w", err)
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("change history has trailing data or exceeds its limit")
	}
	return decodeCompatibleChangeHistory(data, id)
}

func validateStoredChange(session ChangeSession) error {
	if err := validateStoredPlan(&session); err != nil {
		return err
	}
	if err := validateChangeFindingTargets(&session); err != nil {
		return err
	}
	if err := validateChangeIssues(session.Issues, session.FindingTargets); err != nil {
		return err
	}
	if err := validateSettledIssues(&session); err != nil {
		return err
	}
	if err := validateTestRecords(&session); err != nil {
		return err
	}
	seen, err := validateStoredTargets(session.Targets)
	if err != nil {
		return err
	}
	if err := validateStoredEdits(session.Changes, seen); err != nil {
		return err
	}
	if err := validateStoredFileReview(&session, seen); err != nil {
		return err
	}
	if session.Hash != changeProposalHash(session.Changes) {
		return fmt.Errorf("corrupt proposal identity")
	}
	if err := validatePinnedChangeTests(&session); err != nil {
		return err
	}
	if err := validateStoredScope(&session); err != nil {
		return err
	}
	if err := validateStoredReview(&session); err != nil {
		return err
	}
	return validateStoredWorkflow(session.Workflow)
}

// validateStoredReview checks the human review state: approval notes bind an
// approval, and line comments are bounded and anchored.
func validateStoredReview(session *ChangeSession) error {
	if session.ReviewNotes != "" && (session.ReviewedHash == "" || !boundedWorkflowText(session.ReviewNotes, maxApprovalNotes)) {
		return fmt.Errorf("corrupt approval notes")
	}
	return validateStoredComments(session)
}

// validateStoredTargets returns the set of captured target paths.
func validateStoredTargets(targets []ChangeTarget) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.Path] || len(target.Content) > maxChangeBytes || target.Hash != contentHash([]byte(target.Content)) && target.Exists {
			return nil, fmt.Errorf("corrupt captured change target")
		}
		seen[target.Path] = true
	}
	return seen, nil
}

func validateStoredEdits(edits []ChangeEdit, targets map[string]bool) error {
	seen := map[string]bool{}
	for _, edit := range edits {
		if !targets[edit.Path] || seen[edit.Path] || len(edit.Content) > maxChangeBytes || edit.Hash != contentHash([]byte(edit.Content)) || !validStoredEditStatus(edit, targets) {
			return fmt.Errorf("corrupt stored proposal")
		}
		seen[edit.Path] = true
	}
	return nil
}

func validStoredEditStatus(edit ChangeEdit, targets map[string]bool) bool {
	switch edit.Status {
	case "", EditAdded, EditModified:
		return edit.OldPath == ""
	case EditDeleted:
		return edit.OldPath == "" && edit.Content == ""
	case EditRenamed:
		return edit.OldPath != "" && edit.OldPath != edit.Path && targets[edit.OldPath]
	default:
		return false
	}
}

func boundedChangeFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 2*1024*1024 {
		return fmt.Errorf("change metadata exceeds 2 MiB")
	}
	return nil
}

func storeChangeSession(root string, session *ChangeSession) error {
	refreshChangeReviewDiff(session)
	settleWorkflowPause(session.Workflow)
	settleWorkflowWait(session.Workflow)
	if err := validateStoredChange(*session); err != nil {
		return err
	}
	path, err := changeMetadataPath(root, session.ID+".json")
	if err != nil {
		return err
	}
	stored := *session
	stored.StaleReason, stored.ChecksOutdated, stored.MainChanged = "", false, nil // Derived on read.
	data, err := json.MarshalIndent(&stored, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("change history exceeds 2 MiB; start another conversation")
	}
	if err := storeChangeArtifacts(root, session); err != nil {
		return err
	}
	return storage.WriteFile(path, data, 0600)
}

func storeChangeArtifacts(root string, session *ChangeSession) error {
	if len(session.Changes) == 0 {
		return nil
	}
	store, err := agentstate.Open(root)
	if err != nil {
		return fmt.Errorf("%w: open change evidence: %w", agentstate.ErrPersistence, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provenance := map[string]string{"project": session.ProjectID, "revision": session.ProjectRevision, "proposal": session.Hash, "workspace": session.WorkspaceHash, "policy": session.PolicyFingerprint}
	if err := saveChangeArtifact(ctx, store, session.ID, "proposal.json", session.Changes, provenance); err != nil {
		return err
	}
	if len(session.Checks) == 0 {
		return nil
	}
	return saveChangeArtifact(ctx, store, session.ID, "checks.json", struct {
		Options CheckOptions  `json:"options"`
		Checks  []CheckResult `json:"checks"`
	}{session.CheckOptions, session.Checks}, provenance)
}

func saveChangeArtifact(ctx context.Context, store *agentstate.Store, id, name string, value any, provenance map[string]string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode change evidence: %w", err)
	}
	_, err = store.SaveArtifact(ctx, agentstate.ArtifactWrite{
		Key:  agentstate.ArtifactKey{Session: agentstate.Key{App: "kasuri-changes", User: "local", ID: id}, Name: name},
		Kind: "inline", MIME: "application/json", Data: data, Provenance: provenance,
		Protected: true, Deduplicate: true,
	})
	if err != nil {
		return fmt.Errorf("%w: save change evidence: %w", agentstate.ErrPersistence, err)
	}
	return nil
}

// listChangeSessions summarizes every session and fails on a file it cannot
// read: callers that decide on the whole history, such as merges, must not
// overlook a task.
func listChangeSessions(root string) ([]ChangeHistoryEntry, error) {
	return listChangeSessionsWith(root, sessionListing{})
}

// sessionListing shapes a listing. freshness, when set, reports each draft
// session's current freshness. A lenient listing sets unreadable task files
// aside and skips newer ones, passing them to skipped when set.
type sessionListing struct {
	freshness func(*ChangeSession) string
	lenient   bool
	skipped   func(*changeFileError)
}

func listChangeSessionsWith(root string, listing sessionListing) ([]ChangeHistoryEntry, error) {
	path, err := changeMetadataPath(root, "")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return []ChangeHistoryEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []ChangeHistoryEntry{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !changeIDPattern.MatchString(id) {
			continue
		}
		session, err := loadChangeSession(root, id)
		if err != nil {
			if err := listing.recover(root, id, err); err != nil {
				return nil, err
			}
			continue
		}
		entry := changeHistoryEntry(session)
		if listing.freshness != nil && session.State == "draft" {
			entry.Freshness = listing.freshness(session)
		}
		result = append(result, entry)
		if len(result) > 200 {
			return nil, fmt.Errorf("change history exceeds 200 sessions")
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

// recover handles a task file a lenient listing cannot read; anything else
// stays an error.
func (listing sessionListing) recover(root, id string, err error) error {
	var problem *changeFileError
	switch {
	case !listing.lenient || !errors.As(err, &problem):
		return err
	case problem.newer:
		if listing.skipped != nil {
			listing.skipped(problem)
		}
		return nil
	default:
		return quarantineTask(root, id, problem)
	}
}

func validatePinnedChangeTests(session *ChangeSession) error {
	for _, pinned := range session.PinnedTests {
		found := false
		for _, edit := range session.Changes {
			if edit.Path == pinned.Path && edit.Hash == pinned.Hash {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("proposal dropped or changed a pinned regression test")
		}
	}
	return nil
}
