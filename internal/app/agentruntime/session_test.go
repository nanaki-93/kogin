package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
	"google.golang.org/genai"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func TestLocalSessionConformance(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{SupportsUserProvidedSessionID: true}, func(t *testing.T) session.Service {
		store, err := agentstate.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return &localSessionService{store: store}
	})
}

func TestLocalSessionCancellationRetainsItsMeaning(t *testing.T) {
	store, err := agentstate.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &localSessionService{store: store}
	_, err = s.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "user", SessionID: "canceled"})
	if !errors.Is(err, context.Canceled) || errors.Is(err, agentstate.ErrPersistence) {
		t.Fatalf("canceled session was labeled a storage failure: %v", err)
	}
	deadline := fmt.Errorf("%w: lock: %w", agentstate.ErrPersistence, context.DeadlineExceeded)
	if err := sessionStorageError(deadline); !errors.Is(err, agentstate.ErrPersistence) {
		t.Fatal("genuine store deadline was hidden")
	}
}

func TestLocalSessionConcurrentAppendAndCopyIsolation(t *testing.T) {
	root := t.TempDir()
	store, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &localSessionService{store: store}
	created, err := s.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "user", State: map[string]any{"nested": map[string]any{"value": "original"}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			event := &session.Event{ID: fmt.Sprintf("e%d", i), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("original", "model")}}
			if err := s.AppendEvent(t.Context(), created.Session, event); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	created.Session.Events().At(0).Content.Parts[0].Text = "mutated"
	state, err := created.Session.State().Get("nested")
	if err != nil {
		t.Fatal(err)
	}
	state.(map[string]any)["value"] = "mutated"
	reopened, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	read, err := (&localSessionService{store: reopened}).Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "user", SessionID: created.Session.ID()})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := read.Session.State().Get("nested")
	if read.Session.Events().Len() != 20 || read.Session.Events().At(0).Content.Parts[0].Text != "original" || value.(map[string]any)["value"] != "original" {
		t.Fatal("durable snapshot lost events or leaked mutable values")
	}
}

func TestLocalSessionPreservesExactToolNumbers(t *testing.T) {
	store, err := agentstate.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &localSessionService{store: store}
	created, err := s.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	event := &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call", Name: "lookup", Args: map[string]any{"id": json.Number("9007199254740993")}}}}}}}
	if err := s.AppendEvent(t.Context(), created.Session, event); err != nil {
		t.Fatal(err)
	}
	read, err := s.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "user", SessionID: created.Session.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if value := read.Session.Events().At(0).Content.Parts[0].FunctionCall.Args["id"]; value != json.Number("9007199254740993") {
		t.Fatalf("tool identity rounded during replay: %#v", value)
	}
}

func persistedContext(t *testing.T, root string) (context.Context, *Budget, *agentstate.Store) {
	t.Helper()
	ctx, budget := budgetContext(t, nil)
	store, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = WithPersistence(ctx, store, agentstate.Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, budget, store
}

func TestDurableCompletionAndBudgetSurviveReopen(t *testing.T) {
	root := t.TempDir()
	ctx, budget, store := persistedContext(t, root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(fixtureResponse("done")) }))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	if _, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "private input"}}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.Sessions(t.Context(), "kasuri", "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Events) != 1 || budget.Snapshot().ModelCalls != 1 {
		t.Fatalf("sessions=%d usage=%+v", len(list), budget.Snapshot())
	}
	paths, err := filepath.Glob(filepath.Join(root, ".kasuri", "agents", "*", "journal.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing durable budget journal", err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Budgets map[string]agentstate.BudgetRecord
		Runs    map[string]agentstate.Run
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if len(journal.Budgets) != 1 || len(journal.Runs) != 1 {
		t.Fatal("missing invocation or shared budget")
	}
	for _, record := range journal.Budgets {
		if record.Usage.ModelCalls != 1 || record.Usage.OutputTokens != 7 || record.Usage.ActiveCalls != 0 || record.Sequence != 3 {
			t.Fatalf("durable usage=%+v", record)
		}
	}
	for _, record := range journal.Runs {
		if record.Status != "completed" || record.RequestHash == "" || record.SchemaHash == "" || record.ProviderBinding == "" {
			t.Fatalf("run=%+v", record)
		}
	}
	var messages []llm.ChatMessage
	if err := json.Unmarshal(list[0].State["mini_orca.request"], &messages); err != nil || len(messages) != 1 || messages[0].Content != "private input" {
		t.Fatalf("input history not retained: %v", err)
	}
	if err := store.DeleteSession(t.Context(), list[0].Key); err != nil {
		t.Fatalf("completed invocation remained active: %v", err)
	}
}

func TestDurableReservationFailurePreventsProviderDispatch(t *testing.T) {
	root := t.TempDir()
	ctx, budget, _ := persistedContext(t, root)
	if err := os.Rename(filepath.Join(root, ".kasuri", "agents"), filepath.Join(root, "saved-agents")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kasuri", "agents"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(fixtureResponse("done"))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	_, err := client.Chat(ctx, []llm.ChatMessage{{Role: "user", Content: "private input"}})
	if !errors.Is(err, agentstate.ErrPersistence) || calls.Load() != 0 || budget.Snapshot().ActiveCalls != 0 {
		t.Fatalf("error=%v calls=%d usage=%+v", err, calls.Load(), budget.Snapshot())
	}
}

func TestDurableEventWriteFailureDoesNotPublishResult(t *testing.T) {
	root := t.TempDir()
	ctx, _, _ := persistedContext(t, root)
	client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
		if err := os.Rename(filepath.Join(root, ".kasuri", "agents"), filepath.Join(root, "saved-agents")); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(root, ".kasuri", "agents"), []byte("blocked"), 0600); err != nil {
			return nil, err
		}
		return fixtureResponse("must not publish"), nil
	})
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
	if result != (Result{}) || !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
