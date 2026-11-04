package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Task is a task as the history keeps it. Outcome, when set, is how it
// ended so far: an accepted task can still be merged, deleted or discarded,
// and those stay.
type Task struct {
	ID, ProjectID, Kind, Title, Branch string
	Outcome, Commit                    string
	CreatedAt, UpdatedAt               time.Time
}

// Run is one agent run of a task and what it did.
type Run struct {
	ID      string
	Attempt int
	// Status is how the run stands or ended; Failure is its category when
	// it failed or paused (see Failure*).
	Status, Failure, Reason string
	StartedAt, UpdatedAt    time.Time
	Stages                  []Stage
	Issues                  []IssueStep
	Review                  []ReviewItem
	Checks                  []Check
}

// Stage is one stage of a run.
type Stage struct {
	Name, Status, Failure, Reason string
	Model, Provider               string
	StartedAt, FinishedAt         time.Time
	ModelCalls, ToolCalls         int
	InputTokens, OutputTokens     int64
}

// IssueStep is how one issue of a task that fixes issues one at a time ended.
type IssueStep struct {
	FindingID, Path, Title, Status, Reason, Commit string
}

// Review item kinds.
const (
	KindFinding        = "finding"
	KindNote           = "note"
	KindBehaviorChange = "behavior_change"
)

// ReviewItem is one finding, note or behavior change of a reviewer.
type ReviewItem struct {
	ProposalHash, Reviewer, Kind string
	Title, Path, Detail          string
	Line                         int
}

// Check is one check result of the run's proposal.
type Check struct {
	ProposalHash, Name, State, Outcome string
	Required                           bool
	DurationMS                         int64
}

// Record writes a task and, when run is set, replaces what the history holds
// for that run with it.
func (s *Store) Record(ctx context.Context, task Task, run *Run) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	outcomeAt := ""
	if task.Outcome != "" {
		outcomeAt = stamp(task.UpdatedAt)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, project_id, kind, title, branch, created_at, updated_at, outcome, outcome_at, commit_oid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, title = excluded.title, branch = excluded.branch, updated_at = excluded.updated_at,
			outcome = CASE WHEN excluded.outcome != '' AND tasks.outcome = '' THEN excluded.outcome ELSE tasks.outcome END,
			outcome_at = CASE WHEN excluded.outcome != '' AND tasks.outcome = '' THEN excluded.outcome_at ELSE tasks.outcome_at END,
			commit_oid = CASE WHEN excluded.commit_oid != '' THEN excluded.commit_oid ELSE tasks.commit_oid END`,
		task.ID, task.ProjectID, task.Kind, task.Title, task.Branch, stamp(task.CreatedAt), stamp(task.UpdatedAt), task.Outcome, outcomeAt, task.Commit); err != nil {
		return fmt.Errorf("record task: %w", err)
	}
	if run != nil {
		if err := recordRun(ctx, tx, task.ID, *run); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordRun(ctx context.Context, tx *sql.Tx, taskID string, run Run) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, task_id, attempt, status, failure, reason, started_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET attempt = excluded.attempt, status = excluded.status, failure = excluded.failure, reason = excluded.reason, updated_at = excluded.updated_at`,
		run.ID, taskID, run.Attempt, run.Status, run.Failure, run.Reason, stamp(run.StartedAt), stamp(run.UpdatedAt)); err != nil {
		return fmt.Errorf("record run: %w", err)
	}
	for _, table := range []string{"stages", "issue_steps", "review_items", "checks"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE run_id = ?", run.ID); err != nil {
			return fmt.Errorf("replace %s: %w", table, err)
		}
	}
	for i, stage := range run.Stages {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stages (run_id, position, name, status, failure, reason, started_at, finished_at, model, provider, model_calls, tool_calls, input_tokens, output_tokens)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, i, stage.Name, stage.Status, stage.Failure, stage.Reason, stamp(stage.StartedAt), stamp(stage.FinishedAt), stage.Model, stage.Provider, stage.ModelCalls, stage.ToolCalls, stage.InputTokens, stage.OutputTokens); err != nil {
			return fmt.Errorf("record stage: %w", err)
		}
	}
	for i, issue := range run.Issues {
		if _, err := tx.ExecContext(ctx, `INSERT INTO issue_steps (run_id, position, finding_id, path, title, status, reason, commit_oid) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, i, issue.FindingID, issue.Path, issue.Title, issue.Status, issue.Reason, issue.Commit); err != nil {
			return fmt.Errorf("record issue step: %w", err)
		}
	}
	for _, item := range run.Review {
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_items (run_id, proposal_hash, reviewer, kind, title, path, line, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, item.ProposalHash, item.Reviewer, item.Kind, item.Title, item.Path, item.Line, item.Detail); err != nil {
			return fmt.Errorf("record review item: %w", err)
		}
	}
	for _, check := range run.Checks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO checks (run_id, proposal_hash, name, required, state, outcome, duration_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			run.ID, check.ProposalHash, check.Name, check.Required, check.State, check.Outcome, check.DurationMS); err != nil {
			return fmt.Errorf("record check: %w", err)
		}
	}
	return nil
}

// Task outcomes.
const (
	OutcomeAccepted  = "accepted"
	OutcomeMerged    = "merged"
	OutcomeDeleted   = "deleted"
	OutcomeClosed    = "closed"
	OutcomeDiscarded = "discarded"
)

// SetOutcome records how a task ended; a task the history does not hold is
// left out.
func (s *Store) SetOutcome(ctx context.Context, taskID, outcome, commit string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET outcome = ?, outcome_at = ?, commit_oid = CASE WHEN ? = '' THEN commit_oid ELSE ? END WHERE id = ?`,
		outcome, stamp(at), commit, commit, taskID)
	return err
}

// BehaviorChange is a behavior change a human accepted with a task.
type BehaviorChange struct {
	TaskTitle, Title, Path, Detail string
}

// AcceptedBehaviorChanges are the latest behavior changes of tasks that were
// accepted or merged, newest first; with paths, those in files of the same
// directories come first.
func (s *Store) AcceptedBehaviorChanges(ctx context.Context, dirs []string, limit int) ([]BehaviorChange, error) {
	order := ""
	args := []any{KindBehaviorChange, OutcomeAccepted, OutcomeMerged}
	if len(dirs) > 0 {
		likes := make([]string, len(dirs))
		for i, dir := range dirs {
			likes[i] = "r.path LIKE ?"
			args = append(args, strings.TrimSuffix(dir, "/")+"/%")
		}
		order = "CASE WHEN " + strings.Join(likes, " OR ") + " THEN 0 ELSE 1 END, "
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT t.title, r.title, r.path, r.detail FROM review_items r JOIN runs u ON u.id = r.run_id JOIN tasks t ON t.id = u.task_id
		WHERE r.kind = ? AND t.outcome IN (?, ?)
		GROUP BY r.title, r.path ORDER BY `+order+`MAX(t.outcome_at) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []BehaviorChange
	for rows.Next() {
		var change BehaviorChange
		if err := rows.Scan(&change.TaskTitle, &change.Title, &change.Path, &change.Detail); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// FailureCount is how often runs or stages failed for one reason.
type FailureCount struct {
	Stage, Failure string
	Count          int
}

// Failures counts the failed and paused stages of runs started since then,
// most frequent first; a run that failed outside its stages counts under
// the stage "run".
func (s *Store) Failures(ctx context.Context, since time.Time) ([]FailureCount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stage, failure, COUNT(*) FROM (
			SELECT g.name AS stage, g.failure AS failure FROM stages g JOIN runs u ON u.id = g.run_id WHERE g.failure != '' AND u.started_at >= ?
			UNION ALL
			SELECT 'run', u.failure FROM runs u WHERE u.failure != '' AND u.started_at >= ? AND NOT EXISTS (SELECT 1 FROM stages g WHERE g.run_id = u.id AND g.failure != '')
		) GROUP BY stage, failure ORDER BY COUNT(*) DESC, stage, failure`, stamp(since), stamp(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var counts []FailureCount
	for rows.Next() {
		var count FailureCount
		if err := rows.Scan(&count.Stage, &count.Failure, &count.Count); err != nil {
			return nil, err
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}
