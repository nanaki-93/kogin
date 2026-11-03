package agentstate

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func traceStore(t *testing.T) *Store {
	t.Helper()
	s, _ := testStore(t)
	runtime, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Checkpoint(t.Context(), BudgetRecord{ID: "trace-budget", Binding: Binding{"project", "revision", "policy", "config"}, Limits: runtime.Limits, Sequence: 1}, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func traceFixture(id int) TraceSpan {
	return TraceSpan{ID: fmt.Sprintf("%016x", id), Kind: "provider", Status: "running", Started: time.Now().UTC(), OperationKey: strings.Repeat("a", 64)}
}

func TestTraceJournalRejectsUnsafeFieldsAndPreservesFailedCompletion(t *testing.T) {
	s := traceStore(t)
	span := traceFixture(1)
	for _, kind := range []string{"raw-kind", "raw-key", "raw-run", "self-parent", "negative-time", "missing-start"} {
		invalid := span
		switch kind {
		case "raw-kind":
			invalid.Kind = "PRIVATE_SOURCE"
		case "raw-key":
			invalid.OperationKey = "PRIVATE_TOOL"
		case "raw-run":
			invalid.RunKey = "PRIVATE_PATH"
		case "self-parent":
			invalid.ParentID = invalid.ID
		case "negative-time":
			invalid.ElapsedNanos = -1
		case "missing-start":
			invalid.Started = time.Time{}
		}
		if recorded, err := s.BeginSpan(t.Context(), "trace-budget", invalid); err == nil || recorded {
			t.Fatalf("unsafe trace accepted: %s", kind)
		}
	}
	if recorded, err := s.BeginSpan(t.Context(), "trace-budget", span); err != nil || !recorded {
		t.Fatal("trace admission", recorded, err)
	}
	if _, err := s.BeginSpan(t.Context(), "trace-budget", span); err == nil {
		t.Fatal("duplicate trace replaced")
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("disk failure") }
	if err := s.FinishSpan(t.Context(), "trace-budget", span.ID, "failed", time.Now().UTC(), 12); !errors.Is(err, ErrPersistence) {
		t.Fatal("lost trace write failure", err)
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History(t.Context())
	if err != nil || history.Traces["trace-budget"].Spans[0].Status != "running" {
		t.Fatal("failed completion became durable", err)
	}
	if err := reopened.FinishSpan(t.Context(), "trace-budget", span.ID, "failed", time.Now().UTC(), 12); err != nil {
		t.Fatal(err)
	}
	if err := reopened.FinishSpan(t.Context(), "trace-budget", span.ID, "completed", time.Now().UTC(), 13); err == nil {
		t.Fatal("terminal trace rewritten")
	}
}

func TestTraceRetentionKeepsActiveSpansAndCumulativeBudgets(t *testing.T) {
	s := traceStore(t)
	if err := s.mutate(t.Context(), func(db *database) error {
		record := TraceRecord{Version: 1}
		for id := 1; id <= MaxTraceSpans; id++ {
			record.Spans = append(record.Spans, traceFixture(id))
		}
		db.Traces = map[string]TraceRecord{"trace-budget": record}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if recorded, err := s.BeginSpan(t.Context(), "trace-budget", traceFixture(MaxTraceSpans+1)); err != nil || recorded {
		t.Fatal("active retention slot stolen", recorded, err)
	}
	if err := s.FinishSpan(t.Context(), "trace-budget", traceFixture(1).ID, "completed", time.Now().UTC(), 100); err != nil {
		t.Fatal(err)
	}
	if recorded, err := s.BeginSpan(t.Context(), "trace-budget", traceFixture(MaxTraceSpans+2)); err != nil || !recorded {
		t.Fatal("completed slot not reclaimed", recorded, err)
	}
	db, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	record := db.Traces["trace-budget"]
	if len(record.Spans) != MaxTraceSpans || record.Dropped != 2 || record.Spans[0].ID != traceFixture(2).ID || db.Budgets["trace-budget"].Sequence != 1 {
		t.Fatal("trace retention altered active work or accounting")
	}
}

func TestTraceJournalRejectsCyclesButAllowsEvictedParents(t *testing.T) {
	a, b := traceFixture(1), traceFixture(2)
	a.ParentID, b.ParentID = b.ID, a.ID
	if validTraceParents([]TraceSpan{a, b}) {
		t.Fatal("trace parent cycle accepted")
	}
	if !validTraceParents([]TraceSpan{a}) {
		t.Fatal("retention gap invalidated surviving trace")
	}
}

func TestTraceTransitionsCommitAtomicallyWithRunAndBudget(t *testing.T) {
	s := traceStore(t)
	key := Key{"app", "user", "session"}
	now := time.Now().UTC()
	run := Run{ID: "traced-run", Session: key, BudgetID: "trace-budget", RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: "completion-v1", Status: "running", Started: now, Updated: now}
	span := traceFixture(1)
	span.Kind = "invocation"
	write := s.write
	fail := func(string, []byte, os.FileMode, func() error) error { return errors.New("disk failure") }
	s.write = fail
	if recorded, err := s.BeginRun(t.Context(), run, &span); !errors.Is(err, ErrPersistence) || recorded {
		t.Fatal("failed admission became durable", recorded, err)
	}
	assertTraceTransaction(t, s, "", 0, 1)
	s.write = write
	if recorded, err := s.BeginRun(t.Context(), run, &span); err != nil || !recorded {
		t.Fatal(recorded, err)
	}
	assertTraceTransaction(t, s, "running", 1, 1)
	db, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	budget := db.Budgets[run.BudgetID]
	budget.Sequence++
	budget.Usage.ModelCalls = 1
	provider := traceFixture(2)
	provider.ParentID = span.ID
	s.write = fail
	if recorded, err := s.Checkpoint(t.Context(), budget, &provider); !errors.Is(err, ErrPersistence) || recorded {
		t.Fatal("failed budget admission became durable", recorded, err)
	}
	assertTraceTransaction(t, s, "running", 1, 1)
	s.write = write
	if recorded, err := s.Checkpoint(t.Context(), budget, &provider); err != nil || !recorded {
		t.Fatal(recorded, err)
	}
	provider.Status, provider.Ended, provider.ElapsedNanos = "failed", time.Now().UTC(), 12
	budget.Sequence++
	budget.Usage.InputTokens = 10
	s.write = fail
	if recorded, err := s.Checkpoint(t.Context(), budget, &provider); !errors.Is(err, ErrPersistence) || recorded {
		t.Fatal("failed settlement became durable", recorded, err)
	}
	assertTraceTransaction(t, s, "running", 2, 2)
	span.Status, span.Ended, span.ElapsedNanos = "failed", time.Now().UTC(), 13
	if err := s.FinishRun(t.Context(), run.ID, "failed", &span); !errors.Is(err, ErrPersistence) {
		t.Fatal("failed run completion became durable", err)
	}
	assertTraceTransaction(t, s, "running", 2, 2)
	s.write = write
	if _, err := s.Checkpoint(t.Context(), budget, &provider); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(t.Context(), run.ID, "failed", &span); err != nil {
		t.Fatal(err)
	}
	assertTraceTransaction(t, s, "failed", 2, 3)
}

func assertTraceTransaction(t *testing.T, s *Store, status string, spans int, sequence uint64) {
	t.Helper()
	db, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Runs["traced-run"].Status != status || len(db.Traces["trace-budget"].Spans) != spans || db.Budgets["trace-budget"].Sequence != sequence {
		t.Fatal("transaction exposed a partial transition")
	}
	for _, span := range db.Traces["trace-budget"].Spans {
		if span.Status != status {
			t.Fatalf("run and trace status diverged: %s %s", status, span.Status)
		}
	}
}
