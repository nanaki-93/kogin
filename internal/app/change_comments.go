package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

// Line comments are the reviewer's requests on specific lines of a proposal.
// They change no source and no review state. An open comment (not yet sent)
// joins the next revision or agent run as a structured repair brief and is
// then marked with the revision it was sent from.

const (
	maxOpenComments    = 32
	maxStoredComments  = 128
	maxCommentText     = 1024
	maxCommentExcerpt  = 240
	maxRepairBrief     = 6144
	commentSideNew     = "new"
	commentSideOld     = "old"
	repairBriefHeading = "Reviewer line comments on the proposal. Address each one; they are the reviewer's requests about the quoted lines, not source:"
)

// ChangeComment anchors one reviewer comment to a line of a proposal version.
type ChangeComment struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Side    string `json:"side"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
	Text    string `json:"text"`
	// Hash is the proposal the comment was written on.
	Hash string `json:"hash"`
	// SentRevision is the revision whose repair brief carried the comment.
	SentRevision int64     `json:"sent_revision,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (comment ChangeComment) open() bool { return comment.SentRevision == 0 }

type ChangeCommentRequest struct {
	ChangeIdentity
	Path string `json:"path"`
	Side string `json:"side"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type ChangeCommentDeleteRequest struct {
	ChangeIdentity
	ID string `json:"id"`
}

// CommentChangeLine adds an open comment on a line of the current proposal.
func (s *Service) CommentChangeLine(ctx context.Context, id string, request ChangeCommentRequest) (*ChangeSession, error) {
	text := strings.TrimSpace(request.Text)
	if !boundedWorkflowText(text, maxCommentText) {
		return nil, fmt.Errorf("write a comment of at most %d bytes", maxCommentText)
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	if openComments(session.Comments) >= maxOpenComments || len(session.Comments) >= maxStoredComments {
		return nil, fmt.Errorf("send or delete existing comments before adding more")
	}
	excerpt, err := commentExcerpt(session, request.Path, request.Side, request.Line)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session.Comments = append(session.Comments, ChangeComment{ID: rand.Text(), Path: request.Path, Side: request.Side, Line: request.Line, Excerpt: excerpt, Text: text, Hash: session.Hash, CreatedAt: now})
	if brief, _ := repairBrief(session); len(brief) > maxRepairBrief {
		return nil, fmt.Errorf("the open comments would exceed %d bytes; send or delete some first", maxRepairBrief)
	}
	session.UpdatedAt = now
	return session, storeChangeSession(root, session)
}

// DeleteChangeComment removes an open comment; sent comments are history.
func (s *Service) DeleteChangeComment(ctx context.Context, id string, request ChangeCommentDeleteRequest) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(session.Comments, func(comment ChangeComment) bool { return comment.ID == request.ID && comment.open() })
	if index < 0 {
		return nil, fmt.Errorf("choose an open comment on this task")
	}
	session.Comments = slices.Delete(session.Comments, index, index+1)
	session.UpdatedAt = time.Now().UTC()
	return session, storeChangeSession(root, session)
}

// commentExcerpt returns the text of the commented line in the proposal's diff.
func commentExcerpt(session *ChangeSession, path, side string, line int) (string, error) {
	if side != commentSideNew && side != commentSideOld || line < 1 {
		return "", fmt.Errorf("comment on a numbered line of the new or old version")
	}
	index := slices.IndexFunc(session.Changes, func(edit ChangeEdit) bool { return edit.Path == path })
	if index < 0 {
		return "", fmt.Errorf("choose a changed file in the current proposal")
	}
	for _, candidate := range session.Changes[index].Diff.Lines {
		number := candidate.NewLine
		if side == commentSideOld {
			number = candidate.OldLine
		}
		if number == line && candidate.Kind != "hunk" && (side == commentSideNew) == (candidate.Kind != "removed") {
			return textutil.Truncate(candidate.Text, maxCommentExcerpt), nil
		}
	}
	return "", fmt.Errorf("line %d is not part of the %s version of %s", line, side, path)
}

func openComments(comments []ChangeComment) int {
	open := 0
	for _, comment := range comments {
		if comment.open() {
			open++
		}
	}
	return open
}

// repairBrief joins the open comments into the structured brief a revision or
// agent run receives, and returns their IDs. Comments written on an earlier
// version say so; their quoted line locates them.
func repairBrief(session *ChangeSession) (string, []string) {
	var brief strings.Builder
	var sent []string
	for _, comment := range session.Comments {
		if !comment.open() {
			continue
		}
		if brief.Len() == 0 {
			brief.WriteString(repairBriefHeading)
		}
		sent = append(sent, comment.ID)
		location := comment.Path + ":" + strconv.Itoa(comment.Line)
		if comment.Side == commentSideOld {
			location += " (removed line)"
		}
		if comment.Hash != session.Hash {
			location += " (on an earlier version)"
		}
		fmt.Fprintf(&brief, "\n%d. %s `%s`\n   %s", len(sent), location, strings.TrimSpace(comment.Excerpt), strings.ReplaceAll(comment.Text, "\n", "\n   "))
	}
	return brief.String(), sent
}

// withRepairBrief appends the open comments to message when asked, failing
// when there are none; it returns the comment IDs to mark sent.
func withRepairBrief(session *ChangeSession, message string, include bool) (string, []string, error) {
	if !include {
		return message, nil, nil
	}
	brief, sent := repairBrief(session)
	if brief == "" {
		return "", nil, fmt.Errorf("there are no open line comments to send")
	}
	if strings.TrimSpace(message) == "" {
		return brief, sent, nil
	}
	return strings.TrimSpace(message) + "\n\n" + brief, sent, nil
}

// markCommentsSent records the revision whose brief carried the comments.
func markCommentsSent(comments []ChangeComment, ids []string, revision int64) {
	for i := range comments {
		if slices.Contains(ids, comments[i].ID) {
			comments[i].SentRevision = revision
		}
	}
}

func validateStoredComments(session *ChangeSession) error {
	if len(session.Comments) > maxStoredComments || openComments(session.Comments) > maxOpenComments {
		return fmt.Errorf("corrupt line comments")
	}
	seen := map[string]bool{}
	for _, comment := range session.Comments {
		if seen[comment.ID] || comment.ID == "" || comment.Path == "" || comment.Hash == "" || comment.Line < 1 || comment.Side != commentSideNew && comment.Side != commentSideOld ||
			!boundedWorkflowText(comment.Text, maxCommentText) || len(comment.Excerpt) > maxCommentExcerpt || comment.SentRevision < 0 || comment.SentRevision > session.Revision {
			return fmt.Errorf("corrupt line comments")
		}
		seen[comment.ID] = true
	}
	return nil
}
