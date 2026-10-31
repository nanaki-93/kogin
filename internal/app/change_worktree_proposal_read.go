package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/diff"
)

// readWorktreeProposal reads the proposal back from the worktree: tracked
// files that differ from the base commit, deleted ones, and new files Git
// does not ignore. A deletion and an addition with mostly the same lines are
// one rename. Files the context policy leaves out are not part of it.
func readWorktreeProposal(ctx context.Context, root string, session *ChangeSession, hints []ChangeEdit) ([]ChangeEdit, error) {
	base := session.Worktree.Base
	changed, err := gitRead(ctx, root, "diff", "--name-status", "-z", "--no-renames", base)
	if err != nil {
		return nil, err
	}
	untracked, err := gitRead(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	statuses := map[string]string{}
	fields := strings.Split(changed, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		statuses[fields[i+1]] = fields[i]
	}
	for _, file := range strings.Split(untracked, "\x00") {
		if file != "" {
			statuses[file] = "A"
		}
	}
	var edits []ChangeEdit
	for file, status := range statuses {
		if worktreeMetadata(file) || !policy.Decide(file).Include {
			continue
		}
		edit, ok, err := worktreeFileEdit(ctx, root, base, file, status)
		if err != nil {
			return nil, err
		}
		if ok {
			edits = append(edits, edit)
		}
	}
	edits = pairRenames(edits, hints)
	if len(edits) > maxStoredTaskPaths {
		return nil, fmt.Errorf("the task's worktree changes %d files; a task changes at most %d", len(edits), maxStoredTaskPaths)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Path < edits[j].Path })
	return edits, nil
}

// worktreeFileEdit builds one file's edit; binary and oversized files are not
// reviewable and stay out of the proposal.
func worktreeFileEdit(ctx context.Context, root, base, file, status string) (ChangeEdit, bool, error) {
	before := ""
	if status != "A" {
		content, _, exists, err := worktreeBaseFile(ctx, root, base, file)
		if err != nil {
			return ChangeEdit{}, false, nil
		}
		if exists {
			before = content
		}
	}
	if status == "D" {
		return ChangeEdit{Path: file, Status: EditDeleted, Hash: contentHash(nil), Diff: diff.BuildUnifiedDiff(file, before, "")}, true, nil
	}
	full, err := project.ResolveWritePath(root, file)
	if err != nil {
		return ChangeEdit{}, false, nil
	}
	data, err := os.ReadFile(full)
	if err != nil || len(data) > maxChangeBytes || !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
		return ChangeEdit{}, false, nil
	}
	edit := ChangeEdit{Path: file, Status: EditModified, Content: string(data), Hash: contentHash(data), Diff: diff.BuildUnifiedDiff(file, before, string(data))}
	if status == "A" {
		edit.Status = EditAdded
	}
	return edit, before != string(data) || status == "A", nil
}
