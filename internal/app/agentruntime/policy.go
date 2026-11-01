package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

var ErrPolicyDenied = errors.New("agent dispatch is outside the admitted policy")

type policyKey struct{}
type dispatchPolicy map[[32]byte]struct{}

// WithPolicy freezes the admitted destinations. Children may narrow this set,
// but cannot add a destination to an existing admission.
func WithPolicy(ctx context.Context, clients ...*ProviderClient) (context.Context, error) {
	policy := make(dispatchPolicy, len(clients))
	for _, client := range clients {
		if client == nil {
			return nil, fmt.Errorf("%w: missing provider", ErrPolicyDenied)
		}
		if err := client.checkPolicy(ctx); err != nil {
			return nil, err
		}
		policy[client.binding()] = struct{}{}
	}
	return context.WithValue(ctx, policyKey{}, policy), nil
}

func (c *ProviderClient) binding() [32]byte {
	// Length-prefixed JSON avoids ambiguous concatenation. Credentials participate
	// in the in-memory identity; neither the input nor digest is logged or exposed.
	p := c.profile
	data, _ := json.Marshal([]string{string(p.Provider), p.APIBaseURL, p.Model, p.CLIPath, p.VertexProject, p.VertexLocation, p.APIKey, p.AccessToken})
	return sha256.Sum256(data)
}

func (c *ProviderClient) checkPolicy(ctx context.Context) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if policy, present := ctx.Value(policyKey{}).(dispatchPolicy); present {
		if _, allowed := policy[c.binding()]; !allowed {
			return ErrPolicyDenied
		}
	}
	if err := validateContextAuthority(ctx); err != nil {
		return err
	}
	return authorizeInvestigation(ctx)
}

func (c *ProviderClient) reserveDispatch(ctx context.Context, request any) (*modelReservation, error) {
	if err := c.checkPolicy(ctx); err != nil {
		return nil, err
	}
	budget := BudgetFromContext(ctx)
	if budget == nil {
		return nil, nil
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode model reservation: %w", err)
	}
	output := int64(c.profile.MaxTokens)
	if c.profile.IsCLI() {
		output = 4 << 20
	} // CLI has a bounded response, but no enforceable token setting.
	key := ProviderUsageKey(string(c.profile.Provider), c.profile.Model)
	return budget.reserveModel(ctx, int64(len(data))+256, output, key)
}

func newPolicyPlugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:              "kasuri_policy",
		BeforeRunCallback: func(ctx agent.InvocationContext) (*genai.Content, error) { return nil, context.Cause(ctx) },
		BeforeModelCallback: func(ctx agent.Context, _ *model.LLMRequest) (*model.LLMResponse, error) {
			return nil, context.Cause(ctx)
		},
		BeforeToolCallback: func(ctx agent.Context, candidate tool.Tool, _ map[string]any) (map[string]any, error) {
			// Actual dispatch validates again and charges the call exactly once.
			return nil, checkToolGrant(ctx, candidate)
		},
		OnEventCallback: observeBudget,
	})
}

func observeBudget(ctx agent.InvocationContext, event *session.Event) (*session.Event, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if budget := BudgetFromContext(ctx); budget != nil && event != nil {
		copy := *event
		copy.CustomMetadata = maps.Clone(event.CustomMetadata)
		if copy.CustomMetadata == nil {
			copy.CustomMetadata = make(map[string]any)
		}
		copy.CustomMetadata["mini_orca.budget"] = budget.Snapshot()
		return &copy, nil
	}
	return event, nil
}
