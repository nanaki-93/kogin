package agentruntime

import (
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

func newInvestigationAgent(client CompletionClient, request Request) (agent.Agent, error) {
	system, prefix, prompt, err := investigationMessages(request)
	if err != nil {
		return nil, err
	}
	adapter, err := investigationAdapter(client, request.Schema)
	if err != nil {
		return nil, err
	}
	var tools []tool.Tool
	for _, item := range request.Investigation.tools {
		tools = append(tools, item)
	}
	config := llmagent.Config{
		Name: "investigation", Description: "Investigate the explicitly admitted repository scope",
		Model: &investigationModel{inner: adapter, scope: request.Investigation, contextTokens: request.ContextMaxTokens, system: system, prefix: prefix, protected: prompt}, Tools: tools,
		DisallowTransferToParent: true, DisallowTransferToPeers: true,
	}
	if request.Schema != nil {
		// OutputSchema installs an SDK tool. The transport's response schema
		// constrains the final answer without adding executable authority.
		config.GenerateContentConfig = &genai.GenerateContentConfig{ResponseMIMEType: "application/json", ResponseJsonSchema: request.Schema.Schema}
	}
	worker, err := llmagent.New(config)
	if err != nil {
		return nil, err
	}
	return agent.New(agent.Config{Name: "investigation_runner", Description: "Bounded repository investigation", Run: investigationRun(worker)})
}

func investigationMessages(request Request) (string, []*genai.Content, string, error) {
	if len(request.Messages) == 0 || request.Stream {
		return "", nil, "", fmt.Errorf("%w: investigation requires one admitted user prompt", llm.ErrRequestRejected)
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "user" || strings.TrimSpace(last.Content) == "" {
		return "", nil, "", fmt.Errorf("%w: investigation requires one admitted user prompt", llm.ErrRequestRejected)
	}
	var system string
	start := 0
	if len(request.Messages) > 1 && (request.Messages[0].Role == "system" || request.Messages[0].Role == "developer") {
		if strings.TrimSpace(request.Messages[0].Content) == "" {
			return "", nil, "", fmt.Errorf("%w: investigation requires one admitted user prompt", llm.ErrRequestRejected)
		}
		system, start = request.Messages[0].Content, 1
	}
	var prefix []*genai.Content
	for _, msg := range request.Messages[start : len(request.Messages)-1] {
		if strings.TrimSpace(msg.Content) == "" || (msg.Role != "user" && msg.Role != "assistant") {
			return "", nil, "", fmt.Errorf("%w: investigation requires one admitted user prompt", llm.ErrRequestRejected)
		}
		role := genai.Role(genai.RoleUser)
		if msg.Role == "assistant" {
			role = genai.RoleModel
		}
		prefix = append(prefix, genai.NewContentFromText(msg.Content, role))
	}
	return system, prefix, last.Content, nil
}

func investigationAdapter(client CompletionClient, schema *llm.JSONSchema) (model.LLM, error) {
	if provider, ok := client.(*ProviderClient); ok && provider.profile.IsGoogle() {
		return &googleModel{client: provider}, nil
	}
	transport, ok := client.(generationClient)
	if !ok {
		return nil, fmt.Errorf("%w: selected provider cannot use investigation tools", llm.ErrRequestRejected)
	}
	adapter := &compatibleModel{client: transport, schemaName: "investigation"}
	if schema != nil {
		adapter.schemaName = schema.Name
	}
	return adapter, nil
}
