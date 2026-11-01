package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type ChangeCommit = gitops.ChangeCommit

func validateCommitMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > 4096 || !utf8.ValidString(message) || strings.ContainsRune(message, 0) {
		return fmt.Errorf("provide a non-empty commit message of at most 4096 bytes")
	}
	return nil
}

func preflightChangeCommit(ctx context.Context, root string, session *ChangeSession, request ChangeApplyRequest) (*ChangeCommit, error) {
	if request.CommitMessage == "" && request.Head == "" && request.Branch == "" {
		return nil, nil
	}
	if err := validateCommitMessage(request.CommitMessage); err != nil {
		return nil, err
	}
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := requireMatchingGitIdentity(repository, request.GitIdentity); err != nil {
		return nil, err
	}
	for _, edit := range session.Changes {
		target := changeTarget(session, edit.Path)
		if err := cleanGitTarget(ctx, root, repository.Head, target); err != nil {
			return nil, err
		}
	}
	return &ChangeCommit{GitIdentity: repository.GitIdentity, State: "pending", Message: strings.TrimSpace(request.CommitMessage)}, nil
}

func cleanGitTarget(ctx context.Context, root, head string, target *ChangeTarget) error {
	if target == nil {
		return project.ErrRevisionConflict
	}
	tracked, err := gitRead(ctx, root, "ls-tree", "-z", head, "--", target.Path)
	if err != nil {
		return err
	}
	if target.Exists {
		if tracked == "" {
			return fmt.Errorf("%s has pre-existing uncommitted content; commit it or prepare the fix in a new worktree", target.Path)
		}
		if !strings.HasPrefix(tracked, gitFileMode(target.Mode)+" ") {
			return fmt.Errorf("%s has a pre-existing executable-bit change; commit it before applying", target.Path)
		}
		original, err := gitRead(ctx, root, "show", head+":"+target.Path)
		if err != nil {
			return err
		}
		if original != target.Content {
			return fmt.Errorf("%s has pre-existing edits; commit them or prepare the fix in a new worktree", target.Path)
		}
	} else if tracked != "" {
		return fmt.Errorf("%s was locally deleted; restore or commit that deletion first", target.Path)
	}
	staged, err := gitRead(ctx, root, "diff", "--cached", "--name-only", "-z", head, "--", target.Path)
	if err != nil {
		return err
	}
	if staged != "" {
		return fmt.Errorf("%s already has staged changes; commit them before applying", target.Path)
	}
	return nil
}

// A private index prevents unrelated staged changes from entering the commit.
// Raw blobs preserve the reviewed bytes without invoking attribute filters.
func commitChangeFiles(ctx context.Context, root string, journal *changeJournal) error {
	session, commit := &journal.Session, journal.Commit
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return err
	}
	if err := requireMatchingGitIdentity(repository, commit.GitIdentity); err != nil {
		return err
	}
	tree, err := writeReviewedGitTree(ctx, root, session, commit.Head)
	if err != nil {
		return err
	}
	current, err := readGitRepository(ctx, root)
	if err != nil {
		return err
	}
	if err := requireMatchingGitIdentity(current, commit.GitIdentity); err != nil {
		return err
	}

	// Build against the reviewed parent, then compare-and-swap the branch. An
	// external commit cannot become an unintended parent or lose its tree.
	oid, err := gitCommand(ctx, root, "", strings.NewReader(commit.Message+"\n"), "commit-tree", strings.TrimSpace(tree), "-p", commit.Head)
	if err != nil {
		return err
	}
	commit.OID = strings.TrimSpace(oid)
	if err := writeChangeJournal(root, journal); err != nil {
		return err
	}
	if _, err := gitRead(ctx, root, "update-ref", "-m", "kasuri: "+strings.Split(commit.Message, "\n")[0], "refs/heads/"+commit.Branch, commit.OID, commit.Head); err != nil {
		return err
	}
	commit.State = "committed"
	return synchronizeCommittedIndex(ctx, root, session, commit)
}

func synchronizeCommittedIndex(ctx context.Context, root string, session *ChangeSession, commit *ChangeCommit) error {
	current, err := readGitRepository(ctx, root)
	if err != nil {
		return err
	}
	if current.Branch != commit.Branch || current.Head != commit.OID {
		return fmt.Errorf("commit created; branch changed concurrently, so staging was left untouched")
	}
	for _, edit := range session.Changes {
		if err := cleanGitTarget(ctx, root, commit.Head, changeTarget(session, edit.Path)); err != nil {
			return fmt.Errorf("commit created; staging changed concurrently: %w", err)
		}
	}
	args := []string{"reset", "--quiet", commit.OID, "--"}
	for _, edit := range session.Changes {
		args = append(args, edit.Path)
	}
	if _, err := gitRead(ctx, root, args...); err != nil {
		return fmt.Errorf("commit %s created; refresh the staging area: %w", commit.OID, err)
	}
	return nil
}

func stageReviewedGitFile(ctx context.Context, root, index string, edit ChangeEdit) error {
	current, err := captureChangeTarget(root, edit.Path)
	if err != nil {
		return err
	}
	if !current.Exists || current.Hash != edit.Hash {
		return project.ErrRevisionConflict
	}
	blob, err := gitCommand(ctx, root, index, strings.NewReader(edit.Content), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	mode := gitFileMode(current.Mode)
	_, err = gitCommand(ctx, root, index, nil, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(blob), edit.Path)
	return err
}

func finishChangeCommit(ctx context.Context, root string, journal *changeJournal) {
	commit := journal.Commit
	if err := commitChangeFiles(ctx, root, journal); err != nil {
		commit.Error = err.Error()
		if commit.State != "committed" {
			reconcileChangeCommit(context.Background(), root, commit)
		}
	}
}

// Recovery checks the recorded candidate before offering retry. Unknown outcomes
// remain pending, including an externally advanced branch after the attempt.
func reconcileChangeCommit(ctx context.Context, root string, commit *ChangeCommit) {
	if commit.OID == "" {
		commit.State = "failed"
		return
	}
	head, err := gitRead(ctx, root, "rev-parse", "--verify", "refs/heads/"+commit.Branch)
	if err != nil {
		return
	}
	switch strings.TrimSpace(head) {
	case commit.OID:
		commit.State = "committed"
	case commit.Head:
		commit.State = "failed"
	}
}

func verifyCommittedChange(ctx context.Context, root string, journal *changeJournal) error {
	if journal.Commit == nil {
		return nil
	}
	commit := journal.Commit
	if commit.State == "pending" {
		return fmt.Errorf("inspect the pending commit outcome before Undo")
	}
	expected := commit.GitIdentity
	if commit.State == "committed" {
		expected.Head = commit.OID
	}
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return err
	}
	return requireMatchingGitIdentity(repository, expected)
}

func writeReviewedGitTree(ctx context.Context, root string, session *ChangeSession, head string) (string, error) {
	temporary, err := os.MkdirTemp("", "kasuri-git-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	index := filepath.Join(temporary, "index")
	if _, err := gitCommand(ctx, root, index, nil, "read-tree", head); err != nil {
		return "", err
	}
	for _, edit := range session.Changes {
		if err := stageReviewedGitFile(ctx, root, index, edit); err != nil {
			return "", err
		}
	}
	return gitCommand(ctx, root, index, nil, "write-tree")
}

func gitFileMode(mode uint32) string {
	if mode&0111 != 0 {
		return "100755"
	}
	return "100644"
}
