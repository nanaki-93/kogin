package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

type googleModel struct {
	client *ProviderClient
}

var _ model.LLM = (*googleModel)(nil)

func (m *googleModel) Name() string { return m.client.ModelName() }

func (m *googleModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		ctx, cancel := nativeRequestContext(ctx)
		defer cancel()
		input, err := m.prepare(request)
		if err != nil {
			yield(nil, err)
			return
		}
		fullInput := input
		input, err = m.cacheCompletionInput(ctx, input)
		if err != nil {
			yield(nil, err)
			return
		}
		// Cached tokens still occupy model context and count against the local
		// run budget. Reserve the complete request, including its cached prefix.
		reservation, err := m.client.reserveDispatch(ctx, fullInput)
		if err != nil {
			yield(nil, err)
			return
		}
		defer reservation.settle(0, 0, false, false)
		sdk, err := m.createSDK(ctx)
		if err != nil {
			yield(nil, fmt.Errorf("%w: cannot initialize the configured Google backend", llm.ErrRequestRejected))
			return
		}
		completed := false
		modelVersion := ""
		for response, err := range sdk.GenerateContent(ctx, input, stream) {
			if err := googleResponseError(ctx, reservation, response, err, input.Config.ResponseJsonSchema != nil); err != nil {
				yield(nil, err)
				return
			}
			if err := validateGoogleEvent(response, completed, &modelVersion); err != nil {
				yield(nil, err)
				return
			}
			if !response.Partial {
				response.TurnComplete = true
				if err := settleGoogleUsage(reservation, response.UsageMetadata); err != nil {
					yield(nil, err)
					return
				}
				completed = true
			}
			if !yield(response, nil) {
				return
			}
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if !completed {
			yield(nil, fmt.Errorf("%w: native generation ended without a complete result", llm.ErrUnusableResponse))
		}
	}
}

func (m *googleModel) cacheCompletionInput(ctx context.Context, input *model.LLMRequest) (*model.LLMRequest, error) {
	return m.cachedRequest(ctx, input)
}

func googleResponseError(ctx context.Context, reservation *modelReservation, response *model.LLMResponse, providerErr error, structured bool) error {
	if err := observeGoogleUsage(reservation, response); err != nil {
		return err
	}
	if providerErr != nil {
		return nativeProviderError(ctx, providerErr, structured)
	}
	return nil
}

func validateGoogleEvent(response *model.LLMResponse, completed bool, version *string) error {
	if response == nil || completed {
		return fmt.Errorf("%w: invalid native response sequence", llm.ErrUnusableResponse)
	}
	if err := retainGoogleModelVersion(response, version); err != nil {
		return err
	}
	if !response.Partial {
		return validateGoogleFinal(response)
	}
	return nil
}

func validateGoogleFinal(response *model.LLMResponse) error {
	if response.ErrorCode != "" || response.FinishReason != genai.FinishReasonStop {
		return fmt.Errorf("%w: native generation did not finish normally", llm.ErrUnusableResponse)
	}
	if response.Content == nil {
		response.Content = genai.NewContentFromText("", genai.RoleModel)
	}
	return nil
}

func retainGoogleModelVersion(response *model.LLMResponse, version *string) error {
	if *version != "" && response.ModelVersion != "" && *version != response.ModelVersion {
		return fmt.Errorf("%w: native response changed model identity", llm.ErrUnusableResponse)
	}
	if response.ModelVersion != "" {
		*version = response.ModelVersion
	}
	response.ModelVersion = *version
	return nil
}

func nativeRequestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, bounded := ctx.Deadline(); bounded {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 5*time.Minute)
}

func (m *googleModel) createSDK(ctx context.Context) (model.LLM, error) {
	configuration, err := m.sdkConfig()
	if err != nil {
		return nil, err
	}
	return gemini.NewModel(ctx, m.client.profile.Model, configuration)
}

func (m *googleModel) sdkConfig() (*genai.ClientConfig, error) {
	profile := m.client.profile
	if profile.APIKey == "" && profile.AccessToken == "" {
		return nil, fmt.Errorf("explicit native credentials are required")
	}
	base, err := url.Parse(profile.APIBaseURL)
	if err != nil || base.Host == "" || base.Scheme == "" {
		return nil, fmt.Errorf("invalid endpoint")
	}
	transport := &geminiTransport{profile: profile, base: base, transport: m.client.transport}
	backend, version := genai.BackendGeminiAPI, "v1beta"
	if profile.Provider == config.VertexProvider {
		backend, version = genai.BackendVertexAI, "v1"
	}
	attempts := int32(1)
	return &genai.ClientConfig{
		Backend: backend, APIKey: profile.APIKey, Project: profile.VertexProject, Location: profile.VertexLocation,
		HTTPClient:  &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return llm.ErrRedirectRejected }},
		HTTPOptions: genai.HTTPOptions{BaseURL: profile.APIBaseURL, APIVersion: version, RetryOptions: &genai.HTTPRetryOptions{Attempts: &attempts}},
	}, nil
}

func (m *googleModel) prepare(request *model.LLMRequest) (*model.LLMRequest, error) {
	if request == nil || request.Model != "" && request.Model != m.Name() {
		return nil, fmt.Errorf("%w: native routing must use the admitted profile", llm.ErrRequestRejected)
	}
	input := *request
	if err := validateNativeContent(request.Contents); err != nil {
		return nil, err
	}
	configuration, err := googleGenerationConfig(m.client.profile, request.Config)
	if err != nil {
		return nil, err
	}
	input.Config = configuration
	data, err := json.Marshal(struct {
		Contents []*genai.Content
		Config   *genai.GenerateContentConfig
	}{input.Contents, input.Config})
	if err != nil || len(data) > nativeProviderByteLimit {
		return nil, fmt.Errorf("%w: invalid or oversized native request", llm.ErrRequestRejected)
	}
	return &input, nil
}

func googleGenerationConfig(profile config.ModelProfile, configured *genai.GenerateContentConfig) (*genai.GenerateContentConfig, error) {
	configuration := &genai.GenerateContentConfig{}
	if configured != nil {
		if err := onlyModelFields(configured, "systemInstruction", "tools", "responseMimeType", "responseJsonSchema"); err != nil {
			return nil, err
		}
		*configuration = *configured
	}
	if configuration.ResponseMIMEType != "" && (configuration.ResponseMIMEType != "application/json" || configuration.ResponseJsonSchema == nil) {
		return nil, fmt.Errorf("%w: native structured output requires a JSON schema", llm.ErrRequestRejected)
	}
	if _, err := generationTools(configuration.Tools); err != nil {
		return nil, err
	}
	if configuration.SystemInstruction != nil {
		if err := validateNativeContent([]*genai.Content{configuration.SystemInstruction}); err != nil {
			return nil, err
		}
	}
	configuration.Temperature, configuration.TopP, configuration.PresencePenalty = &profile.Temperature, profile.TopP, profile.PresencePenalty
	configuration.CandidateCount, configuration.MaxOutputTokens = 1, int32(profile.MaxTokens)
	if profile.TopK != nil {
		value := float32(*profile.TopK)
		configuration.TopK = &value
	}
	if profile.ReasoningEffort == "none" {
		zero := int32(0)
		configuration.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: &zero}
	} else if profile.ReasoningEffort != "" {
		configuration.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevel(strings.ToUpper(profile.ReasoningEffort))}
	}
	return configuration, nil
}

func validateNativeContent(contents []*genai.Content) error {
	if len(contents) == 0 {
		return fmt.Errorf("%w: native request requires content", llm.ErrRequestRejected)
	}
	for _, content := range contents {
		if content == nil {
			return fmt.Errorf("%w: missing native content", llm.ErrRequestRejected)
		}
		for _, part := range content.Parts {
			if part == nil || onlyModelFields(part, "text", "thought", "thoughtSignature", "functionCall", "functionResponse") != nil {
				return fmt.Errorf("%w: unsupported native content part", llm.ErrRequestRejected)
			}
		}
	}
	return nil
}

func nativeProviderError(ctx context.Context, err error, structured bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status *nativeStatusError
	if errors.As(err, &status) {
		if structured && errors.Is(status, llm.ErrRequestRejected) {
			return fmt.Errorf("%w: native provider HTTP status %d", llm.ErrStructuredRequestRejected, status.status)
		}
		return status
	}
	for _, cause := range []error{llm.ErrRedirectRejected, llm.ErrRequestRejected, llm.ErrUnusableResponse} {
		if errors.Is(err, cause) {
			if structured && cause == llm.ErrRequestRejected {
				return llm.ErrStructuredRequestRejected
			}
			return cause
		}
	}
	// SDK parse errors may contain complete response bodies or request maps.
	return fmt.Errorf("%w: native provider exchange failed", llm.ErrUnusableResponse)
}

func newGeminiCompletionAgent(client *ProviderClient, request Request) (agent.Agent, error) {
	return agent.New(agent.Config{Name: "completion", Description: "Execute one admitted native Google completion", Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			input, err := googleCompletionRequest(request)
			if err != nil {
				yield(nil, err)
				return
			}
			for response, err := range (&googleModel{client: client}).GenerateContent(ctx, input, request.Stream) {
				if err != nil {
					yield(nil, err)
					return
				}
				event, err := googleCompletionEvent(ctx, response, client.optional)
				if err != nil {
					yield(nil, err)
					return
				}
				if event != nil && !yield(event, nil) {
					return
				}
			}
		}
	}})
}

func googleCompletionRequest(request Request) (*model.LLMRequest, error) {
	input := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
	var instructions []string
	for _, message := range request.Messages {
		if message.Reasoning != "" || message.ReasoningContent != "" || message.Refusal != "" {
			return nil, fmt.Errorf("%w: incompatible provider-specific conversation fields", llm.ErrRequestRejected)
		}
		switch message.Role {
		case "system", "developer":
			if len(input.Contents) != 0 {
				return nil, fmt.Errorf("%w: native Google policy messages must precede conversation content", llm.ErrRequestRejected)
			}
			instructions = append(instructions, message.Content)
		case "user":
			input.Contents = append(input.Contents, genai.NewContentFromText(message.Content, genai.RoleUser))
		case "assistant":
			input.Contents = append(input.Contents, genai.NewContentFromText(message.Content, genai.RoleModel))
		default:
			return nil, fmt.Errorf("%w: unsupported native conversation role", llm.ErrRequestRejected)
		}
	}
	if len(instructions) != 0 {
		input.Config.SystemInstruction = genai.NewContentFromText(strings.Join(instructions, "\n\n"), genai.RoleUser)
	}
	if request.Schema != nil {
		input.Config.ResponseMIMEType = "application/json"
		input.Config.ResponseJsonSchema = request.Schema.Schema
	}
	return input, nil
}

func googleCompletionEvent(ctx agent.InvocationContext, response *model.LLMResponse, optional bool) (*session.Event, error) {
	text, err := googleCompletionText(response.Content)
	if err != nil {
		return nil, err
	}
	if response.Partial && text == "" {
		return nil, nil
	}
	if !response.Partial && strings.TrimSpace(text) == "" && !optional {
		return nil, fmt.Errorf("%w: native completion has no final text", llm.ErrUnusableResponse)
	}
	event := session.NewEvent(ctx, ctx.InvocationID())
	event.Author = ctx.Agent().Name()
	event.LLMResponse = *response
	// A terminal provider chunk remains a preview until the SDK has drained the
	// stream and emitted its aggregate. The collector accepts only that aggregate.
	event.TurnComplete = !response.Partial
	event.Content = genai.NewContentFromText(text, genai.RoleModel)
	if !response.Partial {
		result := Result{Content: text, Model: response.ModelVersion, FinishReason: "stop"}
		if usage := response.UsageMetadata; usage != nil {
			if _, err := googleTokenReceipt(usage, true); err != nil {
				return nil, err
			}
			result.UsageKnown = true
			result.Usage = llm.ChatUsage{PromptTokens: int(usage.PromptTokenCount), CompletionTokens: int(usage.CandidatesTokenCount) + int(usage.ThoughtsTokenCount), TotalTokens: int(usage.TotalTokenCount), PromptTokensDetails: &llm.PromptTokenDetails{CachedTokens: int(usage.CachedContentTokenCount)}}
		}
		event.Output = result
	}
	return event, nil
}

func settleGoogleUsage(reservation *modelReservation, usage *genai.GenerateContentResponseUsageMetadata) error {
	if usage == nil {
		return reservation.settle(0, 0, false, true)
	}
	receipt, err := googleTokenReceipt(usage, true)
	if err != nil {
		return err
	}
	if err := reservation.observeUsage(receipt); err != nil {
		return err
	}
	return reservation.settle(receipt.Input, receipt.Output, true, true)
}

func observeGoogleUsage(reservation *modelReservation, response *model.LLMResponse) error {
	if response == nil || response.UsageMetadata == nil {
		return nil
	}
	// The SDK may aggregate a truncated stream. An aggregate alone does not
	// prove that the provider supplied its terminal usage receipt.
	final := !response.Partial && response.FinishReason != "" && response.FinishReason != genai.FinishReasonUnspecified
	receipt, err := googleTokenReceipt(response.UsageMetadata, final)
	if err != nil {
		return err
	}
	return reservation.observeUsage(receipt)
}

func googleCompletionText(content *genai.Content) (string, error) {
	var text strings.Builder
	if content != nil {
		for _, part := range content.Parts {
			if part == nil || onlyModelFields(part, "text", "thought", "thoughtSignature") != nil {
				return "", fmt.Errorf("%w: native completion returned unsupported content", llm.ErrUnusableResponse)
			}
			if !part.Thought {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String(), nil
}
