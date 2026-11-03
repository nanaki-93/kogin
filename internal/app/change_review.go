package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project/diff"
)

type ChangeFileReviewRequest struct {
	ChangeIdentity
	Path string `json:"path"`
}

// ReviewChangeFile records explicit inspection without running checks or models.
func (s *Service) ReviewChangeFile(ctx context.Context, id string, request ChangeFileReviewRequest) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(session.Changes, func(edit ChangeEdit) bool { return edit.Path == request.Path }) {
		return nil, fmt.Errorf("choose a changed file in the current proposal")
	}
	if session.FileReviewHash != session.Hash {
		session.ReviewedFiles = nil
	}
	session.FileReviewHash = session.Hash
	if !slices.Contains(session.ReviewedFiles, request.Path) {
		session.ReviewedFiles = append(session.ReviewedFiles, request.Path)
	}
	if allChangeFilesReviewed(session) {
		session.LastReviewedChanges = append([]ChangeEdit(nil), session.Changes...)
		session.LastReviewedHash = session.Hash
	}
	session.UpdatedAt = time.Now().UTC()
	return session, storeChangeSession(root, session)
}

func allChangeFilesReviewed(session *ChangeSession) bool {
	if session.FileReviewHash != session.Hash || len(session.Changes) == 0 {
		return false
	}
	for _, edit := range session.Changes {
		if !slices.Contains(session.ReviewedFiles, edit.Path) {
			return false
		}
	}
	return true
}

func refreshChangeReviewDiff(session *ChangeSession) {
	if session.FileReviewHash != session.Hash {
		session.FileReviewHash, session.ReviewedFiles = "", nil
	}
	session.ReviewDiff = nil
	if session.LastReviewedHash == "" {
		return
	}
	previous := map[string]string{}
	next := map[string]ChangeEdit{}
	for _, edit := range session.LastReviewedChanges {
		previous[edit.Path] = edit.Content
	}
	for _, edit := range session.Changes {
		next[edit.Path] = edit
	}
	for _, target := range session.Targets {
		before, ok := previous[target.Path]
		if !ok {
			before = target.Content
		}
		after, ok := next[target.Path]
		if !ok {
			after = ChangeEdit{Path: target.Path, Content: target.Content, Hash: target.Hash, Summary: "Returned to the captured source."}
		}
		if before != after.Content {
			after.Diff = diff.BuildUnifiedDiff(target.Path, before, after.Content)
			session.ReviewDiff = append(session.ReviewDiff, after)
		}
	}
}

func validateStoredFileReview(session *ChangeSession, targets map[string]bool) error {
	if err := validateStoredEdits(session.LastReviewedChanges, targets); err != nil {
		return err
	}
	if (len(session.LastReviewedChanges) > 0) != (session.LastReviewedHash != "") || session.LastReviewedHash != "" && session.LastReviewedHash != changeProposalHash(session.LastReviewedChanges) {
		return fmt.Errorf("corrupt previous review")
	}
	seen := map[string]bool{}
	for _, path := range session.ReviewedFiles {
		if seen[path] || session.FileReviewHash != session.Hash || !slices.ContainsFunc(session.Changes, func(edit ChangeEdit) bool { return edit.Path == path }) {
			return fmt.Errorf("corrupt file review")
		}
		seen[path] = true
	}
	return nil
}
