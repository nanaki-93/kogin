package agentruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/session"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func TestProgressObserverPreservesNativeAncestryAndStreaming(t *testing.T) {
	client := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, partialNativeStream+finalNativeStream)
	})
	ctx, _, store := persistedContext(t, t.TempDir())
	var mu sync.Mutex
	active := map[string]bool{}
	var text strings.Builder
	changes := 0
	ctx = WithObserver(ctx, &Observer{Started: func(run agentstate.Run, _ context.CancelFunc) { mu.Lock(); defer mu.Unlock(); active[run.ID] = true }, Ended: func(id string) { mu.Lock(); defer mu.Unlock(); delete(active, id) }, Changed: func() { mu.Lock(); defer mu.Unlock(); changes++ }, Text: func(id, delta string) {
		mu.Lock()
		defer mu.Unlock()
		if !active[id] {
			t.Error("text without owned invocation")
		}
		text.WriteString(delta)
	}})
	_, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, node ChangeNodeInput) (ChangeNodeResult, error) {
		if node.Node == ChangeCreate {
			if _, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "PRIVATE_PROMPT"}}, Stream: true}); err != nil {
				return ChangeNodeResult{}, err
			}
		}
		return changeGraphHost(ctx, node)
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(active) != 0 || text.String() != "partial" || changes < 4 {
		t.Fatalf("observer lifecycle: active=%v text=%q changes=%d", active, text.String(), changes)
	}
	mu.Unlock()
	history, err := store.History(t.Context())
	if err != nil || len(history.Runs) != 2 {
		t.Fatalf("native hierarchy: %+v %v", history.Runs, err)
	}
	var parent, child string
	for _, run := range history.Runs {
		if run.SchemaVersion == "change-graph-v1" {
			parent = run.ID
		} else {
			child = run.ParentID
		}
	}
	if parent == "" || child != parent {
		t.Fatalf("lost parent: %q %q", parent, child)
	}
	data, err := json.Marshal(history.Events)
	if err != nil || strings.Contains(string(data), "PRIVATE_PROMPT") || strings.Contains(string(data), "partial") {
		t.Fatal("source-bearing progress", err)
	}
	found := false
	for _, event := range history.Events {
		if len(event.Evidence) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("completion evidence missing from progress")
	}
}

func TestProgressProjectionIgnoresUntrustedNamesAndPayloads(t *testing.T) {
	event := &session.Event{Author: "PRIVATE_SOURCE", CustomMetadata: map[string]any{"mini_orca.change_node": "PRIVATE_SOURCE", "mini_orca.review_node": "unknown"}, Output: "PRIVATE_OUTPUT"}
	value := projectEvent(event, agentstate.Key{App: "app", User: "user", ID: "id"})
	if value.NodeID != "agent" || value.State != "completed" || len(value.Evidence) != 0 {
		t.Fatalf("untrusted projection: %+v", value)
	}
}
