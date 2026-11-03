package agentruntime

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
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

const googleTextResponse = `{"modelVersion":"fixture-google","candidates":[{"content":{"role":"model","parts":[{"text":"complete"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`

func googleFixture(t *testing.T, handler http.HandlerFunc) *ProviderClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewProviderClient(config.ModelProfile{Provider: config.GeminiProvider, APIBaseURL: server.URL, APIKey: "configured-key", Model: "fixture-google", Temperature: 0.2, MaxTokens: 100})
}

func googleRequest() Request {
	return Request{Messages: []llm.ChatMessage{{Role: "system", Content: "policy"}, {Role: "user", Content: "request"}}}
}

func TestGoogleBudgetCountsThinkingAndRejectsOverrunBeforePublication(t *testing.T) {
	client := googleFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, strings.Replace(googleTextResponse, `"totalTokenCount":8`, `"thoughtsTokenCount":120,"totalTokenCount":128`, 1))
	})
	ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.OutputTokens = 100 })
	result, err := Complete(ctx, client, googleRequest())
	if !errors.Is(err, ErrBudgetExceeded) || result != (Result{}) || budget.Snapshot().OutputTokens != 123 {
		t.Fatalf("thinking overrun: result=%+v usage=%+v error=%v", result, budget.Snapshot(), err)
	}
	result, err = Complete(context.Background(), client, googleRequest())
	if err != nil || !result.UsageKnown || result.Usage.CompletionTokens != 123 {
		t.Fatalf("thinking metadata lost: %+v %v", result, err)
	}
}

func TestGoogleCompletionUsesExplicitEndpointCredentialsAndSampling(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "ambient-key")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "http://must-not-connect.invalid")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/fixture-google:generateContent" || r.Header.Get("X-Goog-Api-Key") != "configured-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected route or authentication: %s", r.URL.Path)
		}
		var body struct {
			SystemInstruction *genai.Content `json:"systemInstruction"`
			GenerationConfig  struct {
				Temperature        float32        `json:"temperature"`
				MaxOutputTokens    int            `json:"maxOutputTokens"`
				ResponseMIMEType   string         `json:"responseMimeType"`
				ResponseJSONSchema map[string]any `json:"responseJsonSchema"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.SystemInstruction == nil || body.SystemInstruction.Parts[0].Text != "policy" || body.GenerationConfig.Temperature != 0.2 || body.GenerationConfig.MaxOutputTokens != 100 || body.GenerationConfig.ResponseMIMEType != "application/json" || body.GenerationConfig.ResponseJSONSchema["type"] != "object" {
			t.Errorf("native request changed: %+v", body)
		}
		io.WriteString(w, strings.Replace(googleTextResponse, "complete", `{\"answer\":\"done\"}`, 1))
	})
	request := googleRequest()
	request.Schema = &llm.JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object"}`)}
	result, err := Complete(context.Background(), client, request)
	if err != nil || result.Content != `{"answer":"done"}` || result.Model != "fixture-google" || result.Usage.TotalTokens != 8 || result.FinishReason != "stop" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestGoogleVertexBackendsBindAuthentication(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "ambient-key")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "ambient-location")
	t.Setenv("GOOGLE_VERTEX_BASE_URL", "http://must-not-connect.invalid")
	for _, express := range []bool{false, true} {
		t.Run(map[bool]string{false: "project token", true: "express key"}[express], func(t *testing.T) {
			client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
				want := "/v1/publishers/google/models/fixture-google:generateContent"
				if !express {
					want = "/v1/projects/project/locations/global/publishers/google/models/fixture-google:generateContent"
				}
				if r.URL.Path != want {
					t.Errorf("path=%s want=%s", r.URL.Path, want)
				}
				if express && (r.Header.Get("X-Goog-Api-Key") != "configured-key" || r.Header.Get("Authorization") != "") {
					t.Error("wrong Express authentication")
				}
				if !express && (r.Header.Get("Authorization") != "Bearer explicit-token" || r.Header.Get("X-Goog-Api-Key") != "") {
					t.Error("wrong Vertex authentication")
				}
				io.WriteString(w, googleTextResponse)
			})
			client.profile.Provider = config.VertexProvider
			if !express {
				client.profile.APIKey = ""
				client.profile.AccessToken = "explicit-token"
				client.profile.VertexProject = "project"
				client.profile.VertexLocation = "global"
			}
			if _, err := Complete(context.Background(), client, googleRequest()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoogleNeverRetriesRedirectsFailuresOrSDKContinuation(t *testing.T) {
	for name, status := range map[string]int{"redirect": http.StatusTemporaryRedirect, "request": http.StatusBadRequest, "server": http.StatusServiceUnavailable, "continuation": http.StatusOK} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://private-token.invalid/%zz")
				w.WriteHeader(status)
				if name == "continuation" {
					io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"unfinished"}]},"finishReason":"CONTINUATION"}]}`)
					return
				}
				io.WriteString(w, `{"error":{"message":"private-source-and-token"}}`)
			})
			result, err := Complete(context.Background(), client, googleRequest())
			if err == nil || result != (Result{}) || calls.Load() != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%+v error=%v requests=%d", result, err, calls.Load())
			}
			if name == "redirect" && !errors.Is(err, llm.ErrRedirectRejected) {
				t.Fatalf("redirect classification=%v", err)
			}
		})
	}
}

func TestGoogleRejectsUnsafeRequestsWithoutDispatch(t *testing.T) {
	client := googleFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unsafe request dispatched") })
	for name, mutate := range map[string]func(*model.LLMRequest){
		"route": func(r *model.LLMRequest) { r.Model = "different-model" },
		"override endpoint": func(r *model.LLMRequest) {
			r.Config.HTTPOptions = &genai.HTTPOptions{BaseURL: "http://another.invalid"}
		},
		"builtin tool": func(r *model.LLMRequest) { r.Config.Tools = []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}} },
		"attachment":   func(r *model.LLMRequest) { r.Contents[0].Parts[0].InlineData = &genai.Blob{Data: []byte("private")} },
		"oversized": func(r *model.LLMRequest) {
			r.Contents[0].Parts[0].Text = strings.Repeat("x", nativeProviderByteLimit+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request, _ := googleCompletionRequest(googleRequest())
			mutate(request)
			var failure error
			for response, err := range (&googleModel{client: client}).GenerateContent(context.Background(), request, false) {
				failure = err
				if response != nil {
					t.Fatal("unexpected response")
				}
			}
			if !errors.Is(failure, llm.ErrRequestRejected) {
				t.Fatalf("error=%v", failure)
			}
		})
	}
}

func TestGoogleRejectsIncompleteMalformedAndOversizedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":      `{"private-source":`,
		"blocked":        `{"promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"private-source"}}`,
		"truncated":      strings.Replace(googleTextResponse, "STOP", "MAX_TOKENS", 1),
		"missing finish": strings.Replace(googleTextResponse, `,"finishReason":"STOP"`, "", 1),
		"oversized":      strings.Replace(googleTextResponse, "complete", strings.Repeat("x", nativeProviderByteLimit), 1),
		"negative usage": strings.Replace(googleTextResponse, `"totalTokenCount":8`, `"totalTokenCount":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := googleFixture(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })
			result, err := Complete(context.Background(), client, googleRequest())
			if err == nil || result != (Result{}) || strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%+v error=%v", result, err)
			}
		})
	}
}

func TestGoogleStreamingNeedsTerminalStateAndClosesOnConsumerError(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "truncated", true: "complete"}[terminal], func(t *testing.T) {
			client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("alt") != "sse" {
					t.Error("missing stream mode")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				body := googleTextResponse
				if !terminal {
					body = strings.Replace(body, `,"finishReason":"STOP"`, "", 1)
				}
				io.WriteString(w, "data: "+body+"\n\n")
			})
			request := googleRequest()
			request.Stream = true
			var partial strings.Builder
			request.OnText = func(s string) error { partial.WriteString(s); return nil }
			result, err := Complete(context.Background(), client, request)
			if terminal && (err != nil || result.Content != "complete" || result.Model != "fixture-google" || partial.String() != "complete") || !terminal && (err == nil || result != (Result{})) {
				t.Fatalf("result=%+v partial=%s error=%v", result, partial.String(), err)
			}
		})
	}
	closed := make(chan struct{})
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+strings.Replace(googleTextResponse, `,"finishReason":"STOP"`, "", 1)+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	cause := errors.New("stop preview")
	request := googleRequest()
	request.Stream = true
	request.OnText = func(string) error { return cause }
	result, err := Complete(context.Background(), client, request)
	if !errors.Is(err, cause) || result != (Result{}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("native provider outlived consumer")
	}
}

func TestGoogleDeadlineAndCancellationRemainCallerOwned(t *testing.T) {
	parent, stop := context.WithTimeout(context.Background(), 15*time.Minute)
	defer stop()
	child, cancel := nativeRequestContext(parent)
	want, _ := parent.Deadline()
	got, _ := child.Deadline()
	cancel()
	if !got.Equal(want) {
		t.Fatal("native request shortened the admitted workflow deadline")
	}
	entered, closed := make(chan struct{}), make(chan struct{})
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			t.Error("provider cancellation was not observed")
		}
		close(closed)
	})
	ctx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	done := make(chan error, 1)
	go func() { _, err := Complete(ctx, client, googleRequest()); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	cancelRequest()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native call did not stop")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider outlived canceled invocation")
	}
}

func TestGoogleOptionalContentIsExplicitAndCredentialsNeverFallBack(t *testing.T) {
	client := googleFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"modelVersion":"fixture-google","candidates":[{"finishReason":"STOP"}]}`)
	})
	if _, err := Complete(context.Background(), client, googleRequest()); !errors.Is(err, llm.ErrUnusableResponse) {
		t.Fatalf("required content error=%v", err)
	}
	result, err := Complete(context.Background(), client.WithOptionalFinalContent(), googleRequest())
	if err != nil || result.Content != "" || result.FinishReason != "stop" {
		t.Fatalf("optional result=%+v error=%v", result, err)
	}
	t.Setenv("GOOGLE_API_KEY", "ambient-secret")
	client = googleFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("missing explicit credentials dispatched") })
	client.profile.APIKey = ""
	if _, err := Complete(context.Background(), client, googleRequest()); !errors.Is(err, llm.ErrRequestRejected) {
		t.Fatalf("missing credentials error=%v", err)
	}
}

func TestGoogleToolTurnsPreserveThoughtSignatures(t *testing.T) {
	var calls atomic.Int32
	client := googleFixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"read_file","args":{"path":"main.go"}},"thoughtSignature":"c2lnbmF0dXJl"}]},"finishReason":"STOP"}]}`)
			return
		}
		if !strings.Contains(string(body), `"thoughtSignature":"c2lnbmF0dXJl"`) || !strings.Contains(string(body), `"id":"call_1"`) {
			t.Errorf("tool signature or identity lost: %s", body)
		}
		io.WriteString(w, googleTextResponse)
	})
	input := nativeToolRequest()
	adapter := &googleModel{client: client}
	var first *model.LLMResponse
	for response, err := range adapter.GenerateContent(context.Background(), input, false) {
		if err != nil {
			t.Fatal(err)
		}
		first = response
	}
	if first == nil || string(first.Content.Parts[0].ThoughtSignature) != "signature" {
		t.Fatalf("native signature lost: %+v", first)
	}
	input.Contents = append(input.Contents, first.Content, &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call_1", Name: "read_file", Response: map[string]any{"source": "package main"}}}}})
	for response, err := range adapter.GenerateContent(context.Background(), input, false) {
		if err != nil || response.Content.Parts[0].Text != "complete" {
			t.Fatalf("response=%+v error=%v", response, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
}
