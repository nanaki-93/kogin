package history

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"database/sql"
)

func TestARunIsReplacedNotAddedWhenRecordedAgain(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	task := Task{ID: "change-1", ProjectID: "p", Kind: "fix", Title: "Fix 2 issues in cmd/get.go", Branch: "orca/fix", CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "change-1-1", Attempt: 1, Status: "running", StartedAt: now, UpdatedAt: now,
		Stages: []Stage{{Name: "create", Status: "running"}}}
	for range 2 {
		if err := store.Record(ctx, task, &run); err != nil {
			t.Fatal(err)
		}
	}
	run.Status, run.Failure, run.Reason = "failed", Categorize("failed", "Testing model request timed out after 15m"), "Testing model request timed out after 15m"
	run.Stages = []Stage{{Name: "create", Status: "completed", ModelCalls: 1, InputTokens: 900}, {Name: "test", Status: "failed", Failure: FailureTimeout, Reason: "timed out"}}
	run.Issues = []IssueStep{{FindingID: "f1", Path: "cmd/get.go", Title: "Password echoed", Status: "fixed", Commit: "abc"}}
	run.Review = []ReviewItem{{ProposalHash: "h", Reviewer: "correctness", Kind: KindBehaviorChange, Title: "`get` refuses pipes", Path: "cmd/get.go"}}
	if err := store.Record(ctx, task, &run); err != nil {
		t.Fatal(err)
	}
	var stages, items int
	if err := store.db.QueryRow("SELECT (SELECT COUNT(*) FROM stages), (SELECT COUNT(*) FROM review_items)").Scan(&stages, &items); err != nil || stages != 2 || items != 1 {
		t.Fatalf("stages %d, items %d, %v", stages, items, err)
	}
	failures, err := store.Failures(ctx, now.Add(-time.Hour))
	if err != nil || len(failures) != 1 || failures[0] != (FailureCount{Stage: "test", Failure: FailureTimeout, Count: 1}) {
		t.Fatalf("failures = %+v, %v", failures, err)
	}
	// A behavior change counts once its task is accepted.
	if changes, err := store.AcceptedBehaviorChanges(ctx, []string{"cmd"}, 5); err != nil || len(changes) != 0 {
		t.Fatalf("before accept: %+v, %v", changes, err)
	}
	if err := store.SetOutcome(ctx, "change-1", OutcomeAccepted, "def", now); err != nil {
		t.Fatal(err)
	}
	if changes, err := store.AcceptedBehaviorChanges(ctx, nil, 5); err != nil || len(changes) != 1 {
		t.Fatalf("accepted, any directory = %+v, %v", changes, err)
	}
	changes, err := store.AcceptedBehaviorChanges(ctx, []string{"cmd"}, 5)
	if err != nil || len(changes) != 1 || changes[0].Title != "`get` refuses pipes" || changes[0].TaskTitle != task.Title {
		t.Fatalf("accepted = %+v, %v", changes, err)
	}
}

func TestAHistoryReopensAndANewerSchemaIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Record(context.Background(), Task{ID: "t", ProjectID: "p", Kind: "fix", Title: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var tasks int
	if err := second.db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("tasks after reopening = %d, %v", tasks, err)
	}
	newer := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(newer, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(newer); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("newer schema: %v", err)
	}
}

func TestFailuresFallIntoFixedCategories(t *testing.T) {
	for _, test := range []struct{ status, reason, want string }{
		{"completed", "", ""},
		{"paused", "", FailurePlanLimit},
		{"changes_requested", "", FailureReviewRejected},
		{"failed", "Creation model request timed out after 15m0s", FailureTimeout},
		{"failed", "conversation context exceeds 256 KiB", FailureContextLimit},
		{"failed", "agent run reached its model calls limit", FailureCallBudget},
		{"failed", "the agent stopped before finishing", FailureNoFinish},
		{"failed", "Required checks fail: go test", FailureChecks},
		{"failed", "something else", FailureOther},
	} {
		if got := Categorize(test.status, test.reason); got != test.want {
			t.Fatalf("%s %q = %q, want %q", test.status, test.reason, got, test.want)
		}
	}
}

func TestAPlanIsRecordedWithItsRunAndAVersion1HistoryIsUpgraded(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A version-1 history (no plans table) upgrades in place on open.
	if _, err := store.db.Exec("DROP TABLE plans; DROP TABLE lessons; PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	task := Task{ID: "change-2", ProjectID: "p", Kind: "fix", Title: "Fix 3 issues", CreatedAt: now, UpdatedAt: now}
	run := Run{ID: "change-2-1", Attempt: 1, Status: "awaiting_plan_review", StartedAt: now, UpdatedAt: now,
		Plan: &Plan{Hash: "plan-1", Source: "investigator", Model: "m", Problem: "Sub is missing.", RootCause: "Nothing defines Sub.", Approach: "Add Sub.",
			Verdicts: []PlanVerdict{{FindingID: "f1", Verdict: "holds"}, {FindingID: "f2", Verdict: "does_not_hold", Note: "No overflow."}}, InvestigatedAt: now}}
	if err := store.Record(ctx, task, &run); err != nil {
		t.Fatal(err)
	}
	// The approved plan replaces the unapproved one for the same run.
	run.Plan.Approved = true
	if err := store.Record(ctx, task, &run); err != nil {
		t.Fatal(err)
	}
	var plans, approved int
	var verdicts string
	if err := store.db.QueryRow("SELECT COUNT(*), MAX(approved), MAX(verdicts) FROM plans").Scan(&plans, &approved, &verdicts); err != nil || plans != 1 || approved != 1 {
		t.Fatalf("plans %d approved %d, %v", plans, approved, err)
	}
	if verdicts == "" || !strings.Contains(verdicts, "does_not_hold") {
		t.Fatalf("verdicts = %q", verdicts)
	}
}

func TestALessonKeepsThePlanYouWroteAndAVersion3HistoryGainsIt(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A version-3 history has lessons without the own plan columns.
	if _, err := store.db.Exec("ALTER TABLE lessons DROP COLUMN own_plan; ALTER TABLE lessons DROP COLUMN own_match; PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO lessons (kind, task_id, title, problem, insight, concepts, paths, created_at) VALUES ('task', 't0', 'Old', 'p', 'i', '', '', '2026-11-09T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if store, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddLesson(ctx, Lesson{Kind: LessonTask, TaskID: "t1", Title: "Add Sub", Problem: "No Sub.", Insight: "Add it.", OwnPlan: "Wrong: There is no Sub.\nApproach: Add Sub beside Add.", OwnMatch: "partly", CreatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	lessons, err := store.Lessons(ctx, 10)
	if err != nil || len(lessons) != 2 {
		t.Fatalf("lessons = %+v, %v", lessons, err)
	}
	if lessons[0].OwnPlan != "Wrong: There is no Sub.\nApproach: Add Sub beside Add." || lessons[0].OwnMatch != "partly" || lessons[1].Title != "Old" || lessons[1].OwnPlan != "" {
		t.Fatalf("lessons = %+v", lessons)
	}
}
