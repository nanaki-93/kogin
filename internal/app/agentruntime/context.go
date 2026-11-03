package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

type contextConfigKey struct{}
type contextAuthorityKey struct{}

func WithContextConfig(ctx context.Context, configured config.AgentContext) (context.Context, error) {
	resolved, err := config.ResolveAgentContext(configured)
	if err != nil {
		return nil, err
	}
	if previous, ok := ctx.Value(contextConfigKey{}).(config.AgentContext); ok && previous != resolved {
		return nil, fmt.Errorf("%w: admitted context configuration changed", ErrPolicyDenied)
	}
	return context.WithValue(ctx, contextConfigKey{}, resolved), nil
}

func contextConfig(ctx context.Context) config.AgentContext {
	value, _ := ctx.Value(contextConfigKey{}).(config.AgentContext)
	return value
}

// Authority is side-effect free. Attempt reservations must never be used here:
// one completion may perform a separately charged summary or cache operation.
func WithContextAuthority(ctx context.Context, authorize func(context.Context) error) context.Context {
	previous, _ := ctx.Value(contextAuthorityKey{}).(func(context.Context) error)
	guard := func(current context.Context) error {
		if previous != nil {
			if err := previous(current); err != nil {
				return err
			}
		}
		if authorize == nil {
			return fmt.Errorf("captured context authority is required")
		}
		return authorize(current)
	}
	return context.WithValue(ctx, contextAuthorityKey{}, guard)
}

func validateContextAuthority(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	guard, ok := ctx.Value(contextAuthorityKey{}).(func(context.Context) error)
	if !ok {
		return nil
	}
	if err := guard(ctx); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return fmt.Errorf("%w: captured context changed: %w", ErrPolicyDenied, err)
	}
	return nil
}

func requireContextAuthority(ctx context.Context) error {
	_, captured := ctx.Value(contextAuthorityKey{}).(func(context.Context) error)
	_, investigation := ctx.Value(investigationKey{}).(*Investigation)
	if !captured && !investigation {
		return fmt.Errorf("%w: context optimization requires captured source authority", ErrPolicyDenied)
	}
	if err := validateContextAuthority(ctx); err != nil {
		return err
	}
	return authorizeInvestigation(ctx)
}

func contextStorageError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil && (errors.Is(err, ctx.Err()) || errors.Is(err, cause)) {
		return cause
	}
	return fmt.Errorf("%w: context cache: %w", agentstate.ErrPersistence, err)
}

func contextKey(ctx context.Context, kind string, input any) (*persistence, agentstate.ContextKey, error) {
	p, _ := ctx.Value(persistenceKey{}).(*persistence)
	if p == nil {
		return nil, agentstate.ContextKey{}, fmt.Errorf("%w: context optimization requires durable admission", ErrPolicyDenied)
	}
	hash, err := fingerprint(input)
	if err != nil {
		return nil, agentstate.ContextKey{}, err
	}
	return p, agentstate.ContextKey{Binding: p.binding, Kind: kind, Fingerprint: hash}, nil
}

// CachedContext reuses deterministic composition only after current authority
// is established. The caller supplies all source/instruction/schema inputs.
func CachedContext(ctx context.Context, input any, build func() (json.RawMessage, error)) (json.RawMessage, error) {
	configuration := contextConfig(ctx)
	if !configuration.LocalCache {
		return build()
	}
	if err := requireContextAuthority(ctx); err != nil {
		return nil, err
	}
	p, key, err := contextKey(ctx, "composition", input)
	if err != nil {
		return nil, err
	}
	record, err := p.store.Context(ctx, key)
	if err != nil && !errors.Is(err, agentstate.ErrNotFound) {
		return nil, contextStorageError(ctx, err)
	}
	if err == nil && record.State == "ready" && record.ExpiresAt.After(time.Now()) {
		if err := requireContextAuthority(ctx); err != nil {
			return nil, err
		}
		return record.Value, nil
	}
	value, err := build()
	if err != nil {
		return nil, err
	}
	expires := time.Now().UTC().Add(time.Duration(configuration.CacheTTLSeconds) * time.Second)
	err = p.store.SaveContext(ctx, key, "", value, expires, func() error { return requireContextAuthority(ctx) })
	if err != nil {
		return nil, contextStorageError(ctx, err)
	}
	return value, nil
}
