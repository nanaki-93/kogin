package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func generationInput() GenerationRequest {
	return GenerationRequest{
		Messages: []GenerationMessage{{ChatMessage: ChatMessage{Role: "user", Content: "Inspect main.go"}}},
		Tools:    []ToolDefinition{{Name: "read_file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}},
	}
}

func TestGenerationPreservesSafeFailureCategories(t *testing.T) {
	client := NewClient(config.ModelProfile{})
	toolSchemas := map[string]*jsonschema.Schema{"read_file": nil}
	for _, test := range []struct {
		refusal, finish string
		schemas         map[string]*jsonschema.Schema
		want            error
	}{
		{refusal: "PRIVATE_PROVIDER_REASON", finish: "stop", want: ErrGenerationRefused},
		{finish: "tool_calls", want: ErrGenerationIncomplete},
		{finish: "length", schemas: toolSchemas, want: ErrGenerationIncomplete},
		{finish: "content_filter", schemas: toolSchemas, want: ErrGenerationIncomplete},
	} {
		wire := generationWireMessage{ChatMessage: ChatMessage{Role: "assistant", Content: "PRIVATE_OUTPUT", Refusal: test.refusal}}
		result, err := client.validateGenerationResult(wire, "model", test.finish, nil, test.schemas, false)
		if result != nil || !errors.Is(err, test.want) || !errors.Is(err, ErrUnusableResponse) || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatalf("unsafe or lost failure category: result=%+v error=%v", result, err)
		}
	}
}

func TestGenerationCacheUsagePreservesUnknownZeroAndRejectsInvalidCounts(t *testing.T) {
	for _, details := range []string{"", `,"prompt_tokens_details":{"cached_tokens":0}`, `,"prompt_tokens_details":{"cached_tokens":2}`, `,"prompt_tokens_details":{"cached_tokens":-1}`, `,"prompt_tokens_details":{"cached_tokens":4}`} {
		for _, stream := range []bool{false, true} {
			usage := `{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5` + details + `}`
			body := `{"model":"fixture","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":` + usage + `}`
			if stream {
				body = streamDelta(t, map[string]any{"content": "done"}, "stop") + `data: {"choices":[],"usage":` + usage + "}\n\ndata: [DONE]\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = io.WriteString(w, body)
			}))
			cl := NewClient(testProfile(server.URL, ""))
			input := generationInput()
			input.Stream = stream
			result, _, err := collectGeneration(cl, t.Context(), input)
			server.Close()
			invalid := strings.Contains(details, ":-1") || strings.Contains(details, ":4")
			if invalid {
				if !errors.Is(err, ErrUnusableResponse) || result != nil {
					t.Fatalf("invalid cache usage accepted: %+v %v", result, err)
				}
				continue
			}
			if err != nil || result == nil || result.Usage == nil || (result.Usage.PromptTokensDetails == nil) != (details == "") {
				t.Fatalf("lost cache knowledge (stream=%t): %+v %v", stream, result, err)
			}
			if strings.Contains(details, ":2") && result.Usage.PromptTokensDetails.CachedTokens != 2 {
				t.Fatal("lost cached tokens")
			}
		}
	}
}

func collectGeneration(client *Client, ctx context.Context, input GenerationRequest) (*GenerationResult, []string, error) {
	var result *GenerationResult
	var deltas []string
	for event, err := range client.Generate(ctx, input) {
		if err != nil {
			return nil, deltas, err
		}
		if event.Result != nil {
			result = event.Result
		} else {
			deltas = append(deltas, event.TextDelta)
		}
	}
	return result, deltas, nil
}

func TestGenerationToolConversationKeepsIdentityAndSampling(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request generationWireRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.TopK == nil || *request.TopK != 17 || request.MinP == nil || *request.MinP != 0.1 || len(request.Tools) != 1 {
			t.Errorf("sampling/tools changed: %+v", request)
		}
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"model":"fixture","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
			return
		}
		if len(request.Messages) != 3 || request.Messages[1].ToolCalls[0].ID != "call_1" || request.Messages[2].ToolCallID != "call_1" || request.Messages[2].Name != "read_file" || request.Messages[2].Content != `{"source":"package main"}` {
			t.Errorf("tool history changed: %+v", request.Messages)
		}
		io.WriteString(w, `{"model":"fixture","choices":[{"message":{"role":"assistant","content":"The file declares main."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	profile := testProfile(server.URL, "fixture-key")
	k, minP := 17, float32(0.1)
	profile.TopK, profile.MinP = &k, &minP
	client := NewClient(profile)
	input := generationInput()
	first, _, err := collectGeneration(client, context.Background(), input)
	if err != nil || first == nil || len(first.Message.ToolCalls) != 1 || first.Usage == nil || first.Usage.TotalTokens != 8 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	input.Messages = append(input.Messages, first.Message, GenerationMessage{ChatMessage: ChatMessage{Role: "tool", Content: `{"source":"package main"}`}, ToolCallID: "call_1", ToolName: "read_file"})
	second, _, err := collectGeneration(client, context.Background(), input)
	if err != nil || second.Message.Content != "The file declares main." || second.Usage != nil || calls.Load() != 2 {
		t.Fatalf("second=%+v error=%v calls=%d", second, err, calls.Load())
	}
}

func TestGenerationRejectsInvalidCallsBeforePublication(t *testing.T) {
	valid := generationWireCall{ID: "call_1", Type: "function"}
	valid.Function.Name, valid.Function.Arguments = "read_file", `{"path":"main.go"}`
	for _, test := range []struct {
		name   string
		modify func(*generationWireCall)
	}{
		{"unknown name", func(c *generationWireCall) { c.Function.Name = "shell" }},
		{"no identity", func(c *generationWireCall) { c.ID = "" }},
		{"invalid identity", func(c *generationWireCall) { c.ID = "bad\nidentity" }},
		{"wrong type", func(c *generationWireCall) { c.Type = "custom" }},
		{"truncated JSON", func(c *generationWireCall) { c.Function.Arguments = `{"path":` }},
		{"missing required", func(c *generationWireCall) { c.Function.Arguments = `{}` }},
		{"wrong argument type", func(c *generationWireCall) { c.Function.Arguments = `{"path":3}` }},
		{"extra argument", func(c *generationWireCall) { c.Function.Arguments = `{"path":"main.go","command":"unsafe"}` }},
		{"duplicate argument", func(c *generationWireCall) { c.Function.Arguments = `{"path":"a","path":"b"}` }},
		{"oversized arguments", func(c *generationWireCall) {
			c.Function.Arguments = `{"path":"` + strings.Repeat("x", maxToolJSONBytes) + `"}`
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := valid
			test.modify(&call)
			body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": generationWireMessage{ChatMessage: ChatMessage{Role: "assistant"}, ToolCalls: []generationWireCall{call}}, "finish_reason": "tool_calls"}}})
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(testProfile("http://provider.invalid", ""))
			client.transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			result, _, err := collectGeneration(client, context.Background(), generationInput())
			if result != nil || !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestGenerationInvalidAdmissionDoesNotDispatch(t *testing.T) {
	client := NewClient(testProfile("http://provider.invalid", ""))
	client.transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid admission reached transport")
		return nil, nil
	})
	for _, change := range []func(*GenerationRequest){
		func(r *GenerationRequest) { r.Messages = nil },
		func(r *GenerationRequest) { r.Tools[0].Name = "bad name" },
		func(r *GenerationRequest) { r.Tools = append(r.Tools, r.Tools[0]) },
		func(r *GenerationRequest) {
			r.Tools[0].Parameters = json.RawMessage(`{"$ref":"https://secret.invalid/schema"}`)
		},
		func(r *GenerationRequest) { r.Tools[0].Parameters = json.RawMessage(`{"type":"array"}`) },
		func(r *GenerationRequest) { r.Messages[0].Role = "unknown" },
		func(r *GenerationRequest) { r.Messages[0].Role = "tool"; r.Messages[0].ToolCallID = "forged" },
		func(r *GenerationRequest) { r.Messages[0].Content = strings.Repeat("x", maxProviderResponseBytes) },
	} {
		input := generationInput()
		change(&input)
		if result, _, err := collectGeneration(client, context.Background(), input); result != nil || !errors.Is(err, ErrRequestRejected) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := collectGeneration(client, ctx, generationInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestGenerationToolHistoryRequiresMatchingResponses(t *testing.T) {
	call := ToolCall{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)}
	assistant := GenerationMessage{ChatMessage: ChatMessage{Role: "assistant"}, ToolCalls: []ToolCall{call}}
	response := GenerationMessage{ChatMessage: ChatMessage{Role: "tool", Content: `{}`}, ToolCallID: call.ID, ToolName: call.Name}
	input := generationInput()
	input.Messages = append(input.Messages, assistant, response)
	if _, err := validateGenerationRequest(input); err != nil {
		t.Fatal(err)
	}
	for _, history := range [][]GenerationMessage{
		{assistant}, {response}, {assistant, input.Messages[0], response}, {assistant, response, response}, {assistant, response, assistant, response},
		{assistant, {ChatMessage: response.ChatMessage, ToolCallID: call.ID, ToolName: "another_tool"}},
	} {
		candidate := generationInput()
		candidate.Messages = append(candidate.Messages, history...)
		if _, err := validateGenerationRequest(candidate); !errors.Is(err, ErrRequestRejected) {
			t.Fatalf("invalid history accepted: %+v, %v", history, err)
		}
	}
	if err := validateFreshCallIDs([]ToolCall{call}, input.Messages); !errors.Is(err, ErrUnusableResponse) {
		t.Fatalf("reused identity accepted: %v", err)
	}
}

func TestGenerationCLIRejectsToolsAndStreamingWithoutStarting(t *testing.T) {
	client := NewClient(config.ModelProfile{Provider: config.PiProvider, CLIPath: "/nonexistent/never-start", Model: "fixture"})
	for _, stream := range []bool{false, true} {
		input := generationInput()
		input.Stream = stream
		if stream {
			input.Tools = nil
		}
		if _, _, err := collectGeneration(client, context.Background(), input); !errors.Is(err, ErrRequestRejected) || strings.Contains(err.Error(), "executable") {
			t.Fatalf("unsupported CLI request=%v", err)
		}
	}
}

func TestGenerationRedirectAndSchemaRejectionNeverRetry(t *testing.T) {
	for _, status := range []int{307, 400, 422} {
		calls := 0
		client := NewClient(testProfile("http://provider.invalid", "secret"))
		client.transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://other.invalid/secret"}}, Body: io.NopCloser(strings.NewReader("provider-private-error"))}, nil
		})
		input := generationInput()
		input.Schema = &JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object"}`)}
		_, _, err := collectGeneration(client, context.Background(), input)
		want := ErrStructuredRequestRejected
		if status == 307 {
			want = ErrRedirectRejected
		}
		if !errors.Is(err, want) || calls != 1 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("status=%d calls=%d error=%v", status, calls, err)
		}
	}
}
