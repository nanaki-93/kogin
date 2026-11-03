package agentruntime

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func TestUsageAccountingDistinguishesFailedAndUnreportedCallsAcrossReopen(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			response := fixtureResponse("PRIVATE_OUTPUT")
			response.Usage.PromptTokensDetails = &llm.PromptTokenDetails{CachedTokens: 5}
			_ = json.NewEncoder(w).Encode(response)
		case 2:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"PRIVATE_OUTPUT"},"finish_reason":"stop"}]}`)
		default:
			http.Error(w, "PRIVATE_DIAGNOSTIC", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	ctx, budget, _ := persistedContext(t, root)
	profile := config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "PRIVATE_MODEL", APIKey: "PRIVATE_KEY", MaxTokens: 100}
	provider := NewProviderClient(profile)
	for i := range 3 {
		_, err := Complete(ctx, provider, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "PRIVATE_SOURCE"}}})
		if (err != nil) != (i == 2) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	usage := budget.Snapshot()
	if usage.Accounting == nil || len(usage.Accounting.Providers) != 1 {
		t.Fatalf("missing grouped accounting: %+v", usage)
	}
	group := usage.Accounting.Providers[0]
	if group.ModelKey != ProviderUsageKey(string(profile.Provider), profile.Model) || group.ModelCalls != 3 || group.FailedCalls != 1 || group.UnknownUsageCalls != 2 || group.ReportedCalls != 1 || group.ReportedInputTokens != 31 || group.ReportedOutputTokens != 7 || group.CachedInputTokens != 5 || group.CacheKnownCalls != 1 || group.ActiveCalls != 0 || group.OutputTokens != 207 {
		t.Fatalf("lost consumption or mislabeled successful unknown response: %+v", group)
	}
	reopened, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History(t.Context())
	if err != nil || len(history.Runs) != 3 {
		t.Fatalf("history=%+v %v", history, err)
	}
	for _, run := range history.Runs {
		if !reflect.DeepEqual(run.Usage.Accounting, usage.Accounting) || run.Usage.InputTokens != usage.InputTokens || run.Usage.OutputTokens != usage.OutputTokens {
			t.Fatal("retry history lost shared usage")
		}
	}
	data, err := json.Marshal(usage)
	if err != nil || strings.Contains(string(data), "PRIVATE_") || strings.Contains(string(data), server.URL) {
		t.Fatal("provider metadata escaped into source-free accounting", err)
	}
}

func TestGoogleUsageRetainsFailedAndTruncatedMeasurements(t *testing.T) {
	for _, mode := range []string{"complete", "failed-final", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			provider := googleFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				body := strings.Replace(googleTextResponse, `"totalTokenCount":8`, `"totalTokenCount":8,"cachedContentTokenCount":2`, 1)
				if mode == "failed-final" {
					body = strings.Replace(body, `"STOP"`, `"MAX_TOKENS"`, 1)
				}
				if mode == "truncated" {
					w.Header().Set("Content-Type", "text/event-stream")
					body = "data: " + strings.Replace(body, `,"finishReason":"STOP"`, "", 1) + "\n\n"
				}
				_, _ = io.WriteString(w, body)
			})
			ctx, budget := budgetContext(t, nil)
			request := googleRequest()
			request.Stream = mode == "truncated"
			result, err := Complete(ctx, provider, request)
			if (err == nil) != (mode == "complete") {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			group := budget.Snapshot().Accounting.Providers[0]
			if group.ReportedInputTokens != 5 || group.ReportedOutputTokens != 3 || group.CachedInputTokens != 2 || group.CacheKnownCalls != 1 {
				t.Fatalf("failed or partial receipt lost: %+v", group)
			}
			if mode == "truncated" {
				if group.UnknownUsageCalls != 1 || group.PartialUsageCalls != 1 || group.InputTokens <= 5 || group.OutputTokens != 100 || group.FailedCalls != 1 {
					t.Fatalf("partial receipt fabricated complete usage: %+v", group)
				}
			} else if group.UnknownUsageCalls != 0 || group.ReportedCalls != 1 || group.InputTokens != 5 || group.OutputTokens != 3 || (group.FailedCalls == 1) != (mode == "failed-final") {
				t.Fatalf("complete receipt misclassified: %+v", group)
			}
			if mode == "complete" && (result.Usage.PromptTokensDetails == nil || result.Usage.PromptTokensDetails.CachedTokens != 2) {
				t.Fatal("native result lost cache metadata")
			}
		})
	}
}

func TestUsageAccountingConcurrentSnapshotsAndInvalidReceipts(t *testing.T) {
	ctx, budget := budgetContext(t, nil)
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Go(func() {
			key := ProviderUsageKey("fixture", string(rune('a'+i%2)))
			reservation, err := budget.reserveModel(ctx, 20, 20, key)
			if err != nil {
				t.Error(err)
				return
			}
			if err := reservation.observeUsage(tokenReceipt{Input: 10, Output: 5, Cached: 11, CacheKnown: true}); !errors.Is(err, llm.ErrUnusableResponse) {
				t.Error("invalid cached receipt accepted", err)
			}
			if err := reservation.observeUsage(tokenReceipt{Input: 10, Output: 5, Cached: 2, CacheKnown: true, Final: true}); err != nil {
				t.Error(err)
			}
			if err := reservation.settle(10, 5, true, true); err != nil {
				t.Error(err)
			}
			if err := reservation.settle(100, 100, true, true); err != nil {
				t.Error(err)
			}
			snapshot := budget.Snapshot()
			snapshot.Accounting.Providers[0].ReportedInputTokens = -100
		})
	}
	workers.Wait()
	usage := budget.Snapshot()
	if usage.ModelCalls != 16 || usage.InputTokens != 160 || usage.OutputTokens != 80 || usage.ActiveCalls != 0 || len(usage.Accounting.Providers) != 2 {
		t.Fatalf("shared accounting=%+v", usage)
	}
	for _, group := range usage.Accounting.Providers {
		if group.ModelCalls != 8 || group.ReportedInputTokens != 80 || group.CachedInputTokens != 16 {
			t.Fatalf("snapshot alias or duplicate settlement: %+v", group)
		}
	}
}
