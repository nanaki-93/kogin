package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

type completionFixture func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error)

func (f completionFixture) Chat(ctx context.Context, messages []llm.ChatMessage) (*llm.ChatResponse, error) {
	return f(ctx, messages, nil)
}

func (f completionFixture) ChatWithJSONSchema(ctx context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	return f(ctx, messages, &schema)
}

func fixtureResponse(content string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model: "fixture-model", Usage: llm.ChatUsage{PromptTokens: 31, CompletionTokens: 7, TotalTokens: 38},
		Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: content}, FinishReason: "stop"}},
	}
}

func TestCompletionRunnerPreservesConversationSchemaAndMetadata(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "Keep this exact policy."},
		{Role: "user", Content: "Original request"},
		{Role: "assistant", Content: "Prior answer", ReasoningContent: "retained field", Reasoning: "alternate field"},
		{Role: "user", Content: "Follow-up request"},
	}
	for _, schema := range []*llm.JSONSchema{nil, {Name: "answer", Schema: []byte(`{"type":"object"}`)}} {
		calls := 0
		client := completionFixture(func(_ context.Context, got []llm.ChatMessage, gotSchema *llm.JSONSchema) (*llm.ChatResponse, error) {
			calls++
			if !reflect.DeepEqual(got, messages) || !reflect.DeepEqual(gotSchema, schema) {
				t.Fatalf("request changed: messages=%+v schema=%+v", got, gotSchema)
			}
			return fixtureResponse(`{"answer":"done"}`), nil
		})
		result, err := Complete(context.Background(), client, Request{Messages: messages, Schema: schema})
		want := Result{Content: `{"answer":"done"}`, Model: "fixture-model", FinishReason: "stop", Usage: fixtureResponse("").Usage, UsageKnown: true}
		if err != nil || result != want || calls != 1 {
			t.Fatalf("result=%+v, error=%v, calls=%d", result, err, calls)
		}
	}
}

func TestCompletionDoesNotRetryProviderFailures(t *testing.T) {
	for _, cause := range []error{llm.ErrRedirectRejected, llm.ErrStructuredRequestRejected, llm.ErrRequestRejected, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
				calls++
				return nil, cause
			})
			result, err := Complete(context.Background(), client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
			if !errors.Is(err, cause) || calls != 1 || result != (Result{}) {
				t.Fatalf("result=%+v, error=%v, calls=%d", result, err, calls)
			}
		})
	}
}

func TestCompletionCancellationEndsOwnedInvocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	client := completionFixture(func(ctx context.Context, _ []llm.ChatMessage, _ *llm.JSONSchema) (*llm.ChatResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	go func() {
		_, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestCompletionRejectsLateResultAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
		cancel()
		return fixtureResponse("late answer"), nil
	})
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
	if !errors.Is(err, context.Canceled) || result != (Result{}) {
		t.Fatalf("late result=%+v, error=%v", result, err)
	}
}

func TestCompletionRejectsMissingChoiceButPreservesTransportEmptyPolicy(t *testing.T) {
	for _, test := range []struct {
		response *llm.ChatResponse
		valid    bool
	}{{nil, false}, {&llm.ChatResponse{}, false}, {fixtureResponse(""), true}} {
		client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
			return test.response, nil
		})
		_, err := Complete(context.Background(), client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
		if test.valid && err != nil || !test.valid && !errors.Is(err, llm.ErrUnusableResponse) {
			t.Fatalf("valid=%v, error=%v", test.valid, err)
		}
	}
}

func TestCompletionSnapshotsCallerInput(t *testing.T) {
	request := Request{
		Messages: []llm.ChatMessage{{Content: "original"}},
		Schema:   &llm.JSONSchema{Name: "original", Schema: []byte(`{"type":"object"}`)},
	}
	client := completionFixture(func(_ context.Context, messages []llm.ChatMessage, schema *llm.JSONSchema) (*llm.ChatResponse, error) {
		messages[0].Content = "mutated"
		schema.Schema[0] = 'X'
		return fixtureResponse("done"), nil
	})
	if _, err := Complete(context.Background(), client, request); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "original" || string(request.Schema.Schema) != `{"type":"object"}` {
		t.Fatalf("caller input mutated: %+v", request)
	}
}

func TestCompletionInvocationsHaveIndependentHistory(t *testing.T) {
	var workers sync.WaitGroup
	for i := range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			want := fmt.Sprintf("request-%d", i)
			client := completionFixture(func(_ context.Context, messages []llm.ChatMessage, _ *llm.JSONSchema) (*llm.ChatResponse, error) {
				return fixtureResponse(messages[0].Content), nil
			})
			result, err := Complete(context.Background(), client, Request{Messages: []llm.ChatMessage{{Content: want}}})
			if err != nil || result.Content != want {
				t.Errorf("result=%+v, error=%v, want=%s", result, err, want)
			}
		}()
	}
	workers.Wait()
}

func TestCompletionRequiresOneTerminalResultAndDrainsErrors(t *testing.T) {
	final := &session.Event{LLMResponse: model.LLMResponse{TurnComplete: true}, Output: Result{Content: "done"}}
	partial := &session.Event{LLMResponse: model.LLMResponse{Partial: true}, Output: Result{Content: "unfinished"}}
	for _, events := range [][]*session.Event{nil, {nil}, {partial}, {final, final}, {{LLMResponse: model.LLMResponse{TurnComplete: true}}}} {
		stream := func(yield func(*session.Event, error) bool) {
			for _, event := range events {
				if !yield(event, nil) {
					return
				}
			}
		}
		if result, err := collectCompletion(context.Background(), stream, nil); err == nil || result != (Result{}) {
			t.Fatalf("invalid stream accepted: result=%+v error=%v", result, err)
		}
	}
	cause := errors.New("terminal persistence failed")
	stream := iter.Seq2[*session.Event, error](func(yield func(*session.Event, error) bool) {
		if yield(final, nil) {
			yield(nil, cause)
		}
	})
	if result, err := collectCompletion(context.Background(), stream, nil); !errors.Is(err, cause) || result != (Result{}) {
		t.Fatalf("post-result error lost: result=%+v error=%v", result, err)
	}
}

func TestInvalidOrCanceledCompletionDoesNotDispatch(t *testing.T) {
	client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
		t.Fatal("invalid request reached provider")
		return nil, nil
	})
	for _, request := range []Request{{}, {Messages: []llm.ChatMessage{{Content: " "}}}} {
		if _, err := Complete(context.Background(), client, request); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Content: "request"}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error=%v", err)
	}
}

func nativeFixture(t *testing.T, handler http.HandlerFunc) *llm.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	topK := 17
	return llm.NewClient(config.ModelProfile{APIBaseURL: server.URL, Model: "fixture-model", MaxTokens: 100, TopK: &topK})
}

func TestNativeCompletionPreservesConversationAndSchema(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "policy"}, {Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer", ReasoningContent: "opaque", Reasoning: "alternate"},
		{Role: "developer", Content: "additional policy"}, {Role: "user", Content: "next"},
	}
	client := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !reflect.DeepEqual(request.Messages, messages) || request.TopK == nil || *request.TopK != 17 {
			t.Errorf("conversation or profile changed: %+v", request)
		}
		if request.ResponseFormat == nil || request.ResponseFormat.JSONSchema.Name != "report" || !request.ResponseFormat.JSONSchema.Strict {
			t.Errorf("schema identity changed: %+v", request.ResponseFormat)
		}
		json.NewEncoder(w).Encode(fixtureResponse(`{"answer":"done"}`))
	})
	result, err := Complete(context.Background(), client, Request{Messages: messages, Schema: &llm.JSONSchema{Name: "report", Schema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil || result.Content != `{"answer":"done"}` || result.Model != "fixture-model" || result.Usage.TotalTokens != 38 || result.FinishReason != "stop" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func nativeToolRequest() *model.LLMRequest {
	return &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("Inspect main.go", genai.RoleUser)},
		Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "read_file", ParametersJsonSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
			ResponseJsonSchema: map[string]any{"type": "object"},
		}}}}},
	}
}

func collectNative(t *testing.T, adapter *compatibleModel, request *model.LLMRequest) *model.LLMResponse {
	t.Helper()
	var result *model.LLMResponse
	for response, err := range adapter.GenerateContent(context.Background(), request, false) {
		if err != nil {
			t.Fatal(err)
		}
		if result != nil || response == nil || !response.TurnComplete || response.Partial {
			t.Fatal("invalid native response sequence")
		}
		result = response
	}
	if result == nil {
		t.Fatal("missing native response")
	}
	return result
}

func TestNativeModelRoundTripsTypedToolCalls(t *testing.T) {
	var calls atomic.Int32
	client := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role, Content    string
				ReasoningContent string `json:"reasoning_content"`
				ToolCallID       string `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct{ Function llm.ToolDefinition } `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "read_file" {
			t.Errorf("missing tool declaration: %+v", request.Tools)
		}
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"model":"fixture-model","choices":[{"message":{"role":"assistant","reasoning_content":"opaque tool reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		if len(request.Messages) != 3 || request.Messages[2].Role != "tool" || request.Messages[2].ToolCallID != "call_1" || request.Messages[2].Content != `{"source":"package main"}` {
			t.Errorf("tool response changed: %+v", request.Messages)
		}
		if len(request.Messages) == 3 && request.Messages[1].ReasoningContent != "opaque tool reasoning" {
			t.Error("opaque reasoning was lost between tool turns")
		}
		json.NewEncoder(w).Encode(fixtureResponse("complete"))
	})
	adapter := &compatibleModel{client: client}
	request := nativeToolRequest()
	first := collectNative(t, adapter, request)
	call := first.Content.Parts[1].FunctionCall
	if call == nil || call.ID != "call_1" || call.Name != "read_file" || call.Args["path"] != "main.go" || first.UsageMetadata != nil {
		t.Fatalf("call=%+v usage=%+v", call, first.UsageMetadata)
	}
	request.Contents = append(request.Contents, first.Content, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: call.ID, Name: call.Name, Response: map[string]any{"source": "package main"},
	}}}})
	second := collectNative(t, adapter, request)
	if second.Content.Parts[0].Text != "complete" || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d", second, calls.Load())
	}
}

func TestNativeModelRejectsUnsupportedRequestsBeforeDispatch(t *testing.T) {
	client := nativeFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request dispatched") })
	for name, mutate := range map[string]func(*model.LLMRequest){
		"route":        func(r *model.LLMRequest) { r.Model = "another-model" },
		"sampling":     func(r *model.LLMRequest) { value := float32(0.8); r.Config.Temperature = &value },
		"media":        func(r *model.LLMRequest) { r.Contents[0].Parts[0].InlineData = &genai.Blob{Data: []byte("image")} },
		"builtin tool": func(r *model.LLMRequest) { r.Config.Tools[0].GoogleSearch = &genai.GoogleSearch{} },
		"unknown call": func(r *model.LLMRequest) {
			r.Contents[0].Parts = []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "shell"}}}
		},
		"missing tool result": func(r *model.LLMRequest) {
			r.Contents[0].Parts = []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call_1", Name: "read_file"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := nativeToolRequest()
			mutate(request)
			var failure error
			for response, err := range (&compatibleModel{client: client}).GenerateContent(context.Background(), request, false) {
				failure = err
				if response != nil {
					t.Fatal("invalid request produced content")
				}
			}
			if !errors.Is(failure, llm.ErrRequestRejected) {
				t.Fatalf("error=%v", failure)
			}
		})
	}
}

const partialNativeStream = "data: {\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
const finalNativeStream = "data: {\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func TestNativeCompletionOnlyPublishesFinishedStreams(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			client := nativeFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, partialNativeStream)
				if terminal {
					io.WriteString(w, finalNativeStream)
				}
			})
			var observed strings.Builder
			result, err := Complete(context.Background(), client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}, Stream: true, OnText: func(delta string) error { observed.WriteString(delta); return nil }})
			if observed.String() != "partial" {
				t.Fatalf("partial text=%q", observed.String())
			}
			if terminal && (err != nil || result.Content != "partial") || !terminal && (err == nil || result != (Result{})) {
				t.Fatalf("terminal=%v result=%+v error=%v", terminal, result, err)
			}
		})
	}
}

func TestNativeCompletionCallbackFailureClosesProvider(t *testing.T) {
	closed := make(chan struct{})
	client := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, partialNativeStream)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	cause := errors.New("consumer stopped")
	result, err := Complete(context.Background(), client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}, Stream: true, OnText: func(string) error { return cause }})
	if !errors.Is(err, cause) || result != (Result{}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider outlived the failed consumer")
	}
}
