package agentstate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func TestJournalDropsTheOldestFinishedStandaloneRunsWithTheirSessions(t *testing.T) {
	grace := pruneGrace
	pruneGrace = 0
	t.Cleanup(func() { pruneGrace = grace })
	s, _ := testStore(t)
	limits, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	budget := BudgetRecord{ID: "budget", Binding: Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}, Limits: limits.Limits, Sequence: 1}
	if _, err := s.Checkpoint(t.Context(), budget, nil); err != nil {
		t.Fatal(err)
	}
	begin := func(id, parent string, owned bool) Key {
		t.Helper()
		key := Key{"app", "user", "s-" + id}
		if _, err := s.CreateSession(t.Context(), key, nil); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		run := Run{ID: id, ParentID: parent, Session: key, BudgetID: budget.ID, RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: "completion-v1", Status: "running", Started: now, Updated: now}
		if owned {
			run.ChangeSessionID, run.WorkflowID = "change", "workflow"
		}
		if _, err := s.BeginRun(t.Context(), run, nil); err != nil {
			t.Fatal(err)
		}
		return key
	}
	finish := func(id string) {
		t.Helper()
		if err := s.FinishRun(t.Context(), id, "completed", nil); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest records: a running invocation and the finished call under
	// it, a task workflow's, and a finished parent whose child still runs.
	// Only the finished call may go.
	running := begin("running", "", false)
	done := begin("call", "running", false)
	finish("call")
	task := begin("task", "", true)
	finish("task")
	parent := begin("parent", "", false)
	child := begin("child", "parent", false)
	for i := range maxRetainedSessions {
		begin(fmt.Sprintf("done-%03d", i), "", false)
		finish(fmt.Sprintf("done-%03d", i))
	}
	db, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Sessions) > maxRetainedSessions+1 {
		t.Fatalf("journal kept %d sessions", len(db.Sessions))
	}
	for _, key := range []Key{running, task, parent, child} {
		if _, ok := db.Sessions[key.key()]; !ok {
			t.Errorf("pruned %s", key.ID)
		}
	}
	if _, ok := db.Sessions[done.key()]; ok {
		t.Error("kept the finished call of a running invocation")
	}
	if _, ok := db.Runs["done-000"]; ok {
		t.Error("kept the oldest finished run")
	}
	if _, ok := db.Sessions[(Key{"app", "user", "s-done-000"}).key()]; ok {
		t.Error("kept the oldest finished run's session")
	}
	last := fmt.Sprintf("done-%03d", maxRetainedSessions-1)
	if _, ok := db.Runs[last]; !ok {
		t.Error("pruned the newest finished run")
	}
	if db.Budgets[budget.ID].ID != budget.ID {
		t.Error("pruned the budget usage reads")
	}
	for i := range maxRetainedSessions {
		begin(fmt.Sprintf("late-%03d", i), "", false)
		finish(fmt.Sprintf("late-%03d", i))
	}
	pruneGrace = time.Hour
	db, err = s.load()
	if err != nil {
		t.Fatal(err)
	}
	sessions := len(db.Sessions)
	if pruneFinishedRuns(db); len(db.Sessions) != sessions {
		t.Fatal("pruned runs that finished within the grace period")
	}
}

// Parallel analysis lanes write the journal at the same time; a writer that
// waits behind others longer than the file lock's timeout must still write.
func TestJournalWritersOfOneProcessWaitTheirTurn(t *testing.T) {
	s, key := testStore(t)
	write := s.write
	s.write = func(path string, data []byte, mode os.FileMode, check func() error) error {
		time.Sleep(450 * time.Millisecond)
		return write(path, data, mode, check)
	}
	const writers = 13
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			if _, err := s.AppendEvent(t.Context(), key, Event{ID: fmt.Sprintf("event-%d", i), Data: json.RawMessage(`{}`)}, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	record, err := s.Session(t.Context(), key)
	if err != nil || len(record.Events) != writers {
		t.Fatalf("lost appends: %d %v", len(record.Events), err)
	}
}
