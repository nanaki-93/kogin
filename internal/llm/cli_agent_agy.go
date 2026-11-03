package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// errAgyToolsDenied is what an agy run reports when agy's permission policy
// kept the model from Kogin's tools. Headless agy soft-denies a tool that
// would ask for approval; Kogin does not skip agy's permissions, so the
// user allows its tool server once.
var errAgyToolsDenied = fmt.Errorf("llm client: %w: agy did not allow Kogin's tools; add \"mcp(%s/*)\" to permissions.allow in ~/.gemini/antigravity-cli/settings.json", ErrRequestRejected, toolServerName)

// agyAgentFile is the custom agent of a run: no built-in tools or commands,
// no subagents, and the workspace's MCP servers, which are the run's.
const agyAgentFile = "---\nname: kogin\ndescription: Kogin task agent\ntools: []\nmainAgent: true\nsubagent: false\ninheritMcp: true\ncommandExecutionPolicy: off\nskills: []\nplugins: []\n---\n# System Prompt\n"

func prepareAgyAgent(ctx context.Context, directory string, profile config.ModelProfile, run AgentRun) (agentLaunch, agentStream, error) {
	server := map[string]any{"serverUrl": run.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + run.MCPToken}, "timeoutSeconds": int(run.ToolTimeout.Seconds())}
	if len(run.Tools) > 0 {
		server["enabledTools"] = toolNames(run.Tools)
	}
	servers, err := json.Marshal(map[string]any{"mcpServers": map[string]any{toolServerName: server}})
	if err != nil {
		return agentLaunch{}, nil, fmt.Errorf("llm client: cannot encode the tool server's configuration")
	}
	// The run's directory is its workspace and is removed when the run ends.
	if err := writeCLIFile(directory, ".agents/mcp_config.json", string(servers)); err != nil {
		return agentLaunch{}, nil, err
	}
	if err := writeCLIFile(directory, ".agents/agents/kogin/agent.md", agyAgentFile+run.System+agyToolGuide(run.Tools)); err != nil {
		return agentLaunch{}, nil, err
	}
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--agent", "kogin", "--disable-slash-commands",
		"--model", profile.Model, "--log-file", os.DevNull, "--print-timeout", agyPrintTimeout(ctx)}
	if profile.ReasoningEffort != "" {
		args = append(args, "--effort", profile.ReasoningEffort)
	}
	stream := &agyAgentStream{profile: profile, run: &AgentRunResult{Model: profile.Model}, tools: toolSet(run.Tools), maxTurns: run.MaxTurns, responses: map[int]bool{}}
	return agentLaunch{args: args, input: agyUserMessage(run.Prompt)}, stream, nil
}

// agyToolGuide lists the run's tools in the system prompt. agy shows its model
// only call_mcp_tool and keeps each MCP tool's schema in a file the model
// would read with agy's own file tools, which the run's agent does not have.
func agyToolGuide(tools []AgentTool) string {
	if len(tools) == 0 {
		return ""
	}
	var guide strings.Builder
	fmt.Fprintf(&guide, "\n\n# Your tools\nCall each tool with call_mcp_tool, ServerName %q, the ToolName below and Arguments that match its schema. These are your only tools, and everything you need to call them is here: do not look for their description files.\n", toolServerName)
	for _, tool := range tools {
		schema := tool.Schema
		var compact bytes.Buffer
		if json.Compact(&compact, schema) == nil {
			schema = compact.Bytes()
		}
		fmt.Fprintf(&guide, "\n- %s: %s\n  Arguments schema: %s\n", tool.Name, strings.TrimSpace(tool.Description), schema)
	}
	return guide.String()
}

// agyPrintTimeout lets agy wait as long as the run may last; agy's own
// default is five minutes.
func agyPrintTimeout(ctx context.Context) string {
	minutes := 120.0
	if deadline, ok := ctx.Deadline(); ok {
		minutes = math.Ceil(time.Until(deadline).Minutes()) + 1
	}
	return fmt.Sprintf("%dm", max(int(minutes), 1))
}

// agyAgentStream follows agy's stream-json output during an agent run.
type agyAgentStream struct {
	profile           config.ModelProfile
	run               *AgentRunResult
	tools             map[string]bool
	maxTurns          int
	started, finished bool
	// responses holds the steps in which the model answered: its turns.
	responses map[int]bool
	failure   error
}

type agyAgentEvent struct {
	Event string `json:"event"`
	Init  struct {
		Agent string `json:"agent"`
		Model string `json:"model"`
	} `json:"init"`
	Step struct {
		Index    int    `json:"step_index"`
		State    string `json:"state"`
		Type     string `json:"step_type"`
		ToolName string `json:"tool_name"`
		Info     *struct {
			Name string `json:"name"`
			// Parameters name the server and tool of a call_mcp_tool step.
			Parameters struct {
				ServerName string `json:"ServerName"`
				ToolName   string `json:"ToolName"`
			} `json:"parameters"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
		NumTurns int    `json:"num_turns"`
		Usage    *struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
			Total  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"result"`
}

func (d *agyAgentStream) result() *AgentRunResult { return d.run }

func (d *agyAgentStream) event(line []byte) error {
	var event agyAgentEvent
	if json.Unmarshal(line, &event) != nil {
		return unusableCLIResponse("malformed agy event")
	}
	switch event.Event {
	case "init":
		if d.started || event.Init.Agent != "kogin" || event.Init.Model != d.profile.Model {
			return unusableCLIResponse("agy did not select the configured model and agent")
		}
		d.started = true
	case "step_update":
		if !d.started || d.finished {
			return unusableCLIResponse("agy progress is outside its run")
		}
		return d.step(event)
	case "result":
		if !d.started || d.finished {
			return unusableCLIResponse("agy reported a result outside its run")
		}
		d.finish(event)
	}
	return nil
}

func (d *agyAgentStream) step(event agyAgentEvent) error {
	step := event.Step
	switch step.Type {
	case "subagent":
		return unusableCLIResponse("agy started a subagent")
	case "tool":
		if err := d.ownToolStep(event); err != nil {
			return err
		}
		if step.State != "DONE" {
			return nil
		}
		d.run.ToolCalls++
		if step.Info != nil && step.Info.Error != nil {
			d.run.ToolErrors++
			if denied(step.Info.Error.Type + " " + step.Info.Error.Message) {
				return errAgyToolsDenied
			}
		}
	case "agent_response":
		d.responses[step.Index] = true
		d.run.Turns = len(d.responses)
		if d.maxTurns > 0 && d.run.Turns > d.maxTurns {
			d.run.Turns, d.run.TurnLimit = d.maxTurns, true
			return errStopAgentRun
		}
	}
	return nil
}

// agyMCPTool is the tool through which agy's model calls any MCP server's
// tool, naming the server and the tool in its parameters.
const agyMCPTool = "call_mcp_tool"

// ownToolStep accepts a tool step only when it calls one of the run's tools
// on Kogin's server.
func (d *agyAgentStream) ownToolStep(event agyAgentEvent) error {
	step := event.Step
	name := step.ToolName
	if name == "" && step.Info != nil {
		name = step.Info.Name
	}
	if name != agyMCPTool {
		if ownTool(name, d.tools) {
			return nil
		}
		return foreignTool("agy", name)
	}
	if step.Info == nil {
		return foreignTool("agy", name)
	}
	call := step.Info.Parameters
	if call.ServerName != toolServerName || call.ToolName == "" || (len(d.tools) > 0 && !d.tools[call.ToolName]) {
		return foreignTool("agy", call.ServerName+"/"+call.ToolName)
	}
	return nil
}

func denied(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "denied") || strings.Contains(text, "permission")
}

func (d *agyAgentStream) finish(event agyAgentEvent) {
	result := event.Result
	d.finished = true
	// agy's num_turns counts the conversation's turns (one per prompt), not
	// the model's: the responses counted above are the model's turns.
	if d.run.Turns == 0 {
		d.run.Turns = result.NumTurns
	}
	if result.Usage != nil {
		d.run.Usage, d.run.UsageKnown = ChatUsage{PromptTokens: result.Usage.Input, CompletionTokens: result.Usage.Output, TotalTokens: result.Usage.Total}, true
	}
	if result.Status == "SUCCESS" {
		d.run.Final = result.Response
		return
	}
	if limit := planLimitFromText(config.AgyProvider, result.Error); limit != nil {
		d.failure = limit
		return
	}
	d.failure = unusableCLIResponse("agy ended with status " + strings.ToLower(result.Status))
}

func (d *agyAgentStream) end(exit error, stderr string) error {
	switch {
	case d.run.TurnLimit:
		return nil
	case d.failure != nil:
		return d.failure
	case exit != nil:
		if limit := planLimitFromText(config.AgyProvider, stderr); limit != nil {
			return limit
		}
		return cliProcessError(config.AgyProvider, exit)
	case !d.started:
		return unusableCLIResponse("agy did not start the agent run")
	case !d.finished:
		return unusableCLIResponse("agy ended without a result")
	case d.run.ToolCalls == 0 && strings.Contains(stderr, toolServerName) && denied(stderr):
		// The soft denial ends the run normally; its notice is on stderr.
		return errAgyToolsDenied
	}
	return nil
}
