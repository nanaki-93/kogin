package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

// Two tasks never change the same code at once. A task whose run would change
// code another open task (in another worktree, not merged yet) is already
// changing waits for it: when its run starts, or when an issue step asks for
// such a file with widen_scope. The waiting run keeps the request it was
// started with; when the other task is merged or deleted, Kasuri continues
// the waiting task on the updated main checkout (task_wait_resume.go).

// ChangeWorkflowWait names the task a run waits for and the code they share.
type ChangeWorkflowWait struct {
	TaskID       string   `json:"task_id"`
	Title        string   `json:"title"`
	WorktreePath string   `json:"worktree_path"`
	Files        []string `json:"files"`
}

// ChangeWorkflowPending is what a waiting run was started with, to start it
// again by itself: its request and the remote models it was allowed to use.
type ChangeWorkflowPending struct {
	Message           string   `json:"message"`
	ConfirmedProfiles []string `json:"confirmed_profiles,omitempty"`
	ConfirmSecurity   bool     `json:"confirm_security,omitempty"`
}

// taskWaitError ends a run that waits for another task.
type taskWaitError struct{ wait *ChangeWorkflowWait }

func (e *taskWaitError) Error() string { return waitReason(e.wait) }

func waitReason(wait *ChangeWorkflowWait) string {
	return fmt.Sprintf("Waiting for “%s”, which is changing %s. This task continues by itself on the updated code once that task is merged or deleted.", wait.Title, strings.Join(wait.Files, ", "))
}

func pendingRun(request ChangeWorkflowRequest) *ChangeWorkflowPending {
	return &ChangeWorkflowPending{Message: request.Message, ConfirmedProfiles: slices.Clone(request.ConfirmedProfiles), ConfirmSecurity: request.ConfirmSecurity}
}

// codeFiles are the files a task changes or names, without tests: tests may
// overlap.
func codeFiles(session *ChangeSession, resolver *rules.Resolver) []string {
	var files []string
	for _, target := range session.Targets {
		if !target.Test && !resolver.IsTestFile(target.Path) {
			files = append(files, target.Path)
		}
	}
	for _, grant := range session.ScopeGrants {
		if !slices.Contains(files, grant.Path) {
			files = append(files, grant.Path)
		}
	}
	return files
}

// blockingTask is the open task this one waits for to change files: a task
// in another of the main checkout's worktrees that changes one of them and is
// ahead (running, or holding a proposal or an accepted commit not merged
// yet). A task that is itself waiting is never ahead, so two never wait for
// each other. Only a task in its own worktree waits.
func (s *Service) blockingTask(ctx context.Context, self string, files []string) (*ChangeWorkflowWait, error) {
	main := s.owner
	if main == nil || len(files) == 0 {
		return nil, nil
	}
	mainRoot := main.manager.Root()
	trees, err := readGitWorktrees(ctx, mainRoot)
	if err != nil {
		return nil, err
	}
	head, err := gitRead(ctx, mainRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	resolver := rules.NewResolver(mainRoot)
	for _, tree := range trees {
		root, err := project.CanonicalRoot(tree.Path)
		if err != nil || tree.Prunable || root == mainRoot || root == s.manager.Root() {
			continue
		}
		if wait := aheadTaskIn(ctx, root, self, strings.TrimSpace(head), files, resolver); wait != nil {
			return wait, nil
		}
	}
	return nil, nil
}

// aheadTaskIn is the first task in a worktree that is ahead of this one on
// one of files.
func aheadTaskIn(ctx context.Context, root, self, mainHead string, files []string, resolver *rules.Resolver) *ChangeWorkflowWait {
	history, err := listChangeSessions(root)
	if err != nil {
		return nil
	}
	for _, entry := range history {
		if entry.ID == self || !openForOverlap(ctx, root, mainHead, entry) {
			continue
		}
		var shared []string
		for _, file := range entry.Paths {
			if slices.Contains(files, file) && !resolver.IsTestFile(file) {
				shared = append(shared, file)
			}
		}
		if len(shared) > 0 && taskAhead(root, entry) {
			return &ChangeWorkflowWait{TaskID: entry.ID, Title: entry.Title, WorktreePath: root, Files: shared}
		}
	}
	return nil
}

// openForOverlap reports whether a task still claims its files: a draft that
// is not waiting, or an accepted task whose commit main does not hold yet.
func openForOverlap(ctx context.Context, root, mainHead string, entry ChangeHistoryEntry) bool {
	switch {
	case entry.State == "draft":
		return entry.WorkflowStatus != "waiting"
	case entry.Integrated():
		_, err := gitRead(ctx, root, "merge-base", "--is-ancestor", entry.Commit.OID, mainHead)
		return err != nil
	default:
		return false
	}
}

// taskAhead reports whether a task got to its files first: it runs, holds a
// proposal or was accepted.
func taskAhead(root string, entry ChangeHistoryEntry) bool {
	if entry.State != "draft" || entry.WorkflowStatus == "running" || entry.WorkflowStatus == "canceling" {
		return true
	}
	session, err := loadChangeSession(root, entry.ID)
	return err == nil && len(session.Changes) > 0
}

// validateWorkflowWait accepts a wait and a pending request only on a
// waiting run.
func validateWorkflowWait(w *ChangeWorkflow) error {
	if w.Status != "waiting" {
		if w.Waiting != nil || w.Pending != nil {
			return fmt.Errorf("corrupt workflow wait")
		}
		return nil
	}
	wait, pending := w.Waiting, w.Pending
	if wait == nil || pending == nil || wait.TaskID == "" || len(wait.TaskID) > 100 || !boundedWorkflowText(wait.Title, 300) || wait.WorktreePath == "" || len(wait.Files) == 0 || len(wait.Files) > maxStoredTaskPaths || len(pending.Message) > 8192 || len(pending.ConfirmedProfiles) > 3 {
		return fmt.Errorf("corrupt workflow wait")
	}
	return nil
}

// settleWorkflowWait drops the wait of a run that no longer waits.
func settleWorkflowWait(w *ChangeWorkflow) {
	if w != nil && w.Status != "waiting" {
		w.Waiting, w.Pending = nil, nil
	}
}
