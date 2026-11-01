package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func evaluationTestLimits() config.AgentLimits {
	return config.AgentLimits{ModelCalls: 4, ToolCalls: 8, InputTokens: 100000, OutputTokens: 4096, ElapsedSeconds: 10, ConcurrentCalls: 1, Repairs: 1}
}

func TestAgentEvaluationAdmissionRejectsInvalidScopeWithoutRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	profile := config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 512}
	evaluator, cancel, err := NewAgentEvaluator(t.Context(), profile, evaluationTestLimits(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	for _, files := range []map[string]string{nil, {"../private.env": "PRIVATE_SOURCE"}, {"/absolute.go": "private"}, {"a.go": strings.Repeat("x", (16<<10)+1)}} {
		result, err := evaluator.Evaluate(t.Context(), AgentEvaluationTask{Goal: "inspect", Files: files}, func(AgentEvaluationAnswer) bool { return true })
		if err == nil || result.State != "incomplete" || result.After.ModelCalls != 0 {
			t.Fatal(result, err)
		}
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	result, err := evaluator.Evaluate(ctx, AgentEvaluationTask{Goal: "inspect", Files: map[string]string{"a.go": "package a"}}, func(AgentEvaluationAnswer) bool { return true })
	if err == nil || result.State != "canceled" || calls.Load() != 0 {
		t.Fatal(result, err, calls.Load())
	}
	profile.APIBaseURL = "https://provider.example/v1"
	if _, _, err := NewAgentEvaluator(t.Context(), profile, evaluationTestLimits(), false); err == nil {
		t.Fatal("unconfirmed remote evaluation admitted")
	}
}

func TestAgentEvaluationMalformedAnswersNeverBecomeCompleted(t *testing.T) {
	for _, content := range []string{`null`, `{}`, `{"expression":"a+b","evidence":[],"checks":[],"extra":"PRIVATE"}`, `{"expression":"a+b","expression":"a-b","evidence":[],"checks":[]}`, `{"expression":"a+b","evidence":null,"checks":[]}`} {
		if decodeEvaluationAnswer(content) != nil {
			t.Fatal("invalid answer accepted", content)
		}
	}
}

func TestAgentEvaluationNativeGeminiPreservesToolEvidenceAndUsage(t *testing.T) {
	const source = "package fixture\nfunc Calculate(a, b int) int { return a - b }\n"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Header.Get("x-goog-api-key") != "PRIVATE_KEY" || !strings.Contains(string(body), "read_snippet") {
			t.Error("native configuration or tools missing")
		}
		w.Header().Set("Content-Type", "application/json")
		var part any
		if calls.Add(1) == 1 {
			part = map[string]any{"functionCall": map[string]any{"id": "native-read", "name": "read_snippet", "args": map[string]any{"path": "calc.go", "start_line": 2, "end_line": 2}}, "thoughtSignature": "c2ln"}
		} else {
			if !strings.Contains(string(body), "functionResponse") || !strings.Contains(string(body), "c2ln") {
				t.Error("native tool round trip lost identity")
			}
			answer, err := json.Marshal(AgentEvaluationAnswer{Expression: "a+b", Evidence: []AgentEvaluationCitation{{Path: "calc.go", Hash: agentruntime.TraceIdentity(source), StartLine: 2, EndLine: 2, Quote: strings.Split(source, "\n")[1]}}, Checks: []string{}})
			if err != nil {
				t.Error(err)
			}
			part = map[string]any{"text": string(answer)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"role": "model", "parts": []any{part}}}}, "usageMetadata": map[string]any{"promptTokenCount": 101, "candidatesTokenCount": 17, "totalTokenCount": 118}})
	}))
	defer server.Close()
	evaluator, cancel, err := NewAgentEvaluator(t.Context(), config.ModelProfile{Provider: config.GeminiProvider, APIBaseURL: server.URL, APIKey: "PRIVATE_KEY", Model: "fixture-google", MaxTokens: 512}, evaluationTestLimits(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	result, err := evaluator.Evaluate(t.Context(), AgentEvaluationTask{Goal: "Return the sum of a and b.", Files: map[string]string{"calc.go": source}}, func(answer AgentEvaluationAnswer) bool { return answer.Expression == "a+b" })
	if err != nil || result.State != "completed" || calls.Load() != 2 || result.After.ModelCalls != 2 || result.After.ToolCalls != 1 || result.After.UnknownUsageCalls != 0 || len(result.Attempts) != 1 || len(result.Attempts[0].Steps) != 1 {
		t.Fatal(result, err)
	}
}

func TestAgentEvaluationWaitingCaseCanCancelWithoutDispatch(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	evaluator, cancel, err := NewAgentEvaluator(t.Context(), config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 512}, evaluationTestLimits(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	task := AgentEvaluationTask{Goal: "inspect", Files: map[string]string{"calc.go": "package fixture"}}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = evaluator.Evaluate(t.Context(), task, func(AgentEvaluationAnswer) bool { return true })
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached")
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	result, err := evaluator.Evaluate(ctx, task, func(AgentEvaluationAnswer) bool { return true })
	if err == nil || result.State != "canceled" || calls.Load() != 1 {
		t.Fatal(result, err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider request leaked")
	}
}
