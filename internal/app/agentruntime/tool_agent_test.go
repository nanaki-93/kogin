package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

func writeLookupCall(w io.Writer, id string) {
	fmt.Fprintf(w, `{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":"lookup","arguments":"{\"id\":7}"}}]}}]}`, id)
}

func TestInvestigationRoundTripPersistsToolEventsAndScope(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"lookup"`) {
			t.Error("tool declaration missing")
		}
		if calls == 1 {
			writeLookupCall(w, "lookup-1")
			return
		}
		if !strings.Contains(string(body), `"tool_call_id":"lookup-1"`) || !strings.Contains(string(body), `"role":"tool"`) {
			t.Errorf("tool reply identity lost: %s", body)
		}
		json.NewEncoder(w).Encode(fixtureResponse("done"))
	}))
	defer server.Close()
	ctx, budget, store := persistedContext(t, t.TempDir())
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "investigate"}}, Investigation: fixtureInvestigation(t, nil, nil)})
	if err != nil || result.Content != "done" || calls != 2 || budget.Snapshot().ToolCalls != 1 || budget.Snapshot().ModelCalls != 2 {
		t.Fatalf("result=%+v error=%v calls=%d budget=%+v", result, err, calls, budget.Snapshot())
	}
	list, err := store.Sessions(t.Context(), "kasuri", "local")
	if err != nil || len(list) != 1 || len(list[0].Events) != 4 {
		t.Fatalf("history=%+v error=%v", list, err)
	}
	if !strings.Contains(string(list[0].State["mini_orca.tools"]), "lookup") || string(list[0].State["mini_orca.scope"]) != `"`+strings.Repeat("a", 64)+`"` {
		t.Fatal("missing tool/scope identity")
	}
}

func TestInvestigationCapsTurnsAcrossRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		writeLookupCall(w, fmt.Sprintf("lookup-%d", calls))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	request := Request{Messages: []llm.ChatMessage{{Role: "user", Content: "investigate"}}, Investigation: fixtureInvestigation(t, nil, nil)}
	for range 2 {
		ctx, _ := budgetContext(t, nil)
		if _, err := Complete(ctx, client, request); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatal(err)
		}
	}
	if calls != 8 {
		t.Fatalf("turn limit reset: %d", calls)
	}
}

func TestInvestigationRejectsOversizedContextBeforeProviderDispatch(t *testing.T) {
	client := googleFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("oversized context dispatched") })
	ctx, budget := budgetContext(t, nil)
	request := Request{Messages: []llm.ChatMessage{{Role: "user", Content: strings.Repeat("source", 1000)}}, Investigation: fixtureInvestigation(t, nil, nil), ContextMaxTokens: 100}
	if _, err := Complete(ctx, client, request); !errors.Is(err, ErrBudgetExceeded) || budget.Snapshot().ModelCalls != 0 {
		t.Fatalf("oversized context accepted: %v", err)
	}
}

func TestInvestigationNativeGoogleToolRoundTrip(t *testing.T) {
	calls := 0
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			io.WriteString(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"id":"google-call","name":"lookup","args":{"id":7}},"thoughtSignature":"c2ln"}]}}]}`)
			return
		}
		if !strings.Contains(string(body), "functionResponse") || !strings.Contains(string(body), "c2ln") {
			t.Errorf("native tool identity/signature lost: %s", body)
		}
		io.WriteString(w, googleTextResponse)
	})
	ctx, budget := budgetContext(t, nil)
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "investigate"}}, Investigation: fixtureInvestigation(t, nil, nil)})
	if err != nil || result.Content != "complete" || calls != 2 || budget.Snapshot().ToolCalls != 1 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}

func TestInvestigationSupportsSystemInstructionAndConversationPrefix(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"role":"system","content":"Keep exact policy."`) || !strings.Contains(string(body), `"content":"Earlier turn"`) || !strings.Contains(string(body), `"content":"Earlier answer"`) || !strings.Contains(string(body), `"content":"Final user turn"`) {
			t.Errorf("system or conversation prefix lost: %s", body)
		}
		if calls == 1 {
			writeLookupCall(w, "lookup-prefix")
			return
		}
		json.NewEncoder(w).Encode(fixtureResponse("done"))
	}))
	defer server.Close()
	ctx, budget := budgetContext(t, nil)
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	messages := []llm.ChatMessage{
		{Role: "system", Content: "Keep exact policy."},
		{Role: "user", Content: "Earlier turn"},
		{Role: "assistant", Content: "Earlier answer"},
		{Role: "user", Content: "Final user turn"},
	}
	result, err := Complete(ctx, client, Request{Messages: messages, Investigation: fixtureInvestigation(t, nil, nil)})
	if err != nil || result.Content != "done" || calls != 2 || budget.Snapshot().ToolCalls != 1 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}
