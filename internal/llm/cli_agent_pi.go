package llm

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// piToolExtension registers the run's tool server with pi and blocks every
// other tool, so the model can act only through Kogin's gates.
const piToolExtension = `// Kogin's tools for one agent run: the run's MCP server, and nothing else.
export default function (pi) {
	const url = process.env.KOGIN_MCP_URL;
	const token = process.env.KOGIN_MCP_TOKEN;
	const maxTurns = Number(process.env.KOGIN_MAX_TURNS || "0");
	const timeout = Number(process.env.KOGIN_TOOL_TIMEOUT || "900");
	if (!url || !token) throw new Error("Kogin's tool server is not configured for this run");
	pi.registerMcpServer("kogin", { url, headers: { Authorization: "Bearer " + token }, exposure: "direct", timeout, description: "Kogin's tools for this task" });
	pi.on("tool_call", async (event) => {
		if (!String(event.toolName).startsWith("mcp__kogin__")) {
			return { block: true, reason: "Only Kogin's tools are available in this run." };
		}
	});
	let turns = 0;
	pi.on("turn_end", async (_event, ctx) => {
		turns++;
		if (maxTurns > 0 && turns >= maxTurns) ctx.abort();
	});
}
`

func preparePiAgent(directory string, profile config.ModelProfile, run AgentRun) (agentLaunch, agentStream, error) {
	if err := writeCLIFile(directory, "system.txt", run.System); err != nil {
		return agentLaunch{}, nil, err
	}
	settings := `{"retry":{"enabled":false,"provider":{"maxRetries":0}},"compaction":{"enabled":false}}`
	if err := writeCLIFile(directory, ".pi/settings.json", settings); err != nil {
		return agentLaunch{}, nil, err
	}
	if err := writeCLIFile(directory, "kogin-tools.ts", piToolExtension); err != nil {
		return agentLaunch{}, nil, err
	}
	args := []string{"--mode", "json", "--no-session", "--no-extensions", "--extension", "builtin:mcp", "--extension", filepath.Join(directory, "kogin-tools.ts"),
		"--tools", "mcp__kogin__*", "--exclude-tools", "codemode,tool_search,list_mcp_resources,list_mcp_resource_templates,read_mcp_resource",
		"--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--approve",
		"--system-prompt", filepath.Join(directory, "system.txt"), "--model", profile.Model}
	if profile.ReasoningEffort != "" {
		effort := profile.ReasoningEffort
		if effort == "none" {
			effort = "off"
		}
		args = append(args, "--thinking", effort)
	}
	// The token reaches the extension through the environment, never a file.
	env := cliEnvironment(config.PiProvider, []string{"KOGIN_MCP_URL=" + run.MCPURL, "KOGIN_MCP_TOKEN=" + run.MCPToken,
		"KOGIN_MAX_TURNS=" + strconv.Itoa(max(run.MaxTurns, 0)), "KOGIN_TOOL_TIMEOUT=" + strconv.Itoa(int(run.ToolTimeout.Seconds()))})
	stream := &piAgentStream{profile: profile, run: &AgentRunResult{Model: profile.Model, UsageKnown: true}}
	return agentLaunch{args: args, env: env, input: run.Prompt}, stream, nil
}

// piAgentStream follows pi's JSON event stream: turns, tool calls and the
// usage of every assistant message.
type piAgentStream struct {
	profile config.ModelProfile
	run     *AgentRunResult
	started bool
	failure error
}

func (d *piAgentStream) result() *AgentRunResult { return d.run }

func (d *piAgentStream) end(exit error, stderr string) error {
	switch {
	case d.failure != nil:
		return d.failure
	case exit != nil:
		if limit := planLimitFromText(config.PiProvider, stderr); limit != nil {
			return limit
		}
		return cliProcessError(config.PiProvider, exit)
	case !d.started:
		return unusableCLIResponse("pi did not start the agent run")
	}
	return nil
}

func (d *piAgentStream) event(data []byte) error {
	var event struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		IsError bool            `json:"isError"`
	}
	if json.Unmarshal(data, &event) != nil {
		return unusableCLIResponse("malformed pi event")
	}
	switch event.Type {
	case "agent_start":
		d.started = true
	case "turn_end":
		d.run.Turns++
	case "tool_execution_end":
		d.run.ToolCalls++
		if event.IsError {
			d.run.ToolErrors++
		}
	case "message_end":
		return d.message(event.Message)
	}
	return nil
}

func (d *piAgentStream) message(data json.RawMessage) error {
	var role struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(data, &role) != nil {
		return unusableCLIResponse("malformed pi message")
	}
	// User and tool-result messages echo context; only the model's turns count.
	if role.Role != "assistant" {
		return nil
	}
	var message struct {
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
		Model        string `json:"model"`
		Content      []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			Input  int `json:"input"`
			Output int `json:"output"`
			Total  int `json:"totalTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &message) != nil {
		return unusableCLIResponse("malformed pi assistant message")
	}
	if !matchesPiModel(d.profile.Model, message.Model) {
		return unusableCLIResponse("pi returned a different model than configured; use an exact model ID")
	}
	if message.Usage == nil {
		d.run.UsageKnown = false
	} else {
		d.run.Usage.PromptTokens += message.Usage.Input
		d.run.Usage.CompletionTokens += message.Usage.Output
		d.run.Usage.TotalTokens += message.Usage.Total
	}
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) != "" {
		d.run.Final = text.String()
	}
	switch message.StopReason {
	case "error":
		d.failure = piCompletionError(message.StopReason, message.ErrorMessage)
	case "stop", "toolUse", "aborted", "length":
		d.failure = nil
	}
	return nil
}
