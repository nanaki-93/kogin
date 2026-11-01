package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func TestLocalTraceCorrelatesAttemptsAndNeverExportsAmbientData(t *testing.T) {
	var exported atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { exported.Add(1) }))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "private=PRIVATE_RESOURCE")
	var calls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "PRIVATE_ERROR", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(fixtureResponse("PRIVATE_OUTPUT"))
	}))
	defer provider.Close()
	root := t.TempDir()
	ctx, _, _ := persistedContext(t, root)
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: provider.URL, Model: "PRIVATE_MODEL", APIKey: "PRIVATE_KEY", MaxTokens: 100})
	for i := range 2 {
		attempt, trace, err := StartTrace(ctx, "retry", "PRIVATE_OPERATION")
		if err != nil {
			t.Fatal(err)
		}
		_, err = Complete(attempt, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "PRIVATE_SOURCE"}}})
		err = trace.Finish(attempt, err)
		if (err != nil) != (i == 0) {
			t.Fatal("attempt outcome", err)
		}
	}
	store, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History(t.Context())
	if err != nil || len(history.Traces) != 1 || len(history.Runs) != 2 {
		t.Fatal("shared trace missing", err)
	}
	record := history.Traces[history.Runs[0].BudgetID]
	if len(record.Spans) != 6 {
		t.Fatalf("spans=%+v", record.Spans)
	}
	parents := map[string]agentstate.TraceSpan{}
	failed := 0
	for _, span := range record.Spans {
		parents[span.ID] = span
		if span.Status == "failed" {
			failed++
		}
		if span.Ended.IsZero() || span.ElapsedNanos < 0 {
			t.Fatal("missing completed duration")
		}
	}
	for _, span := range record.Spans {
		if span.Kind == "provider" && (parents[span.ParentID].Kind != "invocation" || span.OperationKey != ProviderUsageKey("openai", "PRIVATE_MODEL")) {
			t.Fatal("provider attribution lost", span)
		}
		if span.Kind == "invocation" && parents[span.ParentID].Kind != "retry" {
			t.Fatal("retry ancestry lost", span)
		}
	}
	data, err := json.Marshal(history.Traces)
	if err != nil || strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), provider.URL) || failed != 3 || exported.Load() != 0 {
		t.Fatal("unsafe or incomplete local trace", failed, exported.Load(), err)
	}
}

type traceNativeFixture struct {
	agent.ContextMock
	base context.Context
}

func (c *traceNativeFixture) Value(key any) any           { return c.base.Value(key) }
func (c *traceNativeFixture) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c *traceNativeFixture) Done() <-chan struct{}       { return c.base.Done() }
func (c *traceNativeFixture) Err() error                  { return c.base.Err() }

func TestLocalTracePreservesNativeContextCancellationAndSingleCompletion(t *testing.T) {
	ctx, _, store := persistedContext(t, t.TempDir())
	owned, cancel := context.WithCancel(ctx)
	native := &traceNativeFixture{base: owned}
	child, trace, err := StartTrace(native, "tool", "PRIVATE_TOOL")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := child.(agent.Context); !ok {
		t.Fatal("native agent context erased")
	}
	if child.Value(traceParentKey{}) != trace.span.ID {
		t.Fatal("trace parent not inherited")
	}
	cancel()
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := trace.Finish(child, context.Cause(child)); !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	history, err := store.History(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range history.Traces {
		if len(record.Spans) != 1 || record.Spans[0].Status != "canceled" {
			t.Fatal("canceled trace lost", record)
		}
	}
}

func TestLocalTraceCapturesGraphChecksAndRejectsFailedJournalWrites(t *testing.T) {
	root := t.TempDir()
	ctx, _, store := persistedContext(t, root)
	if _, err := RunChangeGraph(ctx, changeGraphFixture(), changeGraphHost); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	checks, nodes := 0, 0
	for _, record := range history.Traces {
		for _, span := range record.Spans {
			if span.Kind == "check" {
				checks++
			}
			if span.Kind == "node" {
				nodes++
			}
			if span.Status != "completed" || span.Ended.IsZero() {
				t.Fatal("host span failed to settle", span)
			}
		}
	}
	if checks != 1 || nodes != 4 {
		t.Fatal("missing typed stage traces", checks, nodes)
	}
	ctx, trace, err := StartTrace(ctx, "summary", "compaction")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(root, ".mini-orca", "agents", "*", "journal.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing journal", err)
	}
	if err := os.WriteFile(paths[0], []byte(`{"broken":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := trace.Finish(ctx, nil); !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) {
		t.Fatal("trace persistence failure became success", err)
	}
}
