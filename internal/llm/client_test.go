package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/logging"
)

func TestChatHonorsWorkflowDeadlinesAndBoundsDirectRequests(t *testing.T) {
	for _, allowance := range []time.Duration{0, time.Minute, 10 * time.Minute, 20 * time.Minute} {
		t.Run(allowance.String(), func(t *testing.T) {
			ctx := context.Background()
			if allowance > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, allowance)
				defer cancel()
			}
			before := time.Now()
			client := NewClient(testProfile("http://provider.invalid", ""))
			client.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				deadline, bounded := request.Context().Deadline()
				if !bounded {
					t.Fatal("provider request has no deadline")
				}
				if want, supplied := ctx.Deadline(); supplied {
					if !deadline.Equal(want) {
						t.Fatalf("provider deadline=%s, workflow deadline=%s", deadline, want)
					}
				} else if deadline.Before(before.Add(5*time.Minute)) || deadline.After(time.Now().Add(5*time.Minute)) {
					t.Fatalf("direct request fallback deadline=%s", deadline)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"done"}}]}`))}, nil
			})
			if _, err := client.Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChatLongWorkflowDeadlineStillAllowsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := NewClient(testProfile("http://provider.invalid", ""))
	client.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		cancel()
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	if _, err := client.Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("provider cancellation=%v", err)
	}
}

func TestChatRejectsMalformedNonSuccessAndOversizedProviderResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   string
		forbid string
	}{
		{name: "malformed JSON", status: http.StatusOK, body: "{", want: "decode chat response"},
		{name: "non-success JSON body", status: http.StatusServiceUnavailable, body: `{"error":{"message":"provider detail must not be returned"}}`, want: "provider returned status 503", forbid: "provider detail"},
		{name: "non-success malformed body", status: http.StatusBadGateway, body: `{secret-provider-detail`, want: "provider returned status 502", forbid: "secret-provider-detail"},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", maxProviderResponseBytes+1), want: "response exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			_, err := NewClient(testProfile(server.URL, "test-key")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
			if err == nil || !strings.Contains(err.Error(), test.want) || (test.forbid != "" && strings.Contains(err.Error(), test.forbid)) || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("error = %v, want %q without provider body or API key", err, test.want)
			}
		})
	}
}

func TestChatRejectsProviderRedirectsBeforeASecondDestinationReceivesPrompt(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		status   int
		location string
	}{
		{name: "301", status: http.StatusMovedPermanently, location: "redirect"},
		{name: "302", status: http.StatusFound, location: "redirect"},
		{name: "303", status: http.StatusSeeOther, location: "redirect"},
		{name: "307", status: http.StatusTemporaryRedirect, location: "redirect"},
		{name: "308", status: http.StatusPermanentRedirect, location: "redirect"},
		{name: "malformed Location", status: http.StatusTemporaryRedirect, location: "http://[::1/?provider_secret=must-not-appear"},
		{name: "missing Location", status: http.StatusPermanentRedirect},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var initialRequests, redirectedRequests atomic.Int32
			var redirectedAuthorization, redirectedBody string
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectedRequests.Add(1)
				redirectedAuthorization = r.Header.Get("Authorization")
				data, _ := io.ReadAll(r.Body)
				redirectedBody = string(data)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"second destination body"}`)
			}))
			defer second.Close()

			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				initialRequests.Add(1)
				if fixture.location == "redirect" {
					w.Header().Set("Location", second.URL+"/capture?provider_secret=must-not-appear")
				} else if fixture.location != "" {
					w.Header().Set("Location", fixture.location)
				}
				w.WriteHeader(fixture.status)
				_, _ = io.WriteString(w, `{"error":"first destination body"}`)
			}))
			defer first.Close()

			_, err := NewClient(testProfile(first.URL, "redirect-test-key")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "prompt content must stay at the first destination"}})
			if !errors.Is(err, ErrRedirectRejected) || !strings.Contains(err.Error(), "status "+strconv.Itoa(fixture.status)) {
				t.Fatalf("redirect error = %v", err)
			}
			if initialRequests.Load() != 1 || redirectedRequests.Load() != 0 {
				t.Fatalf("requests first=%d second=%d, want one first request and no second request", initialRequests.Load(), redirectedRequests.Load())
			}
			for _, forbidden := range []string{"provider_secret", "first destination body", "second destination body", "redirect-test-key"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("redirect error exposed %q: %v", forbidden, err)
				}
			}
			if redirectedAuthorization != "" || redirectedBody != "" {
				t.Fatalf("second destination received authorization=%q body=%q", redirectedAuthorization, redirectedBody)
			}
		})
	}
}

func TestChatPropagatesCancellationAndRejectsMissingContent(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			_ = json.NewEncoder(w).Encode(ChatResponse{Model: "fixture", Choices: []ChatChoice{}})
			return
		}
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(testProfile(server.URL, "")).Chat(ctx, []ChatMessage{{Role: "user", Content: "hello"}})
		done <- err
	}()
	waitForTestSignal(t, started, "provider request")
	cancel()
	if err := waitForTestError(t, done, "canceled chat request"); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	_, err := NewClient(testProfile(server.URL, "")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if err == nil || !strings.Contains(err.Error(), "chat response has no content") {
		t.Fatalf("missing content error = %v", err)
	}
}

func TestClientPreservesConfiguredCompatibilityPrefixAndRequestContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		basePath string
	}{
		{name: "OpenAI API base", basePath: "/v1"},
		{name: "trailing slash", basePath: "/v1/"},
		{name: "Gemini compatibility API base", basePath: "/v1beta/openai"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if want := strings.TrimRight(test.basePath, "/") + "/chat/completions"; r.URL.Path != want {
					t.Fatalf("path = %q, want %q", r.URL.Path, want)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Fatalf("Authorization = %q", got)
				}
				var request ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				if request.Model != "compatible-model" || request.Temperature != 0.2 || request.MaxTokens != 1234 || request.ReasoningEffort != "" || request.TopP != nil || request.TopK != nil || request.MinP != nil || request.PresencePenalty != nil || request.RepeatPenalty != nil || request.ResponseFormat != nil || len(request.Messages) != 1 {
					t.Fatalf("request = %+v", request)
				}
				_ = json.NewEncoder(w).Encode(ChatResponse{Model: "compatible-model", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: "ok"}}}})
			}))
			defer server.Close()

			profile := testProfile(server.URL+test.basePath, "test-key")
			profile.Model, profile.Temperature, profile.MaxTokens = "compatible-model", 0.2, 1234
			if _, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
				t.Fatalf("Chat() error = %v", err)
			}
		})
	}
}

func TestClientSendsConfiguredTemperatureIncludingZero(t *testing.T) {
	for _, test := range []struct {
		name        string
		temperature float32
		newClient   func(config.ModelProfile) *Client
	}{
		{name: "standard client zero", temperature: 0, newClient: NewClient},
		{name: "evaluation client zero", temperature: 0, newClient: NewEvaluationClient},
		{name: "standard client nonzero", temperature: 0.2, newClient: NewClient},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := testProfile("https://provider.example/v1", "")
			profile.Temperature = test.temperature
			client := test.newClient(profile)
			client.transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				rawTemperature, ok := request["temperature"]
				if !ok {
					t.Fatal("request omitted temperature")
				}
				var temperature float32
				if err := json.Unmarshal(rawTemperature, &temperature); err != nil {
					t.Fatalf("decode temperature: %v", err)
				}
				if temperature != test.temperature {
					t.Fatalf("temperature = %v, want %v", temperature, test.temperature)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"model":"fixture","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)),
					Request:    r,
				}, nil
			})
			if _, err := client.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
				t.Fatalf("Chat() error = %v", err)
			}
		})
	}
}

func TestChatWithJSONSchemaSerializesStrictFormatAndEscapedSchema(t *testing.T) {
	schema := JSONSchema{Name: "file_analysis", Schema: json.RawMessage(`{"type":"object","properties":{"note":{"type":"string","description":"quoted \\\"text\\\""}},"required":["note"],"additionalProperties":false}`)}
	client := NewClient(testProfile("https://provider.example/v1", ""))
	client.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var format ResponseFormat
		if err := json.Unmarshal(payload["response_format"], &format); err != nil {
			t.Fatalf("decode response_format: %v", err)
		}
		if format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict || format.JSONSchema.Name != schema.Name || string(format.JSONSchema.Schema) != string(schema.Schema) {
			t.Fatalf("response_format = %+v", format)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"model":"fixture","choices":[{"message":{"role":"assistant","content":"{}"}}]}`)), Request: request}, nil
	})
	if _, err := client.ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, schema); err != nil {
		t.Fatalf("ChatWithJSONSchema() error = %v", err)
	}
}

func TestChatWithJSONSchemaRejectsProviderValidationWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			client := NewClient(testProfile("https://provider.example/v1", ""))
			client.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				requests.Add(1)
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"unsupported format"}`)), Request: request}, nil
			})
			_, err := client.ChatWithJSONSchema(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, JSONSchema{Name: "answer", Schema: json.RawMessage(`{"type":"object"}`)})
			if !errors.Is(err, ErrStructuredRequestRejected) || requests.Load() != 1 || strings.Contains(err.Error(), "unsupported format") {
				t.Fatalf("schema rejection = %v, requests=%d", err, requests.Load())
			}
		})
	}

	var ordinaryRequests atomic.Int32
	ordinary := NewClient(testProfile("https://provider.example/v1", ""))
	ordinary.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		ordinaryRequests.Add(1)
		return &http.Response{StatusCode: http.StatusUnprocessableEntity, Body: io.NopCloser(strings.NewReader(`{"detail":"invalid request"}`)), Request: request}, nil
	})
	_, err := ordinary.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, ErrRequestRejected) || errors.Is(err, ErrStructuredRequestRejected) || ordinaryRequests.Load() != 1 || strings.Contains(err.Error(), "invalid request") {
		t.Fatalf("ordinary validation rejection = %v, requests=%d", err, ordinaryRequests.Load())
	}
}

func TestClientForwardsConfiguredReasoningEffort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ReasoningEffort != "high" {
			t.Fatalf("reasoning effort = %q, want high", request.ReasoningEffort)
		}
		_ = json.NewEncoder(w).Encode(ChatResponse{Model: "reasoning-model", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: "ok"}}}})
	}))
	defer server.Close()

	profile := testProfile(server.URL+"/v1", "")
	profile.Model, profile.ReasoningEffort = "reasoning-model", "high"
	if _, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestClientForwardsOptionalSamplingControlsIncludingZero(t *testing.T) {
	profile := testProfile("https://provider.example/v1", "")
	topP, minP, presencePenalty, repeatPenalty := float32(0.95), float32(0), float32(0), float32(1)
	topK := 20
	profile.TopP = &topP
	profile.TopK = &topK
	profile.MinP = &minP
	profile.PresencePenalty = &presencePenalty
	profile.RepeatPenalty = &repeatPenalty
	client := NewClient(profile)
	client.transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for field, want := range map[string]string{"top_p": "0.95", "top_k": "20", "min_p": "0", "presence_penalty": "0", "repeat_penalty": "1"} {
			if got := string(payload[field]); got != want {
				t.Fatalf("%s = %s, want %s", field, got, want)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"model":"fixture","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)), Request: request}, nil
	})
	if _, err := client.Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestEvaluationClientRetainsReasoningOnlyResponseWithoutNormalLogDisclosure(t *testing.T) {
	var output bytes.Buffer
	logging.Init(logging.Config{Level: "info", Format: "json", Output: &output})
	t.Cleanup(func() { logging.Init(logging.Config{Level: "info", Format: "json"}) })

	const reasoning = "private reasoning_content must stay out of ordinary logs"
	const alternateReasoning = "private reasoning must stay out of ordinary logs"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(ChatResponse{Model: "fixture", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: "final", ReasoningContent: reasoning, Reasoning: alternateReasoning}, FinishReason: "stop"}}, Usage: ChatUsage{TotalTokens: 7}})
	}))
	defer server.Close()

	ordinary, err := NewClient(testProfile(server.URL, "")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("ordinary Chat() error = %v", err)
	}
	if ordinary.Choices[0].Message.ReasoningContent != reasoning || ordinary.Choices[0].Message.Reasoning != alternateReasoning {
		t.Fatalf("ordinary Chat() did not retain separate reasoning fields: %+v", ordinary.Choices[0].Message)
	}
	if strings.Contains(output.String(), reasoning) || strings.Contains(output.String(), alternateReasoning) {
		t.Fatalf("ordinary completion log exposed reasoning: %s", output.String())
	}

	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(ChatResponse{Model: "fixture", Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", ReasoningContent: reasoning}, FinishReason: "length"}}, Usage: ChatUsage{CompletionTokens: 19, TotalTokens: 23}})
	})
	if _, err := NewClient(testProfile(server.URL, "")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}); !errors.Is(err, ErrUnusableResponse) {
		t.Fatalf("ordinary empty final error = %v", err)
	}
	response, err := NewEvaluationClient(testProfile(server.URL, "")).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}})
	if err != nil || response.Choices[0].Message.ReasoningContent != reasoning || response.Usage.CompletionTokens != 19 || response.Choices[0].FinishReason != "length" {
		t.Fatalf("evaluation response = %+v, error = %v", response, err)
	}
	if strings.Contains(output.String(), reasoning) {
		t.Fatalf("evaluation response exposed reasoning in ordinary logs: %s", output.String())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testProfile(apiBaseURL, apiKey string) config.ModelProfile {
	return config.ModelProfile{APIBaseURL: apiBaseURL, APIKey: apiKey, Model: "fixture", Temperature: 0.2, MaxTokens: 1234}
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForTestError(t *testing.T, done <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func TestOptionalFinalContentRequiresNormalCompletionAndRemainsScoped(t *testing.T) {
	for _, test := range []struct {
		name, content, finish, refusal string
		wantErr                        bool
	}{
		{"empty completed", "", "stop", "", false},
		{"blank completed", " \n", "stop", "", false},
		{"missing completion", "", "", "", true},
		{"truncated empty", "", "length", "", true},
		{"truncated findings", `{"findings":[]}`, "length", "", true},
		{"filtered", "", "content_filter", "", true},
		{"tool call", "", "tool_calls", "", true},
		{"refused", "", "stop", "Cannot review", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(ChatResponse{Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: test.content, Refusal: test.refusal}, FinishReason: test.finish}}})
			}))
			defer server.Close()
			client := NewClient(testProfile(server.URL, ""))
			messages := []ChatMessage{{Role: "user", Content: "Review"}}
			if _, err := client.WithOptionalFinalContent().Chat(context.Background(), messages); (err != nil) != test.wantErr {
				t.Fatalf("err=%v", err)
			}
			if test.content == "" {
				if _, err := client.Chat(context.Background(), messages); !errors.Is(err, ErrUnusableResponse) {
					t.Fatalf("ordinary empty output passed: %v", err)
				}
			}
		})
	}
}
