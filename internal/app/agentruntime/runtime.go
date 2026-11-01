// Package agentruntime isolates ADK execution from application authority and wire types.
package agentruntime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// Request is an admitted completion. The caller owns retries and scope checks.
type Request struct {
	Messages         []llm.ChatMessage
	Schema           *llm.JSONSchema
	Stream           bool
	OnText           func(string) error
	Investigation    *Investigation
	ContextMaxTokens int
}

// Result retains provider metadata without exposing ADK types to callers.
type Result struct {
	Content      string
	Model        string
	FinishReason string
	Usage        llm.ChatUsage
	UsageKnown   bool
}

// Complete runs an admitted invocation in a private ADK session. Completion
// requests make one provider attempt; investigations retain bounded tool turns.
// The transport owns sampling, schemas and CLI isolation; the caller owns retries.
func Complete(ctx context.Context, client CompletionClient, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, context.Cause(ctx)
	}
	if client == nil {
		return Result{}, fmt.Errorf("model client is not configured")
	}
	if len(request.Messages) == 0 || strings.TrimSpace(request.Messages[0].Content) == "" {
		return Result{}, fmt.Errorf("model request is required")
	}
	if _, supported := client.(generationClient); request.Stream && !supported {
		return Result{}, fmt.Errorf("%w: provider does not support streaming", llm.ErrRequestRejected)
	}
	request = snapshotRequest(request)
	if err := validateProviderCacheConfig(ctx, client); err != nil {
		return Result{}, err
	}
	var err error
	var completion agent.Agent
	if request.Investigation != nil {
		if parent, ok := ctx.Value(investigationKey{}).(*Investigation); ok && parent != request.Investigation {
			return Result{}, fmt.Errorf("%w: investigation scope cannot be replaced", ErrPolicyDenied)
		}
		ctx = context.WithValue(ctx, investigationKey{}, request.Investigation)
		completion, err = newInvestigationAgent(client, request)
	} else {
		completion, err = newCompletionAgent(client, request)
	}
	if err != nil {
		return Result{}, fmt.Errorf("create completion agent: %w", err)
	}
	result, err := runCompletion(ctx, completion, request, client)
	if errors.Is(err, agentstate.ErrPersistence) || errors.Is(err, ErrPolicyDenied) {
		CancelRun(ctx, err)
	}
	return result, err
}

func completionProviderBinding(client CompletionClient) string {
	if provider, ok := client.(*ProviderClient); ok {
		binding := provider.binding()
		return hex.EncodeToString(binding[:])
	}
	return ""
}

func runCompletion(ctx context.Context, completion agent.Agent, request Request, client CompletionClient) (result Result, runErr error) {
	const appName, userID = "kasuri", "local"
	invocation, err := createCompletionSession(ctx, request, completionProviderBinding(client))
	if err != nil {
		return Result{}, err
	}
	ctx = invocation.activate(ctx)
	defer func() {
		if err := invocation.finish(ctx, runErr); err != nil {
			result = Result{}
			runErr = errors.Join(runErr, err)
		}
	}()
	policy, err := newPolicyPlugin()
	if err != nil {
		return Result{}, fmt.Errorf("create runtime policy: %w", err)
	}
	artifacts, err := newArtifactPlugin()
	if err != nil {
		return Result{}, fmt.Errorf("create artifact recorder: %w", err)
	}
	compaction, err := completionCompaction(ctx, client, request)
	if err != nil {
		return Result{}, err
	}
	service := invocation.service
	if compaction != nil {
		service = compactionSessionService{service}
	}
	runtime, err := runner.New(runner.Config{AppName: appName, Agent: completion, SessionService: service, Compaction: compaction, ArtifactService: completionArtifacts(invocation), PluginConfig: runner.PluginConfig{Plugins: []*plugin.Plugin{artifacts, policy}}})
	if err != nil {
		return Result{}, fmt.Errorf("create completion runner: %w", err)
	}
	var input *genai.Content
	if request.Investigation != nil {
		input = genai.NewContentFromText(request.Messages[len(request.Messages)-1].Content, genai.RoleUser)
	}
	return collectCompletion(ctx, runtime.Run(ctx, userID, invocation.session.ID(), input, agent.RunConfig{}), request.OnText)
}

func collectCompletion(ctx context.Context, events iter.Seq2[*session.Event, error], onText func(string) error) (Result, error) {
	var result Result
	completed := false
	for event, err := range events {
		if err != nil {
			return Result{}, fmt.Errorf("model request failed: %w", err)
		}
		if event == nil || completed {
			return Result{}, fmt.Errorf("completion runner returned an invalid event")
		}
		if event.Partial {
			if err := consumePartialText(ctx, event, onText); err != nil {
				return Result{}, err
			}
			continue
		}
		if !event.TurnComplete || !event.IsFinalResponse() {
			if _, investigation := ctx.Value(investigationKey{}).(*Investigation); investigation && event.Content != nil && event.Output == nil {
				continue
			}
			return Result{}, fmt.Errorf("completion runner returned an invalid terminal event")
		}
		result, completed = event.Output.(Result)
		if !completed {
			return Result{}, fmt.Errorf("completion runner returned an invalid result")
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, context.Cause(ctx)
	}
	if !completed {
		return Result{}, fmt.Errorf("completion runner ended without a result")
	}
	return result, nil
}

func consumePartialText(ctx context.Context, event *session.Event, onText func(string) error) error {
	if event.Content == nil || event.TurnComplete || event.Output != nil {
		return fmt.Errorf("completion runner returned an invalid partial event")
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall != nil || part.FunctionResponse != nil || part.Thought {
			return fmt.Errorf("completion runner returned unsupported partial content")
		}
		if onText != nil {
			if err := onText(part.Text); err != nil {
				return fmt.Errorf("consume partial completion: %w", err)
			}
		}
		observeText(ctx, part.Text)
	}
	return nil
}

func snapshotRequest(request Request) Request {
	request.Messages = append([]llm.ChatMessage(nil), request.Messages...)
	if request.Schema != nil {
		schema := *request.Schema
		schema.Schema = append([]byte(nil), schema.Schema...)
		request.Schema = &schema
	}
	return request
}
