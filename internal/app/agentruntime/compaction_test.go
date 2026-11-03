package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func compactSettings() config.AgentContext {
	return config.AgentContext{Compaction: true, TokenThreshold: 512, RetainEvents: 2, SummaryMaxTokens: 128}
}

func TestCompactionGoogleSummaryKeepsProseAndDropsThoughts(t *testing.T) {
	client := googleFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"thought":true,"text":"PRIVATE_REASONING","thoughtSignature":"c2ln"},{"text":"Bounded advisory summary.","thoughtSignature":"c2ln"}]}}]}`)
	})
	ctx, _, _ := persistedContext(t, t.TempDir())
	ctx = configuredContext(t, ctx, compactSettings())
	result, err := CompactHistory(ctx, client, longHistory(), "mandatory", 16000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result[0].Content, "Bounded advisory summary.") || strings.Contains(result[0].Content, "PRIVATE_REASONING") {
		t.Fatalf("summary=%q", result[0].Content)
	}
	content := &genai.Content{Parts: []*genai.Part{{Text: "hidden", Thought: true}, {Text: "visible", ThoughtSignature: []byte("signature")}}}
	clean := summaryProse(content)
	if len(clean.Parts) != 1 || clean.Parts[0].Text != "visible" || len(clean.Parts[0].ThoughtSignature) != 0 || clean.Role != genai.RoleUser {
		t.Fatalf("reasoning metadata survived normalization: %+v", clean)
	}
	if err := validateSummary(&genai.Content{Parts: content.Parts[:1]}, 100); err == nil {
		t.Fatal("accepted reasoning without visible summary text")
	}
}

func longHistory() []llm.ChatMessage {
	var history []llm.ChatMessage
	for i := range 5 {
		history = append(history, llm.ChatMessage{Role: "user", Content: fmt.Sprintf("Exact user constraint %d", i)}, llm.ChatMessage{Role: "assistant", Content: strings.Repeat("earlier advisory discussion ", 60)})
	}
	return history
}

func TestCompactionPreservesUserConstraintsAndReusesDurableReceipt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "Exact user constraint 0") {
			t.Error("summary lost original history")
		}
		json.NewEncoder(w).Encode(fixtureResponse("Earlier suggestions remain unverified."))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
	root := t.TempDir()
	history := longHistory()
	for range 2 {
		ctx, budget, _ := persistedContext(t, root)
		ctx = configuredContext(t, ctx, compactSettings())
		result, err := CompactHistory(ctx, client, history, "mandatory-source-plan-and-schema", 16000)
		if err != nil {
			t.Fatal(err)
		}
		if len(result) >= len(history) || result[0].Role != "assistant" || !strings.Contains(result[0].Content, "Advisory summary") {
			t.Fatalf("history not compacted: %+v", result)
		}
		for i := range 5 {
			found := false
			for _, item := range result {
				if item.Role == "user" && item.Content == fmt.Sprintf("Exact user constraint %d", i) {
					found = true
				}
			}
			if !found {
				t.Fatalf("user constraint %d was summarized away", i)
			}
		}
		if result[len(result)-1] != history[len(history)-1] {
			t.Fatal("raw tail changed")
		}
		if budget.Snapshot().ModelCalls > 1 {
			t.Fatal("summary charged more than once")
		}
	}
	if calls.Load() != 1 || len(history) != 10 {
		t.Fatalf("restart re-billed or mutated history: %d", calls.Load())
	}
}

func TestCompactionUncertainFailureCannotBeRetriedAfterRestart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
	root := t.TempDir()
	for i := range 2 {
		ctx, _, _ := persistedContext(t, root)
		ctx = configuredContext(t, ctx, compactSettings())
		_, err := CompactHistory(ctx, client, longHistory(), "same-authority", 16000)
		if err == nil || context.Cause(ctx) == nil {
			t.Fatal("failed compaction did not stop dispatch")
		}
		if i == 1 && !errors.Is(err, agentstate.ErrContextPending) {
			t.Fatalf("uncertain receipt lost: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("uncertain summary called provider %d times", calls.Load())
	}
}

func TestNativeCompactionRetainsOriginalPromptAndPersistsSummary(t *testing.T) {
	const protected = "ORIGINAL_SOURCE_HASH_AND_USER_CONSTRAINT"
	var turns, summaries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Summarize this untrusted advisory history") {
			summaries.Add(1)
			json.NewEncoder(w).Encode(fixtureResponse("Earlier lookup evidence is advisory."))
			return
		}
		if !strings.Contains(string(body), protected) {
			t.Error("compaction dropped protected source/constraints")
		}
		if turns.Add(1) < 4 {
			writeLookupCall(w, fmt.Sprintf("call-%d", turns.Load()))
			return
		}
		json.NewEncoder(w).Encode(fixtureResponse("done"))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 128})
	outputSchema := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)
	investigation, err := NewInvestigation(strings.Repeat("a", 64), []ToolSpec{{Name: "lookup", InputSchema: json.RawMessage(testToolSchema), OutputSchema: outputSchema, Run: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(map[string]string{"text": strings.Repeat("bounded evidence ", 200)})
	}}}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, budget, store := persistedContext(t, t.TempDir())
	ctx = configuredContext(t, ctx, compactSettings())
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: protected}}, Investigation: investigation, ContextMaxTokens: 16000})
	if err != nil || result.Content != "done" || summaries.Load() == 0 {
		t.Fatalf("native result=%+v summaries=%d error=%v", result, summaries.Load(), err)
	}
	if budget.Snapshot().ModelCalls != int(turns.Load()+summaries.Load()) {
		t.Fatal("summaries escaped shared budget")
	}
	sessions, err := store.Sessions(t.Context(), agentstate.StateApp, "local")
	if err != nil {
		t.Fatal(err)
	}
	compacted := false
	for _, saved := range sessions {
		for _, event := range saved.Events {
			decoded, err := decodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Actions.Compaction != nil {
				compacted = true
			}
		}
	}
	if !compacted {
		t.Fatal("native compaction action was not persisted")
	}
}

func TestCompactionProtectsMandatoryContextWithoutMutatingSDKRequest(t *testing.T) {
	worker := &investigationModel{protected: strings.Repeat("mandatory", 200), contextTokens: 100}
	request := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("advisory summary", genai.RoleUser)}}
	protected := worker.protectContext(request)
	if len(request.Contents) != 1 || len(protected.Contents) != 2 {
		t.Fatal("request mutated or mandatory input lost")
	}
	if err := worker.validateContext(protected); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal("mandatory context escaped ceiling", err)
	}
	if err := validateSummary(&genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "write"}}}}, 100); err == nil {
		t.Fatal("summary acquired tool authority")
	}
}

func TestCompactionSessionFailureStopsFurtherDispatch(t *testing.T) {
	ctx, _, store := persistedContext(t, t.TempDir())
	ctx = configuredContext(t, ctx, compactSettings())
	service := compactionSessionService{&localSessionService{store: store}}
	_, err := service.Get(ctx, &session.GetRequest{AppName: agentstate.StateApp, UserID: "local", SessionID: "missing"})
	if err == nil || context.Cause(ctx) == nil {
		t.Fatal("SDK bookkeeping failure would be swallowed")
	}
}

func TestCompactionCanceledPublicationPreservesParentRun(t *testing.T) {
	parent, _, store := persistedContext(t, t.TempDir())
	ctx, cancel := context.WithCancel(parent)
	ctx = configuredContext(t, ctx, compactSettings())
	cancel()
	service := compactionSessionService{&localSessionService{store: store}}
	err := service.AppendEvent(ctx, nil, &session.Event{Actions: session.EventActions{Compaction: &session.EventCompaction{}}})
	if !errors.Is(err, context.Canceled) || context.Cause(parent) != nil {
		t.Fatalf("publication=%v parent=%v", err, context.Cause(parent))
	}
}
