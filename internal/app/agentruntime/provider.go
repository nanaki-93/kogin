package agentruntime

import (
	"context"
	"fmt"
	"iter"
	"net/http"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// ProviderClient keeps provider selection immutable and exposes the existing
// project-analysis contract without leaking SDK types out of this package.
type ProviderClient struct {
	profile    config.ModelProfile
	compatible *llm.Client
	transport  http.RoundTripper
	optional   bool
}

func NewProviderClient(profile config.ModelProfile) *ProviderClient {
	client := &ProviderClient{profile: profile, transport: http.DefaultTransport}
	if !profile.IsGoogle() {
		client.compatible = llm.NewClient(profile)
	}
	return client
}

func (c *ProviderClient) ModelName() string { return c.profile.Model }

func (c *ProviderClient) WithOptionalFinalContent() *ProviderClient {
	copy := *c
	copy.optional = true
	if copy.compatible != nil {
		copy.compatible = copy.compatible.WithOptionalFinalContent()
	}
	return &copy
}

func (c *ProviderClient) Chat(ctx context.Context, messages []llm.ChatMessage) (*llm.ChatResponse, error) {
	if c.compatible != nil {
		return c.compatibleChat(ctx, Request{Messages: messages})
	}
	return c.chat(ctx, Request{Messages: messages})
}

func (c *ProviderClient) ChatWithJSONSchema(ctx context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	if c.compatible != nil {
		return c.compatibleChat(ctx, Request{Messages: messages, Schema: &schema})
	}
	return c.chat(ctx, Request{Messages: messages, Schema: &schema})
}

func (c *ProviderClient) chat(ctx context.Context, request Request) (*llm.ChatResponse, error) {
	result, err := Complete(ctx, c, request)
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Model: result.Model, Usage: result.Usage, Choices: []llm.ChatChoice{{
		Message: llm.ChatMessage{Role: "assistant", Content: result.Content}, FinishReason: result.FinishReason,
	}}}, nil
}

// Generate is the compatibility protocol. Google agents use native model parts
// so their thought signatures survive tool turns without an intermediate wire format.
func (c *ProviderClient) Generate(ctx context.Context, request llm.GenerationRequest) iter.Seq2[llm.GenerationEvent, error] {
	if c.compatible != nil {
		return c.generateCompatible(ctx, request)
	}
	return func(yield func(llm.GenerationEvent, error) bool) {
		yield(llm.GenerationEvent{}, fmt.Errorf("%w: Google models require the native ADK protocol", llm.ErrRequestRejected))
	}
}

func (c *ProviderClient) compatibleChat(ctx context.Context, request Request) (*llm.ChatResponse, error) {
	reservation, err := c.reserveDispatch(ctx, struct {
		Messages []llm.ChatMessage
		Schema   *llm.JSONSchema
	}{request.Messages, request.Schema})
	if err != nil {
		return nil, err
	}
	defer reservation.settle(0, 0, false, false)
	var response *llm.ChatResponse
	if request.Schema == nil {
		response, err = c.compatible.Chat(ctx, request.Messages)
	} else {
		response, err = c.compatible.ChatWithJSONSchema(ctx, request.Messages, *request.Schema)
	}
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("%w: missing completion", llm.ErrUnusableResponse)
	}
	// The legacy import DTO cannot distinguish missing usage from zero usage.
	known := response.Usage != (llm.ChatUsage{})
	if err := settleChatUsage(reservation, &response.Usage, known); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *ProviderClient) generateCompatible(ctx context.Context, request llm.GenerationRequest) iter.Seq2[llm.GenerationEvent, error] {
	return func(yield func(llm.GenerationEvent, error) bool) {
		reservation, err := c.reserveDispatch(ctx, request)
		if err != nil {
			yield(llm.GenerationEvent{}, err)
			return
		}
		defer reservation.settle(0, 0, false, false)
		for event, err := range c.compatible.Generate(ctx, request) {
			if err == nil && event.Result != nil {
				err = settleChatUsage(reservation, event.Result.Usage, event.Result.Usage != nil)
			}
			if err != nil {
				yield(llm.GenerationEvent{}, err)
				return
			}
			if !yield(event, nil) {
				return
			}
		}
	}
}

func settleChatUsage(reservation *modelReservation, usage *llm.ChatUsage, known bool) error {
	if !known {
		return reservation.settle(0, 0, false, true)
	}
	receipt, err := chatTokenReceipt(usage)
	if err != nil {
		return err
	}
	if err := reservation.observeUsage(receipt); err != nil {
		return err
	}
	return reservation.settle(receipt.Input, receipt.Output, true, true)
}
