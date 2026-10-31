package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
)

// ChangeWorktree marks a task that owns its worktree. Its proposal lives in
// the worktree's files: it is whatever differs from Base, new, deleted and
// renamed files included. Accepting it commits on Branch.
type ChangeWorktree struct {
	Base   string `json:"base"`
	Branch string `json:"branch"`
}

// Edit statuses. A proposal stored before statuses existed has none: its
// edits add or modify their file.
const (
	EditAdded    = "added"
	EditModified = "modified"
	EditDeleted  = "deleted"
	EditRenamed  = "renamed"
)

func ownsWorktree(session *ChangeSession) bool {
	return session != nil && session.Worktree != nil
}

// worktreeMetadata is never part of a proposal.
func worktreeMetadata(file string) bool {
	first := strings.SplitN(file, "/", 2)[0]
	return first == ".mini-orca" || first == ".worktrees" || first == ".git"
}

// worktreeWritable is the diff policy that replaces the target list for a
// task in its own worktree: any project file the context policy admits,
// except Mini-Orca and Git metadata and, unless the task edits instructions,
// the AGENTS.md files whose rules govern the task.
func worktreeWritable(policy *project.ContextPolicy, session *ChangeSession, file string) error {
	clean := path.Clean(filepath.ToSlash(file))
	switch {
	case clean != file || clean == "." || strings.HasPrefix(clean, "../") || path.IsAbs(clean):
		return fmt.Errorf("proposal wrote %s, which is not a plain project-relative path", boundedProposalPath(file))
	case worktreeMetadata(clean):
		return fmt.Errorf("proposal wrote %s, which belongs to Git or Mini-Orca", boundedProposalPath(file))
	case path.Base(clean) == "AGENTS.md" && session.Kind != "instructions":
		return fmt.Errorf("proposal wrote %s; a task cannot change the rules it is checked against", boundedProposalPath(file))
	}
	if decision := policy.Decide(clean); !decision.Include {
		return fmt.Errorf("proposal wrote %s, which the project's context policy leaves out (%s)", boundedProposalPath(file), decision.Reason)
	}
	return nil
}

// buildWorktreeEdits validates a model's file changes against the diff
// policy. Unlike the target list of a task in the main checkout, it admits
// new, deleted and renamed files anywhere the policy allows.
func buildWorktreeEdits(root string, session *ChangeSession, inputs []ChangeEditInput) ([]ChangeEdit, error) {
	if len(inputs) == 0 || len(inputs) > maxStoredTaskPaths {
		return nil, fmt.Errorf("provide one to sixty-four file changes")
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	edits := make([]ChangeEdit, 0, len(inputs))
	for _, input := range inputs {
		edit, err := worktreeEdit(policy, session, input)
		if err != nil {
			return nil, err
		}
		for _, file := range []string{edit.Path, edit.OldPath} {
			if file != "" && seen[file] {
				return nil, fmt.Errorf("proposal repeated %s", file)
			}
			seen[file] = file != ""
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

func worktreeEdit(policy *project.ContextPolicy, session *ChangeSession, input ChangeEditInput) (ChangeEdit, error) {
	if err := worktreeWritable(policy, session, input.Path); err != nil {
		return ChangeEdit{}, err
	}
	edit := ChangeEdit{Path: input.Path, Content: input.Content, Summary: sanitizeEditSummary(input.Summary), Criteria: input.Criteria}
	if len(input.Criteria) > 16 {
		return ChangeEdit{}, fmt.Errorf("at most 16 criterion references per file")
	}
	for _, criterion := range input.Criteria {
		if criterion < 1 || criterion > len(session.AcceptanceCriteria) {
			return ChangeEdit{}, fmt.Errorf("file criterion must reference an acceptance criterion")
		}
	}
	switch {
	case input.Delete:
		if input.Content != "" || input.RenamedFrom != "" {
			return ChangeEdit{}, fmt.Errorf("a deletion of %s carries no content or rename", boundedProposalPath(input.Path))
		}
		edit.Status = EditDeleted
	case input.RenamedFrom != "":
		if err := worktreeWritable(policy, session, input.RenamedFrom); err != nil {
			return ChangeEdit{}, err
		}
		edit.Status, edit.OldPath = EditRenamed, input.RenamedFrom
	case input.Content == "":
		return ChangeEdit{}, fmt.Errorf("proposal emptied %s; mark a deletion with delete instead", boundedProposalPath(input.Path))
	}
	if !utf8.ValidString(edit.Content) || strings.ContainsRune(edit.Content, 0) || len(edit.Content) > maxChangeBytes {
		return ChangeEdit{}, fmt.Errorf("proposal for %s is not UTF-8 text within 256 KiB", boundedProposalPath(input.Path))
	}
	return edit, nil
}

// worktreeBaseFile reads a file as it is in the task's base commit.
func worktreeBaseFile(ctx context.Context, root, base, file string) (content string, mode uint32, exists bool, err error) {
	listed, err := gitRead(ctx, root, "ls-tree", "-z", base, "--", file)
	if err != nil {
		return "", 0, false, err
	}
	if listed == "" {
		return "", 0, false, nil
	}
	mode = 0o644
	if strings.HasPrefix(listed, "100755 ") {
		mode = 0o755
	}
	if !strings.HasPrefix(listed, "100644 ") && !strings.HasPrefix(listed, "100755 ") {
		return "", 0, false, fmt.Errorf("%s is not a regular file in the task's base commit", boundedProposalPath(file))
	}
	content, err = gitRead(ctx, root, "cat-file", "blob", base+":"+file)
	if err != nil {
		return "", 0, false, err
	}
	if len(content) > maxChangeBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return "", 0, false, fmt.Errorf("%s is not UTF-8 text within 256 KiB in the task's base commit", boundedProposalPath(file))
	}
	return content, mode, true, nil
}

// ensureWorktreeTargets adds, for every file the edits touch that the task
// does not hold yet, a target with its content in the base commit, so every
// edit keeps a base to diff, ratchet and restore against.
func ensureWorktreeTargets(ctx context.Context, root string, session *ChangeSession, edits []ChangeEdit) error {
	resolver := rules.NewResolver(root)
	for _, edit := range edits {
		for _, file := range []string{edit.Path, edit.OldPath} {
			if file == "" || changeTarget(session, file) != nil {
				continue
			}
			if len(session.Targets) >= maxStoredTaskPaths {
				return fmt.Errorf("a task changes at most %d files", maxStoredTaskPaths)
			}
			content, mode, exists, err := worktreeBaseFile(ctx, root, session.Worktree.Base, file)
			if err != nil {
				return err
			}
			target := ChangeTarget{Path: file, Exists: exists, Content: content, Mode: mode, Test: resolver.IsTestFile(file)}
			if exists {
				target.Hash = contentHash([]byte(content))
			} else {
				target.Mode = 0o644
			}
			instructions, err := project.ResolveInstructions(root, file)
			if err != nil {
				return err
			}
			if session.Instructions == nil {
				session.Instructions = map[string]string{}
			}
			session.Targets = append(session.Targets, target)
			session.Instructions[file] = instructions.Fingerprint
		}
	}
	return nil
}

// writeProposalToWorktree makes the worktree's files hold exactly the base
// plus edits: files of the previous proposal that the new one leaves alone
// return to their base content.
func writeProposalToWorktree(root string, session *ChangeSession, edits []ChangeEdit) error {
	desired := map[string]*ChangeEdit{}
	restore := func(file string) {
		if file != "" {
			desired[file] = nil
		}
	}
	for _, prior := range session.Changes {
		restore(prior.Path)
		restore(prior.OldPath)
	}
	for i := range edits {
		edit := &edits[i]
		restore(edit.OldPath)
		if edit.OldPath != "" {
			desired[edit.OldPath] = &ChangeEdit{Path: edit.OldPath, Status: EditDeleted}
		}
		desired[edit.Path] = edit
	}
	files := make([]string, 0, len(desired))
	for file := range desired {
		files = append(files, file)
	}
	sort.Strings(files)
	for _, file := range files {
		if err := writeWorktreeFile(root, session, file, desired[file]); err != nil {
			return err
		}
	}
	return nil
}

func writeWorktreeFile(root string, session *ChangeSession, file string, edit *ChangeEdit) error {
	target := changeTarget(session, file)
	if target == nil {
		return fmt.Errorf("%w: %s has no base", project.ErrRevisionConflict, file)
	}
	full, err := project.ResolveWritePath(root, file)
	if err != nil {
		return err
	}
	content, keep := target.Content, target.Exists
	if edit != nil {
		content, keep = edit.Content, edit.Status != EditDeleted
	}
	if !keep {
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if current, err := os.ReadFile(full); err == nil && bytes.Equal(current, []byte(content)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return writeChangeSource(root, *target, content, atomicWrite)
}

// setProposal stores new edits as the task's proposal. For a task in its own
// worktree, the edits are written into the worktree and the proposal is read
// back from it, so it is exactly the worktree's diff against the base; each
// file keeps the summary and criteria the agent gave it.
func setProposal(ctx context.Context, root string, session *ChangeSession, edits []ChangeEdit) ([]ChangeEdit, error) {
	if !ownsWorktree(session) {
		return edits, nil
	}
	if err := ensureWorktreeTargets(ctx, root, session, edits); err != nil {
		return nil, err
	}
	if err := writeProposalToWorktree(root, session, edits); err != nil {
		return nil, err
	}
	read, err := readWorktreeProposal(ctx, root, session, edits)
	if err != nil {
		return nil, err
	}
	if err := ensureWorktreeTargets(ctx, root, session, read); err != nil {
		return nil, err
	}
	given := map[string]ChangeEdit{}
	for _, edit := range edits {
		given[edit.Path] = edit
	}
	for i := range read {
		if edit, ok := given[read[i].Path]; ok {
			read[i].Summary, read[i].Criteria = edit.Summary, edit.Criteria
		}
	}
	return read, nil
}

// verifyProposalInTree checks that a copy of the worktree holds the stored
// proposal, file by file.
func verifyProposalInTree(workspace string, session *ChangeSession) error {
	for _, edit := range session.Changes {
		full, err := project.ResolveWritePath(workspace, edit.Path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		switch {
		case edit.Status == EditDeleted:
			if err == nil {
				return fmt.Errorf("%w: %s came back in the task's worktree", project.ErrRevisionConflict, edit.Path)
			}
		case err != nil || contentHash(data) != edit.Hash:
			return fmt.Errorf("%w: %s changed in the task's worktree", project.ErrRevisionConflict, edit.Path)
		}
	}
	return nil
}

// revertProposalInTree turns a copy of the worktree back into the base tree.
func revertProposalInTree(workspace string, session *ChangeSession) error {
	for _, edit := range session.Changes {
		for _, file := range []string{edit.Path, edit.OldPath} {
			if file == "" {
				continue
			}
			target := changeTarget(session, file)
			full, err := project.ResolveWritePath(workspace, file)
			if err != nil || target == nil {
				return fmt.Errorf("%w: %s has no base", project.ErrRevisionConflict, file)
			}
			if !target.Exists {
				if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
					return err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(target.Content), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}
