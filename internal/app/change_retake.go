package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// A task in its own worktree starts from the main branch's commit of the
// time. When the main branch later changes the task's files, often by
// merging another task on the same files, the task's work is built on code
// that is no longer there, finished or not. Retaking it asks the Bug model
// whether the task's issue is still present in the main branch's current
// files: if it is, the task starts again in a new worktree from the main
// branch with its title, plan, criteria, scope and linked issues; if not, it
// is deleted and its linked issues are dismissed. Either way the old task is
// deleted, and its worktree, which may still hold its work, is left for the
// user to discard.

// ChangeMainChange names the files of a task in its own worktree that the
// main branch changed since the task's base commit.
type ChangeMainChange struct {
	Branch string   `json:"branch"`
	Head   string   `json:"head"`
	Paths  []string `json:"paths"`
}

type ChangeRetakeRequest struct {
	ChangeIdentity
	ConfirmRemoteProvider bool `json:"confirm_remote_provider"`
	// ConfirmExecution accepts that checking out the new worktree can run
	// the repository's filters, as for any Git mutation.
	ConfirmExecution bool `json:"confirm_execution"`
}

// ChangeRetake reports the decision and its effect: the task started again
// in its new worktree when it is still valid, otherwise how many of its
// linked issues were dismissed. Deleted names the old task once it is gone.
type ChangeRetake struct {
	Outcome         ChangeReanalysisOutcome `json:"outcome"`
	Reason          string                  `json:"reason"`
	Opened          *ChangeOpened           `json:"opened,omitempty"`
	Deleted         string                  `json:"deleted,omitempty"`
	DismissedIssues int                     `json:"dismissed_issues,omitempty"`
	Warnings        []string                `json:"warnings"`
}

// mainChange reports the task's files that the main branch changed since
// the task's base commit, or nil when it changed none of them.
func (s *Service) mainChange(ctx context.Context, root string, session *ChangeSession) (*ChangeMainChange, error) {
	if s.owner == nil || !ownsWorktree(session) {
		return nil, nil
	}
	// Read on every load of the task, so only the main branch's name and
	// commit, without the status of its files.
	mainRoot := s.owner.manager.Root()
	head, err := gitRead(ctx, mainRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	branch, err := gitRead(ctx, mainRoot, "branch", "--show-current")
	if err != nil {
		return nil, err
	}
	main := ChangeMainChange{Branch: strings.TrimSpace(branch), Head: strings.TrimSpace(head)}
	if main.Head == session.Worktree.Base {
		return nil, nil
	}
	paths := make([]string, 0, len(session.Targets))
	for _, target := range session.Targets {
		paths = append(paths, target.Path)
	}
	// Task paths are relative to the project root; the Git runner reads
	// every pathspec literally.
	changed, err := gitRead(ctx, root, append([]string{"diff", "--name-only", "-z", "--no-renames", session.Worktree.Base, main.Head, "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	main.Paths = slices.DeleteFunc(strings.Split(changed, "\x00"), func(file string) bool { return file == "" })
	if len(main.Paths) == 0 {
		return nil, nil
	}
	return &main, nil
}

// RetakeChange re-checks the issue of a task in its own worktree against the
// main branch's current files, once the main branch changed the task's files.
// Nothing changes unless the model answers and the task and those files are
// still the ones it was asked about.
func (s *Service) RetakeChange(ctx context.Context, id string, request ChangeRetakeRequest) (*ChangeRetake, error) {
	runtime := s.conf().runtimes.bug
	if err := requireModelRuntimeConfirmation(runtime, request.ConfirmRemoteProvider); err != nil {
		return nil, err
	}
	prepared, open, err := s.prepareRetake(ctx, id, request)
	if err != nil {
		return nil, err
	}
	timed, cancel := context.WithTimeout(revisionScope(ctx, prepared.session), duration(runtime.effective.Timeout))
	defer cancel()
	schema := reanalysisSchema()
	authorize := func(ctx context.Context) error { return s.verifyRetakeCurrent(ctx, prepared) }
	output, err := s.retryRequestAuthorized(timed, runtime, prepared.messages, &schema, authorize)
	if err != nil {
		return nil, err
	}
	stillValid, reason, err := parseReanalysis(output.Content)
	if err != nil {
		return nil, err
	}
	if err := s.verifyRetakeCurrent(timed, prepared); err != nil {
		return nil, err
	}
	if stillValid {
		return s.startRetakenChange(ctx, prepared, open, reason)
	}
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	if err := s.verifyRetakeCurrent(timed, prepared); err != nil {
		return nil, err
	}
	removed, err := s.removeReanalyzedChangeLocked(prepared, reason)
	if err != nil {
		return nil, err
	}
	return &ChangeRetake{Outcome: ReanalysisNotValid, Reason: reason, Deleted: removed.Deleted, DismissedIssues: removed.DismissedIssues, Warnings: removed.Warnings}, nil
}

// prepareRetake loads the version the user saw, captures the same task at
// the main branch's current source and admits a new worktree for it, so the
// model is asked only when a still-valid answer can be acted on.
func (s *Service) prepareRetake(ctx context.Context, id string, request ChangeRetakeRequest) (*preparedReanalysis, ChangeOpenRequest, error) {
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	if request.ProjectID != session.ProjectID || request.Revision != session.Revision || request.Hash != session.Hash {
		return nil, ChangeOpenRequest{}, project.ErrRevisionConflict
	}
	if isChangePipelineActive(session.Workflow) {
		return nil, ChangeOpenRequest{}, fmt.Errorf("%w: stop its agents before retaking it", ErrChangeInUse)
	}
	change, err := s.mainChange(ctx, root, session)
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	if change == nil {
		return nil, ChangeOpenRequest{}, fmt.Errorf("only a task in its own worktree whose files the main branch changed since it started is retaken")
	}
	if !request.ConfirmExecution {
		return nil, ChangeOpenRequest{}, fmt.Errorf("confirm the Git action and execution of repository filters or merge drivers")
	}
	main := s.owner
	mainRoot := main.manager.Root()
	index, err := main.manager.Index()
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	open := ChangeOpenRequest{ChangeCreateRequest: retakeRequest(mainRoot, session, index), Worktree: true, ConfirmExecution: true}
	if err := main.requireProjectExecutionTrust(ctx, index.ProjectRevision); err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	if _, err := main.taskWorktreeBase(ctx, open); err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	fresh, err := main.captureChangeSession(ctx, mainRoot, index, open.ChangeCreateRequest)
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	text, _, err := composeChangePromptContext(mainRoot, fresh)
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	changed := fmt.Sprintf("%s changed %s since the task started", change.Branch, strings.Join(change.Paths, ", "))
	messages, err := reanalysisMessages(session, changed, text)
	if err != nil {
		return nil, ChangeOpenRequest{}, err
	}
	return &preparedReanalysis{root: root, session: session, fresh: fresh, messages: messages}, open, nil
}

// retakeRequest restates the task for capture at the main branch's current
// source. The scope keeps the files the task was created with, which come
// first, then the files its agents added, within a new task's limit.
func retakeRequest(root string, session *ChangeSession, index *project.ProjectIndex) ChangeCreateRequest {
	request := recaptureRequest(root, session)
	request.Paths = request.Paths[:min(len(request.Paths), maxTaskPaths)]
	request.FindingTargets = slices.DeleteFunc(slices.Clone(request.FindingTargets), func(target project.FindingTarget) bool { return !slices.Contains(request.Paths, target.Path) })
	request.Issues = issuesForRequest(request.Issues, request.FindingTargets)
	request.ProjectID, request.ProjectRevision = index.ProjectID, index.ProjectRevision
	return request
}

// verifyRetakeCurrent holds the answer to the question asked: the stored task
// is the version retaken, and the main branch's files are as captured.
func (s *Service) verifyRetakeCurrent(ctx context.Context, p *preparedReanalysis) error {
	stored, err := loadChangeSession(p.root, p.session.ID)
	if err != nil {
		return err
	}
	if s.manager.Root() != p.root || stored.State != p.session.State || stored.Revision != p.session.Revision || stored.Hash != p.session.Hash {
		return project.ErrRevisionConflict
	}
	return s.owner.verifyChangeCurrent(ctx, s.owner.manager.Root(), p.fresh)
}

// startRetakenChange opens the task again in a new worktree from the main
// branch, notes why in its conversation, then deletes the old task; its
// linked issues now follow the new one.
func (s *Service) startRetakenChange(ctx context.Context, p *preparedReanalysis, open ChangeOpenRequest, reason string) (*ChangeRetake, error) {
	opened, err := s.owner.OpenChangeInWorktree(ctx, open)
	if err != nil {
		return nil, err
	}
	result := &ChangeRetake{Outcome: ReanalysisStillValid, Reason: reason, Opened: opened, Warnings: slices.Clone(opened.Warnings)}
	note := fmt.Sprintf("Retaken from %s after the main branch changed its files. Re-analyzed against them: the issue is still present. %s", p.session.Worktree.Branch, reason)
	if err := s.owner.noteChange(ctx, opened, note); err != nil {
		result.Warnings = append(result.Warnings, "The task was started again, but the note about why could not be saved: "+err.Error()+".")
	}
	if err := s.removeRetakenChange(p); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("The task was started again, but the old one on %s could not be deleted: %v. Delete it from its task page.", p.session.Worktree.Branch, err))
		return result, nil
	}
	result.Deleted = p.session.ID
	return result, nil
}

// noteChange adds an assistant note to a task just opened in its worktree.
func (s *Service) noteChange(ctx context.Context, opened *ChangeOpened, note string) error {
	child, _, err := s.WorktreeService(ctx, opened.WorktreePath)
	if err != nil {
		return err
	}
	child.changesMu.Lock()
	defer child.changesMu.Unlock()
	root := child.manager.Root()
	session, err := loadChangeSession(root, opened.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	session.Messages = append(session.Messages, ChangeMessage{Role: "assistant", Content: note, CreatedAt: now})
	session.UpdatedAt = now
	if err := storeChangeSession(root, session); err != nil {
		return err
	}
	opened.ChangeSession = session
	return nil
}

// removeRetakenChange deletes the old task if it is still the version
// retaken.
func (s *Service) removeRetakenChange(p *preparedReanalysis) error {
	s.jobLifecycleMu.Lock()
	defer s.jobLifecycleMu.Unlock()
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	stored, err := loadChangeSession(p.root, p.session.ID)
	if err != nil {
		return err
	}
	if stored.Revision != p.session.Revision || stored.Hash != p.session.Hash {
		return project.ErrRevisionConflict
	}
	if err := requireDeletableChange(p.root, stored); err != nil {
		return err
	}
	return s.removeChangeLocked(p.root, stored)
}
