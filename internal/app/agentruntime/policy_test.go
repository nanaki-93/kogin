package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func budgetContext(t *testing.T, alter func(*config.AgentLimits)) (context.Context, *Budget) {
	t.Helper()
	resolved, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if alter != nil {
		alter(&resolved.Limits)
	}
	ctx, cancel, err := WithBudget(context.Background(), resolved.Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	return ctx, BudgetFromContext(ctx)
}

func TestCanceledChildReservationsDoNotCancelSharedRun(t *testing.T) {
	for _, kind := range []string{"model", "tool", "repair"} {
		t.Run(kind, func(t *testing.T) {
			parent, budget, _ := persistedContext(t, t.TempDir())
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			persist := budget.persist
			budget.persist = func(ctx context.Context, usage BudgetSnapshot, span *agentstate.TraceSpan) (bool, error) {
				cancel()
				return persist(ctx, usage, span)
			}
			var err error
			switch kind {
			case "model":
				_, err = budget.reserveModel(ctx, 1, 1)
			case "tool":
				err = budget.AdmitTool(ctx)
			case "repair":
				err = budget.AdmitRepair(ctx)
			}
			budget.persist = persist
			if !errors.Is(err, context.Canceled) || parent.Err() != nil || budget.Snapshot().ActiveCalls != 0 {
				t.Fatalf("canceled reservation poisoned the shared run: %v parent=%v", err, parent.Err())
			}
			reservation, err := budget.reserveModel(parent, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := reservation.settle(1, 1, true, true); err != nil {
				t.Fatal(err)
			}
			got := budget.Snapshot()
			if got.ModelCalls != 1 || got.ToolCalls != 0 || got.Repairs != 0 || len(got.Accounting.Providers) != 1 || got.Accounting.Providers[0].ModelCalls != 1 {
				t.Fatalf("canceled child reservation leaked into accounting: %+v", got)
			}
		})
	}
}

func TestProviderDispatchRechecksInvestigationWithoutSDKHooks(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(fixtureResponse("unexpected"))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	changed := errors.New("source changed")
	inv := fixtureInvestigation(t, nil, func(context.Context) error { return changed })
	ctx, _ := budgetContext(t, nil)
	ctx = context.WithValue(ctx, investigationKey{}, inv)
	_, err := client.Chat(ctx, []llm.ChatMessage{{Role: "user", Content: "admitted earlier"}})
	if !errors.Is(err, changed) || !errors.Is(err, ErrPolicyDenied) || calls.Load() != 0 {
		t.Fatalf("dispatch bypassed authority: calls=%d error=%v", calls.Load(), err)
	}
}

func TestBudgetReservationsShareCapacityAndSettleOnce(t *testing.T) {
	ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.InputTokens = 100; l.OutputTokens = 100; l.ModelCalls = 2 })
	first, err := budget.reserveModel(ctx, 60, 70)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.settle(10, 15, true, true); err != nil {
		t.Fatal(err)
	}
	if err := first.settle(90, 90, true, true); err != nil {
		t.Fatal(err)
	}
	child, stop, err := WithBudget(ctx, config.AgentLimits{})
	if err != nil || BudgetFromContext(child) != budget {
		t.Fatal("child refreshed budget", err)
	}
	stop()
	second, err := budget.reserveModel(child, 80, 80)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.settle(0, 0, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.reserveModel(ctx, 1, 1); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("call cap error=%v", err)
	}
	got := budget.Snapshot()
	if got.ModelCalls != 2 || got.InputTokens != 90 || got.OutputTokens != 95 || got.UnknownUsageCalls != 1 || got.ActiveCalls != 0 || got.ReservedInputTokens != 0 || got.ReservedOutputTokens != 0 {
		t.Fatalf("accounting=%+v", got)
	}
}

func TestBudgetReservationsPreventConcurrentOverspending(t *testing.T) {
	ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.OutputTokens = 100 })
	first, err := budget.reserveModel(ctx, 1, 70)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := budget.reserveModel(ctx, 1, 40); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("unreserved concurrent request admitted: %v", err)
	}
	if !errors.Is(context.Cause(ctx), ErrBudgetExceeded) {
		t.Fatal("budget did not cancel its children")
	}
	if err := first.settle(1, 70, true, true); err != nil {
		t.Fatal(err)
	}
	if budget.Snapshot().ModelCalls != 1 {
		t.Fatal("rejected dispatch was counted as a call")
	}
}

func TestBudgetConcurrencyWaitHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.ConcurrentCalls = 1 })
		first, err := budget.reserveModel(ctx, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		child, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { _, err := budget.reserveModel(child, 1, 1); done <- err }()
		synctest.Wait()
		if budget.Snapshot().ActiveCalls != 1 {
			t.Fatal("concurrency cap bypassed")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal("child cancellation canceled the entire run")
		}
		if err := first.settle(1, 1, true, true); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBudgetElapsedToolsRepairsAndUsageOverrun(t *testing.T) {
	t.Run("elapsed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, _ := budgetContext(t, func(l *config.AgentLimits) { l.ElapsedSeconds = 1 })
			time.Sleep(time.Second)
			synctest.Wait()
			if !errors.Is(context.Cause(ctx), ErrBudgetExceeded) {
				t.Fatal("elapsed limit not enforced")
			}
		})
	})
	for _, kind := range []string{"tool", "repair", "usage"} {
		t.Run(kind, func(t *testing.T) {
			ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.ToolCalls = 1; l.Repairs = 0; l.OutputTokens = 10 })
			var err error
			switch kind {
			case "tool":
				if err := budget.AdmitTool(ctx); err != nil {
					t.Fatal(err)
				}
				err = budget.AdmitTool(ctx)
			case "repair":
				err = budget.AdmitRepair(ctx)
			case "usage":
				reservation, reserveErr := budget.reserveModel(ctx, 1, 5)
				if reserveErr != nil {
					t.Fatal(reserveErr)
				}
				err = reservation.settle(1, 11, true, true)
			}
			if !errors.Is(err, ErrBudgetExceeded) || !errors.Is(context.Cause(ctx), ErrBudgetExceeded) {
				t.Fatalf("limit not enforced: %v", err)
			}
		})
	}
}

func TestProviderPolicyAndBudgetGuardActualDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(fixtureResponse("done"))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture-model", MaxTokens: 10})
	ctx, budget := budgetContext(t, func(l *config.AgentLimits) { l.ModelCalls = 1 })
	ctx, err := WithPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	other := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "other", MaxTokens: 10})
	request := Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}}
	if _, err := WithPolicy(ctx, other); !errors.Is(err, ErrPolicyDenied) {
		t.Fatal("child expanded policy")
	}
	if _, err := Complete(ctx, other, request); !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("unadmitted destination accepted: %v", err)
	}
	if !errors.Is(context.Cause(ctx), ErrPolicyDenied) || calls.Load() != 0 || budget.Snapshot().ModelCalls != 0 {
		t.Fatal("policy failure did not stop the run before dispatch")
	}
	ctx, budget = budgetContext(t, func(l *config.AgentLimits) { l.ModelCalls = 1 })
	ctx, err = WithPolicy(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Complete(ctx, client, request)
	if err != nil || !result.UsageKnown || calls.Load() != 1 {
		t.Fatalf("result=%+v calls=%d error=%v", result, calls.Load(), err)
	}
	if _, err := client.Chat(ctx, request.Messages); !errors.Is(err, ErrBudgetExceeded) || calls.Load() != 1 {
		t.Fatalf("legacy path bypassed shared budget: %v calls=%d", err, calls.Load())
	}
	if budget.Snapshot().OutputTokens != 7 {
		t.Fatal("reported usage not settled")
	}
}

func TestProviderMissingUsageRetainsReservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"fixture","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := NewProviderClient(config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, Model: "fixture", MaxTokens: 100})
	ctx, budget := budgetContext(t, nil)
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}})
	if err != nil || result.UsageKnown {
		t.Fatalf("missing usage mislabeled: %+v %v", result, err)
	}
	usage := budget.Snapshot()
	if usage.UnknownUsageCalls != 1 || usage.OutputTokens != 100 || usage.InputTokens < 256 || usage.ActiveCalls != 0 {
		t.Fatalf("unreported usage not reserved: %+v", usage)
	}
}
