package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// CompletionClient is the existing transport contract, including CLI providers.
// Implementations validate provider output before returning it to the runtime.
type CompletionClient interface {
	Chat(context.Context, []llm.ChatMessage) (*llm.ChatResponse, error)
	ChatWithJSONSchema(context.Context, []llm.ChatMessage, llm.JSONSchema) (*llm.ChatResponse, error)
}

func newCompletionAgent(client CompletionClient, request Request) (agent.Agent, error) {
	if provider, ok := client.(*ProviderClient); ok && provider.profile.IsGoogle() {
		return newGeminiCompletionAgent(provider, request)
	}
	if native, ok := client.(generationClient); ok {
		return agent.New(agent.Config{
			Name: "completion", Description: "Execute one admitted model completion",
			Run: nativeCompletionRun(native, request),
		})
	}
	return agent.New(agent.Config{
		Name:        "completion",
		Description: "Execute one admitted model completion",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				result, err := requestCompletion(ctx, client, request)
				if err != nil {
					yield(nil, err)
					return
				}
				event := session.NewEvent(ctx, ctx.InvocationID())
				event.Author = ctx.Agent().Name()
				event.LLMResponse = model.LLMResponse{
					Content:      genai.NewContentFromText(result.Content, genai.RoleModel),
					ModelVersion: result.Model,
					TurnComplete: true,
				}
				event.Output = result
				yield(event, nil)
			}
		},
	})
}

type generationClient interface {
	ModelName() string
	Generate(context.Context, llm.GenerationRequest) iter.Seq2[llm.GenerationEvent, error]
}

// compatibleModel implements ADK's model contract using the guarded transport.
// Profile sampling values remain owned by the client; unsupported ADK overrides
// are rejected before dispatch instead of being silently discarded.
type compatibleModel struct {
	client     generationClient
	schemaName string
}

var _ model.LLM = (*compatibleModel)(nil)

func (m *compatibleModel) Name() string { return m.client.ModelName() }

func (m *compatibleModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		input, err := m.generationRequest(request, stream)
		if err != nil {
			yield(nil, err)
			return
		}
		for event, err := range m.client.Generate(ctx, input) {
			if err != nil {
				yield(nil, err)
				return
			}
			response, err := nativeGenerationResponse(event)
			if !yield(response, err) || err != nil {
				return
			}
		}
	}
}

func nativeCompletionRun(client generationClient, request Request) func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			adapter := &compatibleModel{client: client, schemaName: "agent_result"}
			input := completionModelRequest(request)
			if request.Schema != nil {
				adapter.schemaName = request.Schema.Name
			}
			for response, err := range adapter.GenerateContent(ctx, input, request.Stream) {
				if err != nil {
					yield(nil, err)
					return
				}
				event := session.NewEvent(ctx, ctx.InvocationID())
				event.Author, event.LLMResponse = ctx.Agent().Name(), *response
				if !response.Partial {
					event.Output = completionResult(response)
				}
				if !yield(event, nil) {
					return
				}
			}
		}
	}
}

func completionModelRequest(request Request) *model.LLMRequest {
	input := &model.LLMRequest{}
	for _, message := range request.Messages {
		role := message.Role
		if role == "assistant" {
			role = genai.RoleModel
		}
		part := &genai.Part{Text: message.Content, PartMetadata: map[string]any{
			"mini_orca.reasoning_content": message.ReasoningContent,
			"mini_orca.reasoning":         message.Reasoning,
			"mini_orca.refusal":           message.Refusal,
		}}
		// The private compatibility bridge retains system/developer positions.
		// These roles are decoded locally and never sent as GenAI HTTP content.
		input.Contents = append(input.Contents, &genai.Content{Role: role, Parts: []*genai.Part{part}})
	}
	if request.Schema != nil {
		input.Config = &genai.GenerateContentConfig{ResponseMIMEType: "application/json", ResponseJsonSchema: request.Schema.Schema}
	}
	return input
}

func (m *compatibleModel) generationRequest(request *model.LLMRequest, stream bool) (llm.GenerationRequest, error) {
	if request == nil || request.Model != "" && request.Model != m.Name() {
		return llm.GenerationRequest{}, fmt.Errorf("%w: model routing must use the admitted profile", llm.ErrRequestRejected)
	}
	input := llm.GenerationRequest{Stream: stream}
	if err := m.applyGenerationConfig(&input, request.Config); err != nil {
		return input, err
	}
	for _, content := range request.Contents {
		messages, err := generationContent(content)
		if err != nil {
			return input, err
		}
		input.Messages = append(input.Messages, messages...)
	}
	return input, nil
}

func (m *compatibleModel) applyGenerationConfig(input *llm.GenerationRequest, config *genai.GenerateContentConfig) error {
	if config == nil {
		return nil
	}
	if err := onlyModelFields(config, "systemInstruction", "tools", "responseMimeType", "responseJsonSchema"); err != nil {
		return err
	}
	if config.SystemInstruction != nil {
		instruction, err := generationContent(config.SystemInstruction)
		if err != nil || len(instruction) != 1 || len(instruction[0].ToolCalls) != 0 || instruction[0].ToolCallID != "" {
			return fmt.Errorf("%w: unsupported system instruction", llm.ErrRequestRejected)
		}
		instruction[0].Role = "system"
		input.Messages = append(input.Messages, instruction[0])
	}
	if config.ResponseMIMEType != "" && config.ResponseMIMEType != "application/json" {
		return fmt.Errorf("%w: unsupported response format", llm.ErrRequestRejected)
	}
	if config.ResponseJsonSchema != nil {
		data, err := schemaJSON(config.ResponseJsonSchema)
		if err != nil {
			return err
		}
		input.Schema = &llm.JSONSchema{Name: m.schemaName, Schema: data}
	} else if config.ResponseMIMEType != "" {
		return fmt.Errorf("%w: structured responses require an explicit schema", llm.ErrRequestRejected)
	}
	tools, err := generationTools(config.Tools)
	input.Tools = tools
	return err
}

func generationTools(tools []*genai.Tool) ([]llm.ToolDefinition, error) {
	var definitions []llm.ToolDefinition
	for _, tool := range tools {
		if tool == nil || onlyModelFields(tool, "functionDeclarations") != nil {
			return nil, fmt.Errorf("%w: unsupported provider tool", llm.ErrRequestRejected)
		}
		for _, declaration := range tool.FunctionDeclarations {
			if declaration == nil || declaration.Parameters != nil || declaration.Behavior != "" {
				return nil, fmt.Errorf("%w: function parameters must use JSON Schema", llm.ErrRequestRejected)
			}
			// ADK tools retain their output schema for local result validation;
			// compatible providers accept only the function's input schema.
			parameters, err := schemaJSON(declaration.ParametersJsonSchema)
			if err != nil {
				return nil, err
			}
			definitions = append(definitions, llm.ToolDefinition{Name: declaration.Name, Description: declaration.Description, Parameters: parameters})
		}
	}
	return definitions, nil
}

func schemaJSON(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		return append(json.RawMessage(nil), raw...), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JSON schema", llm.ErrRequestRejected)
	}
	return data, nil
}

func onlyModelFields(value any, allowed ...string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: invalid model configuration", llm.ErrRequestRejected)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("%w: invalid model configuration", llm.ErrRequestRejected)
	}
	for _, name := range allowed {
		delete(fields, name)
	}
	if len(fields) != 0 {
		return fmt.Errorf("%w: unsupported model configuration fields", llm.ErrRequestRejected)
	}
	return nil
}

func generationContent(content *genai.Content) ([]llm.GenerationMessage, error) {
	if content == nil {
		return nil, fmt.Errorf("%w: missing model content", llm.ErrRequestRejected)
	}
	role := content.Role
	if role == genai.RoleModel {
		role = "assistant"
	} else if role == "" {
		role = "user"
	}
	message := llm.GenerationMessage{ChatMessage: llm.ChatMessage{Role: role}}
	var responses []llm.GenerationMessage
	for _, part := range content.Parts {
		if part == nil || onlyModelFields(part, "text", "thought", "partMetadata", "functionCall", "functionResponse") != nil {
			return nil, fmt.Errorf("%w: unsupported content part", llm.ErrRequestRejected)
		}
		if part.FunctionResponse != nil {
			response, err := generationToolResponse(part)
			if err != nil {
				return nil, err
			}
			responses = append(responses, response)
			continue
		}
		if err := appendGenerationPart(&message, part); err != nil {
			return nil, err
		}
	}
	if len(responses) != 0 {
		if message.Content != "" || len(message.ToolCalls) != 0 || message.ReasoningContent != "" {
			return nil, fmt.Errorf("%w: mixed tool responses and message content", llm.ErrRequestRejected)
		}
		return responses, nil
	}
	return []llm.GenerationMessage{message}, nil
}

func appendGenerationPart(message *llm.GenerationMessage, part *genai.Part) error {
	if part.FunctionCall != nil {
		call := part.FunctionCall
		if part.Text != "" || call.WillContinue != nil || len(call.PartialArgs) != 0 {
			return fmt.Errorf("%w: incomplete function call", llm.ErrRequestRejected)
		}
		arguments, err := json.Marshal(call.Args)
		if call.Args == nil {
			arguments = []byte("{}")
		}
		if err != nil {
			return fmt.Errorf("%w: invalid function arguments", llm.ErrRequestRejected)
		}
		message.ToolCalls = append(message.ToolCalls, llm.ToolCall{ID: call.ID, Name: call.Name, Arguments: arguments})
		return nil
	}
	if part.Thought {
		if message.Role != "assistant" {
			return fmt.Errorf("%w: invalid reasoning role", llm.ErrRequestRejected)
		}
		message.ReasoningContent += part.Text
	} else {
		message.Content += part.Text
	}
	for name, destination := range map[string]*string{
		"mini_orca.reasoning_content": &message.ReasoningContent,
		"mini_orca.reasoning":         &message.Reasoning,
		"mini_orca.refusal":           &message.Refusal,
	} {
		if value, present := part.PartMetadata[name]; present {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("%w: invalid message metadata", llm.ErrRequestRejected)
			}
			*destination += text
		}
	}
	return nil
}

func generationToolResponse(part *genai.Part) (llm.GenerationMessage, error) {
	if part.Text != "" || part.FunctionCall != nil || part.Thought {
		return llm.GenerationMessage{}, fmt.Errorf("%w: mixed tool-response part", llm.ErrRequestRejected)
	}
	response := part.FunctionResponse
	if response.Response == nil {
		return llm.GenerationMessage{}, fmt.Errorf("%w: tool response must be an object", llm.ErrRequestRejected)
	}
	if err := onlyModelFields(response, "id", "name", "response"); err != nil {
		return llm.GenerationMessage{}, err
	}
	data, err := json.Marshal(response.Response)
	if err != nil {
		return llm.GenerationMessage{}, fmt.Errorf("%w: invalid tool response", llm.ErrRequestRejected)
	}
	return llm.GenerationMessage{ChatMessage: llm.ChatMessage{Role: "tool", Content: string(data)}, ToolCallID: response.ID, ToolName: response.Name}, nil
}

func nativeGenerationResponse(event llm.GenerationEvent) (*model.LLMResponse, error) {
	if event.Result == nil {
		if event.TextDelta == "" {
			return nil, fmt.Errorf("%w: empty generation event", llm.ErrUnusableResponse)
		}
		return &model.LLMResponse{Content: genai.NewContentFromText(event.TextDelta, genai.RoleModel), Partial: true}, nil
	}
	result := event.Result
	if event.TextDelta != "" || result.Message.Refusal != "" {
		return nil, fmt.Errorf("%w: invalid final generation event", llm.ErrUnusableResponse)
	}
	content := genai.NewContentFromText(result.Message.Content, genai.RoleModel)
	content.Parts[0].PartMetadata = map[string]any{
		"mini_orca.reasoning_content": result.Message.ReasoningContent,
		"mini_orca.reasoning":         result.Message.Reasoning,
	}
	for _, call := range result.Message.ToolCalls {
		var arguments map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(call.Arguments)))
		decoder.UseNumber()
		if err := decoder.Decode(&arguments); err != nil {
			return nil, fmt.Errorf("%w: invalid tool arguments", llm.ErrUnusableResponse)
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Name, Args: arguments}})
	}
	response := &model.LLMResponse{
		Content: content, ModelVersion: result.Model, TurnComplete: true,
		FinishReason:   nativeFinishReason(result.FinishReason),
		CustomMetadata: map[string]any{"mini_orca.finish_reason": result.FinishReason},
	}
	if err := nativeGenerationUsage(response, result.Usage); err != nil {
		return nil, err
	}
	return response, nil
}

func nativeGenerationUsage(response *model.LLMResponse, usage *llm.ChatUsage) error {
	if usage != nil {
		for _, count := range []int{usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens} {
			if count < 0 || count > math.MaxInt32 {
				return fmt.Errorf("%w: invalid generation usage", llm.ErrUnusableResponse)
			}
		}
		response.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: int32(usage.PromptTokens), CandidatesTokenCount: int32(usage.CompletionTokens), TotalTokenCount: int32(usage.TotalTokens),
		}
		if details := usage.PromptTokensDetails; details != nil {
			if details.CachedTokens < 0 || details.CachedTokens > usage.PromptTokens {
				return fmt.Errorf("%w: invalid cached generation usage", llm.ErrUnusableResponse)
			}
			response.UsageMetadata.CachedContentTokenCount = int32(details.CachedTokens)
			response.CustomMetadata["mini_orca.cache_usage_known"] = true
		}
	}
	return nil
}

func nativeFinishReason(reason string) genai.FinishReason {
	switch reason {
	case "stop", "tool_calls":
		return genai.FinishReasonStop
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	case "":
		return genai.FinishReasonUnspecified
	default:
		return genai.FinishReasonOther
	}
}

func completionResult(response *model.LLMResponse) Result {
	result := Result{Model: response.ModelVersion}
	result.FinishReason, _ = response.CustomMetadata["mini_orca.finish_reason"].(string)
	for _, part := range response.Content.Parts {
		if !part.Thought {
			result.Content += part.Text
		}
	}
	if response.UsageMetadata != nil {
		result.UsageKnown = true
		usage := response.UsageMetadata
		result.Usage = llm.ChatUsage{PromptTokens: int(usage.PromptTokenCount), CompletionTokens: int(usage.CandidatesTokenCount), TotalTokens: int(usage.TotalTokenCount)}
		if response.CustomMetadata["mini_orca.cache_usage_known"] == true {
			result.Usage.PromptTokensDetails = &llm.PromptTokenDetails{CachedTokens: int(usage.CachedContentTokenCount)}
		}
	}
	return result
}

func requestCompletion(ctx context.Context, client CompletionClient, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var response *llm.ChatResponse
	var err error
	if request.Schema == nil {
		response, err = client.Chat(ctx, request.Messages)
	} else {
		response, err = client.ChatWithJSONSchema(ctx, request.Messages, *request.Schema)
	}
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return Result{}, fmt.Errorf("%w: completion has no choice", llm.ErrUnusableResponse)
	}
	choice := response.Choices[0]
	return Result{
		Content: choice.Message.Content, Model: response.Model,
		FinishReason: choice.FinishReason, Usage: response.Usage, UsageKnown: response.Usage != (llm.ChatUsage{}),
	}, nil
}
