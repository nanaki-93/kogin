package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/history"
)

func TestEveryRunOfATaskIsKeptInTheProjectsHistory(t *testing.T) {
	s, root, session := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return behaviorReview }})
	if _, err := s.StartChangeWorkflow(t.Context(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	reviewed := finishedWorkflow(t, s, session.ID)
	db, err := sql.Open("sqlite", filepath.Join(root, ".kogin", history.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var runs, stages, behavior int
	var status string
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM runs), (SELECT COUNT(*) FROM stages WHERE status = 'completed'), (SELECT COUNT(*) FROM review_items WHERE kind = 'behavior_change'), (SELECT status FROM runs)`).Scan(&runs, &stages, &behavior, &status); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || stages != 3 || behavior == 0 || status != reviewed.Workflow.Status {
		t.Fatalf("history: runs %d, completed stages %d, behavior changes %d, status %q", runs, stages, behavior, status)
	}
	// Deleting the task keeps its history, with how it ended.
	if err := s.DeleteChange(t.Context(), reviewed.ID, changeIdentity(reviewed)); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.QueryRow(`SELECT outcome FROM tasks WHERE id = ?`, reviewed.ID).Scan(&outcome); err != nil || outcome != history.OutcomeDeleted {
		t.Fatalf("outcome = %q, %v", outcome, err)
	}
	// Accepted behavior changes reach the next tasks' agents.
	store, err := history.Open(historyDir(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOutcome(context.Background(), reviewed.ID, history.OutcomeMerged, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if brief := acceptedBehaviorBrief(root, []string{"new.go"}); !strings.Contains(brief, "- Helper returns two (new.go): Before: no Helper.") {
		t.Fatalf("brief = %q", brief)
	}
}

func TestAWorktreeTaskKeepsItsHistoryInTheMainCheckout(t *testing.T) {
	if got, want := historyDir(filepath.Join("/p", ".worktrees", "fix-1")), filepath.Join("/p", ".kogin"); got != want {
		t.Fatalf("history dir = %s, want %s", got, want)
	}
	if got, want := historyDir("/p"), filepath.Join("/p", ".kogin"); got != want {
		t.Fatalf("history dir = %s, want %s", got, want)
	}
}

func TestAnUnreadableHistoryIsSetAsideAndStartedAgain(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, ".kogin/"+history.FileName, "not a database")
	recordHistory(root, &ChangeSession{ID: "change-1", ProjectID: "p", Kind: "fix", Title: "Fix it"})
	matches, _ := filepath.Glob(filepath.Join(root, ".kogin", "kogin.unreadable-*.db"))
	db, err := sql.Open("sqlite", filepath.Join(root, ".kogin", history.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tasks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil || tasks != 1 || len(matches) != 1 {
		t.Fatalf("tasks %d (%v), set aside %v", tasks, err, matches)
	}
}
