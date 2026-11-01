package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/toolserver"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// scriptedModel is an OpenAI-compatible endpoint that answers pi's requests
// in order: two tool calls, then text. It records the tools pi declared.
type scriptedModel struct {
	mu       sync.Mutex
	requests int
	declared [][]string
}

func (m *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stream bool `json:"stream"`
		Tools  []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	m.mu.Lock()
	var names []string
	for _, tool := range body.Tools {
		names = append(names, tool.Function.Name)
	}
	sort.Strings(names)
	m.declared = append(m.declared, names)
	turn := m.requests
	m.requests++
	m.mu.Unlock()
	delta := map[string]any{"role": "assistant", "content": "Done."}
	finish := "stop"
	if call := [][2]string{{"mcp__kasuri__read_file", `{"path":"notes.txt"}`}, {"mcp__kasuri__finish", `{"summary":"Read the notes."}`}}; turn < len(call) {
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", turn), "type": "function", "function": map[string]any{"name": call[turn][0], "arguments": call[turn][1]}}}}
		finish = "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
	if !body.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "smoke", "object": "chat.completion", "model": "smoke-coder", "choices": []any{map[string]any{"index": 0, "message": delta, "finish_reason": finish}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range []map[string]any{
		{"id": "smoke", "object": "chat.completion.chunk", "model": "smoke-coder", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}},
		{"id": "smoke", "object": "chat.completion.chunk", "model": "smoke-coder", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": usage},
	} {
		encoded, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// TestRealPiReachesOnlyKasurisTools runs the pi installed on this machine
// against a scripted local model, with its own empty configuration, so it
// costs no plan usage. It needs pi and runs only with KASURI_PI_SMOKE=1:
//
//	KASURI_PI_SMOKE=1 go test ./internal/app -run TestRealPi -v
func TestRealPiReachesOnlyKasurisTools(t *testing.T) {
	if os.Getenv("KASURI_PI_SMOKE") != "1" {
		t.Skip("set KASURI_PI_SMOKE=1 to run the installed pi against a scripted local model")
	}
	model := &scriptedModel{}
	endpoint := httptest.NewServer(model)
	defer endpoint.Close()
	agentDir := t.TempDir()
	models := fmt.Sprintf(`{"providers":{"smoke":{"baseUrl":%q,"api":"openai-completions","apiKey":"smoke","models":[{"id":"smoke-coder"}]}}}`, endpoint.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	var calls []string
	var mu sync.Mutex
	record := func(name string) func(context.Context, json.RawMessage) (string, error) {
		return func(_ context.Context, arguments json.RawMessage) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name+" "+string(arguments))
			return "ok: " + name, nil
		}
	}
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"summary":{"type":"string"}},"additionalProperties":false}`)
	server, err := toolserver.Start(context.Background(), "Smoke test tools.", []toolserver.Tool{
		{Name: "read_file", Description: "Read a file.", Schema: schema, ReadOnly: true, Call: record("read_file")},
		{Name: "finish", Description: "Finish.", Schema: schema, Call: record("finish")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cli := os.Getenv("KASURI_PI_SMOKE_CLI")
	if cli == "" {
		cli = "pi"
	}
	client := llm.NewClient(config.ModelProfile{Provider: config.PiProvider, Model: "smoke/smoke-coder", CLIPath: cli})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := client.RunAgent(ctx, llm.AgentRun{System: "Use the tools, then finish.", Prompt: "Read notes.txt, then finish.", MCPURL: server.URL, MCPToken: server.Token, MaxTurns: 10})
	if err != nil {
		t.Fatalf("pi run: %v (result %+v)", err, result)
	}
	if result.Turns != 3 || result.ToolCalls != 2 || result.ToolErrors != 0 || result.Final != "Done." || result.Usage.TotalTokens != 330 {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Equal(calls, []string{`read_file {"path":"notes.txt"}`, `finish {"summary":"Read the notes."}`}) {
		t.Fatalf("tool server calls = %v", calls)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, declared := range model.declared {
		if !slices.Equal(declared, []string{"mcp__kasuri__finish", "mcp__kasuri__read_file"}) {
			t.Fatalf("pi declared %v to the model; only Kasuri's tools may reach it", declared)
		}
	}
}
