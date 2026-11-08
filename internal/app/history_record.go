package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/gitops"
	"github.com/nanaki-93/kogin/v2/internal/app/history"
	"github.com/nanaki-93/kogin/v2/internal/brand"
	"github.com/nanaki-93/kogin/v2/internal/logging"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// The history keeps what the agents did on every task after its worktree is
// gone: each store of a task writes its latest run there, and a task's end
// (accepted, merged, deleted, closed, discarded) is its outcome. Writing it
// is best effort: a task never fails because its history could not be kept.

// historyDir is the metadata directory of the project a task belongs to: a
// task in its own worktree keeps its history in the main checkout's.
func historyDir(root string) string {
	return filepath.Join(mainCheckoutRoot(root), brand.MetadataDir)
}

// withHistory runs write on the project's history, logging a failure. A
// history that cannot be opened (not one a newer Kogin wrote) is set aside
// and started again.
func withHistory(root string, write func(*history.Store) error) {
	dir := historyDir(root)
	store, err := history.Open(dir)
	if err != nil && !errors.Is(err, history.ErrNewerSchema) && history.SetAside(dir) == nil {
		logging.Error("Set aside an unreadable task history", "error", err)
		store, err = history.Open(dir)
	}
	if err == nil {
		err = write(store)
		_ = store.Close()
	}
	if err != nil {
		logging.Error("Task history not updated", "error", err)
	}
}

// recordHistory writes a task and its latest run to the history.
func recordHistory(root string, session *ChangeSession) {
	withHistory(root, func(store *history.Store) error {
		return store.Record(context.Background(), historyTask(session), historyRun(session))
	})
}

// recordOutcome writes how a task ended.
func recordOutcome(root string, session *ChangeSession, outcome string) {
	commit := ""
	if session.Commit != nil {
		commit = session.Commit.OID
	}
	withHistory(root, func(store *history.Store) error {
		return store.SetOutcome(context.Background(), session.ID, outcome, commit, time.Now().UTC())
	})
}

// recordWorktreeOutcome writes the outcome of the tasks of a worktree that is
// being merged or discarded: merged tasks are the committed ones.
func recordWorktreeOutcome(path string, removal gitops.WorktreeRemoval) {
	entries, err := listChangeSessions(path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		session, err := loadChangeSession(path, entry.ID)
		if err != nil {
			continue
		}
		switch {
		case removal == gitops.WorktreeMerged && session.Commit != nil && session.Commit.State == "committed":
			recordOutcome(path, session, history.OutcomeMerged)
		case removal == gitops.WorktreeDiscarded:
			recordOutcome(path, session, history.OutcomeDiscarded)
		}
	}
}

func historyTask(session *ChangeSession) history.Task {
	task := history.Task{ID: session.ID, ProjectID: session.ProjectID, Kind: session.Kind, Title: session.Title, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt}
	if session.Worktree != nil {
		task.Branch = session.Worktree.Branch
	}
	if session.Commit != nil && session.Commit.State == "committed" {
		task.Outcome, task.Commit = history.OutcomeAccepted, session.Commit.OID
	}
	return task
}

// historyRun is the task's latest agent run; nil when it has none.
func historyRun(session *ChangeSession) *history.Run {
	w := session.Workflow
	if w == nil || w.ID == "" {
		return nil
	}
	run := &history.Run{ID: w.ID, Attempt: w.Attempt, Status: w.Status, Failure: history.Categorize(w.Status, w.Reason),
		Reason: textutil.Truncate(w.Reason, maxHistoryReason), StartedAt: w.StartedAt, UpdatedAt: w.UpdatedAt}
	for _, stage := range w.Stages {
		run.Stages = append(run.Stages, historyStage(stage))
	}
	for _, issue := range session.Issues {
		run.Issues = append(run.Issues, history.IssueStep{FindingID: issue.FindingID, Path: issue.Path, Title: issue.Title, Status: issue.Status, Reason: textutil.Truncate(issue.Reason, maxHistoryReason), Commit: issue.Commit})
	}
	for _, specialist := range w.Specialists {
		if review := specialist.Review; review != nil {
			run.Review = append(run.Review, historyItems(review.ProposalHash, specialist.ID, history.KindFinding, review.Findings)...)
			run.Review = append(run.Review, historyItems(review.ProposalHash, specialist.ID, history.KindNote, review.Notes)...)
			run.Review = append(run.Review, historyItems(review.ProposalHash, specialist.ID, history.KindBehaviorChange, review.BehaviorChanges)...)
		}
	}
	for _, check := range session.Checks {
		run.Checks = append(run.Checks, history.Check{ProposalHash: session.Hash, Name: check.Name, State: check.State, Outcome: check.Outcome, Required: check.Required, DurationMS: check.DurationMS})
	}
	return run
}

// maxHistoryReason bounds the reasons the history keeps.
const maxHistoryReason = 1024

func historyStage(stage ChangeWorkflowStage) history.Stage {
	recorded := history.Stage{Name: stage.Name, Status: stage.Status, Failure: history.Categorize(stage.Status, stage.Reason), Reason: textutil.Truncate(stage.Reason, maxHistoryReason), StartedAt: stage.StartedAt, FinishedAt: stage.FinishedAt}
	if stage.Model != nil {
		recorded.Model, recorded.Provider = stage.Model.Model, stage.Model.ProviderOrigin
	}
	if usage := stage.Usage; usage != nil {
		recorded.ModelCalls, recorded.ToolCalls, recorded.InputTokens, recorded.OutputTokens = usage.ModelCalls, usage.ToolCalls, usage.InputTokens, usage.OutputTokens
	}
	if stage.Tools != nil && recorded.ToolCalls == 0 {
		recorded.ToolCalls = stage.Tools.Calls
	}
	return recorded
}

func historyItems(hash, reviewer, kind string, items []ReviewItem) []history.ReviewItem {
	recorded := make([]history.ReviewItem, 0, len(items))
	for _, item := range items {
		recorded = append(recorded, history.ReviewItem{ProposalHash: hash, Reviewer: reviewer, Kind: kind, Title: item.Title, Path: item.Path, Line: item.Line, Detail: item.Detail})
	}
	return recorded
}

// maxSharedBehaviorChanges bounds the earlier tasks' behavior changes an
// agent is told about.
const maxSharedBehaviorChanges = 8

// acceptedBehaviorBrief tells an agent the behavior changes earlier tasks of
// the project made and a human accepted, those near its files first, so it
// reuses their names instead of inventing a second one; empty when none.
func acceptedBehaviorBrief(root string, files []string) string {
	dirs := make([]string, 0, len(files))
	for _, file := range files {
		if dir := path.Dir(file); dir != "." && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	var changes []history.BehaviorChange
	withHistory(root, func(store *history.Store) error {
		var err error
		changes, err = store.AcceptedBehaviorChanges(context.Background(), dirs, maxSharedBehaviorChanges)
		return err
	})
	if len(changes) == 0 {
		return ""
	}
	var brief strings.Builder
	brief.WriteString("\nBehavior changes earlier tasks of this project made and a human accepted. Reuse their names and conventions (flags, commands, messages, exit codes) for the same thing; do not invent a second name:\n")
	for _, change := range changes {
		fmt.Fprintf(&brief, "- %s", change.Title)
		if change.Path != "" {
			fmt.Fprintf(&brief, " (%s)", change.Path)
		}
		if change.Detail != "" {
			brief.WriteString(": " + textutil.FirstSentence(change.Detail, 200))
		}
		brief.WriteString("\n")
	}
	return brief.String()
}

// HistoryFailures counts how the project's agent runs failed in the last
// days, by stage and category, most frequent first.
type HistoryFailures struct {
	Since    time.Time             `json:"since"`
	Failures []HistoryFailureCount `json:"failures"`
}

// HistoryFailureCount is how often one stage failed in one category.
type HistoryFailureCount struct {
	Stage   string `json:"stage"`
	Failure string `json:"failure"`
	Count   int    `json:"count"`
}

// maxHistoryDays bounds how far back a failure count looks.
const maxHistoryDays = 365

// TaskFailures counts the project's agent failures of the last days.
func (s *Service) TaskFailures(ctx context.Context, days int) (*HistoryFailures, error) {
	if days <= 0 || days > maxHistoryDays {
		return nil, fmt.Errorf("days must be between 1 and %d", maxHistoryDays)
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	report := &HistoryFailures{Since: since, Failures: []HistoryFailureCount{}}
	store, err := history.Open(historyDir(s.manager.Root()))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	counts, err := store.Failures(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, count := range counts {
		report.Failures = append(report.Failures, HistoryFailureCount{Stage: count.Stage, Failure: count.Failure, Count: count.Count})
	}
	return report, nil
}
