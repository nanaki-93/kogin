package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func configuredContext(t *testing.T, ctx context.Context, settings config.AgentContext) context.Context {
	t.Helper()
	ctx, err := WithContextConfig(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	return WithContextAuthority(ctx, func(context.Context) error { return nil })
}

func TestContextCompositionReuseRevalidatesAuthority(t *testing.T) {
	ctx, budget, _ := persistedContext(t, t.TempDir())
	ctx = configuredContext(t, ctx, config.AgentContext{LocalCache: true})
	calls := 0
	build := func() (json.RawMessage, error) { calls++; return []byte(`{"text":"captured"}`), nil }
	for range 2 {
		data, err := CachedContext(ctx, []string{"source", "instructions", "schema"}, build)
		if err != nil || string(data) != `{"text":"captured"}` {
			t.Fatalf("reuse=%s %v", data, err)
		}
	}
	if calls != 1 || budget.Snapshot().ModelCalls != 0 {
		t.Fatal("deterministic reuse rebuilt or dispatched")
	}
	if _, err := CachedContext(ctx, []string{"new source", "instructions", "schema"}, build); err != nil || calls != 2 {
		t.Fatal("changed identity reused", err)
	}
	denied := WithContextAuthority(ctx, func(context.Context) error { return errors.New("source changed") })
	if _, err := CachedContext(denied, []string{"source", "instructions", "schema"}, build); !errors.Is(err, ErrPolicyDenied) || calls != 2 {
		t.Fatal("stale hit escaped authority", err)
	}
	if _, err := WithContextConfig(ctx, config.AgentContext{}); !errors.Is(err, ErrPolicyDenied) {
		t.Fatal("admitted configuration replaced", err)
	}
}

func TestContextChildCancellationDoesNotCancelParentBudget(t *testing.T) {
	parent, _, _ := persistedContext(t, t.TempDir())
	child, cancel := context.WithCancel(parent)
	child = configuredContext(t, child, config.AgentContext{LocalCache: true})
	child = WithContextAuthority(child, func(ctx context.Context) error { cancel(); return context.Cause(ctx) })
	_, err := CachedContext(child, "identity", func() (json.RawMessage, error) { t.Fatal("canceled build"); return nil, nil })
	if !errors.Is(err, context.Canceled) || context.Cause(parent) != nil {
		t.Fatalf("child=%v parent=%v", err, context.Cause(parent))
	}
}
