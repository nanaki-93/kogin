package launcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskNoticesTellARunThatEndedOnce(t *testing.T) {
	notices := NewNotices()
	review := Event{Kind: "workflow", SessionID: "task-1", WorkflowID: "run-1", Title: "Fix login", Status: "awaiting_human_review", WorktreePath: "/work/fix login"}
	if _, ok := notices.Notice(Event{Kind: "workflow", SessionID: "task-1", WorkflowID: "run-1", Stage: "review", Status: "running"}); ok {
		t.Fatal("a stage in progress was told")
	}
	notice, ok := notices.Notice(review)
	if !ok || notice.Title != "Ready for your review" || notice.Body != "Agents finished “Fix login”. It needs your review." || notice.Route != "/tasks/task-1?wt=%2Fwork%2Ffix+login" {
		t.Fatalf("review notice = %+v %v", notice, ok)
	}
	if _, ok := notices.Notice(review); ok {
		t.Fatal("the same outcome was told twice")
	}
	notice, ok = notices.Notice(Event{Kind: "workflow", SessionID: "task-2", WorkflowID: "run-2", Status: "failed"})
	if !ok || notice.Body != "The agent run on “a task” failed." || notice.Route != "/tasks/task-2" {
		t.Fatalf("failure notice = %+v %v", notice, ok)
	}
	for _, status := range []string{"applied", "deleted", "running", "reanalyzed"} {
		if _, ok := notices.Notice(Event{Kind: "workflow", SessionID: "task-3", Status: status}); ok {
			t.Fatalf("%s was told", status)
		}
	}
}

func TestAnalysisNoticesTellOnlyRunsSeenRunning(t *testing.T) {
	notices := NewNotices()
	if _, ok := notices.Notice(Event{Kind: "analysis", WorkflowID: "old", Status: "completed"}); ok {
		t.Fatal("a run that ended before the stream began was told")
	}
	steps := []struct {
		status, title string
	}{
		{"queued", ""}, {"running", ""}, {"paused", "Analysis paused"}, {"paused", ""},
		{"running", ""}, {"partial", "Analysis finished with failures"}, {"partial", ""},
		{"running", ""}, {"canceled", ""}, {"canceled", ""},
	}
	for i, step := range steps {
		notice, ok := notices.Notice(Event{Kind: "analysis", WorkflowID: "run-1", Status: step.status})
		if ok != (step.title != "") || notice.Title != step.title {
			t.Fatalf("step %d %s: %+v %v", i, step.status, notice, ok)
		}
		if ok && (notice.Route != "/analysis" || notice.ID != "analysis-run-1-"+step.status) {
			t.Fatalf("step %d: %+v", i, notice)
		}
	}
	if _, ok := notices.Notice(Event{Kind: "analysis", Status: "running"}); ok {
		t.Fatal("an analysis event without a run was told")
	}
}

// eventStream serves the events route the way the daemon does: a ready frame,
// then update frames, heartbeats and malformed data; each connection gets the
// next batch and then ends.
func eventStream(t *testing.T, batches ...[]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/current/events" || r.Header.Get("Accept") != "text/event-stream" {
			http.NotFound(w, r)
			return
		}
		n := int(connections.Add(1)) - 1
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: ready\ndata: {\"status\":\"connected\"}\n\n: heartbeat\n\n")
		if n < len(batches) {
			for _, data := range batches[n] {
				fmt.Fprintf(w, "event: update\ndata: %s\n\n", data)
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, &connections
}

func TestWatchReadsUpdatesAndReconnects(t *testing.T) {
	server, connections := eventStream(t,
		[]string{`{"kind":"workflow","session_id":"task-1","status":"awaiting_human_review","title":"Fix"}`, `not json`},
		[]string{`{"kind":"analysis","workflow_id":"run-1","status":"running"}`},
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	var seen []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewClient(server.URL).Watch(ctx, func(event Event) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, event)
			if len(seen) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not end with its context")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].SessionID != "task-1" || seen[0].Title != "Fix" || seen[1].WorkflowID != "run-1" || seen[1].Status != "running" {
		t.Fatalf("events = %+v", seen)
	}
	if connections.Load() != 2 {
		t.Fatalf("connections = %d, want a reconnect after the first stream ended", connections.Load())
	}
}

func TestWatchStopsWhileTheDaemonIsAway(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	NewClient("http://127.0.0.1:1").Watch(ctx, func(Event) { t.Error("an event from no daemon") })
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Watch outlived its context by %s", elapsed)
	}
	if !strings.Contains(ctx.Err().Error(), "deadline") {
		t.Fatalf("context = %v", ctx.Err())
	}
}
