package llm

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

// Claude Code runs on the Claude plan its user signed in to with /login.
// Kasuri starts it with its own tools off, no settings but those of a
// private, empty directory, and none of the user's MCP servers, skills or
// hooks. Its environment carries no API key (see cliEnvironment), and a run
// that would still bill one is stopped before the model is called.

// claudeArgs are the flags every Claude Code run gets.
func claudeArgs(directory string, profile config.ModelProfile, maxTurns int) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--input-format", "text",
		"--tools", "", "--strict-mcp-config", "--permission-mode", "dontAsk", "--setting-sources", "project",
		"--disable-slash-commands", "--no-session-persistence",
		"--system-prompt-file", filepath.Join(directory, "system.txt"), "--model", profile.Model}
	if maxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(maxTurns))
	}
	if profile.ReasoningEffort != "" {
		args = append(args, "--effort", profile.ReasoningEffort)
	}
	return args
}

// prepareClaude is a one-turn completion without tools.
func prepareClaude(directory string, profile config.ModelProfile, system string) ([]string, error) {
	if err := writeCLIFile(directory, "system.txt", system); err != nil {
		return nil, err
	}
	return claudeArgs(directory, profile, 1), nil
}

func prepareClaudeAgent(directory string, profile config.ModelProfile, run AgentRun) (agentLaunch, agentStream, error) {
	if err := writeCLIFile(directory, "system.txt", run.System+claudeToolGuide(run.Tools)); err != nil {
		return agentLaunch{}, nil, err
	}
	servers, err := json.Marshal(map[string]any{"mcpServers": map[string]any{toolServerName: map[string]any{
		"type": "http", "url": run.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + run.MCPToken},
	}}})
	if err != nil {
		return agentLaunch{}, nil, fmt.Errorf("llm client: cannot encode the tool server's configuration")
	}
	// The private directory is removed when the run ends; the token dies with the run.
	if err := writeCLIFile(directory, "mcp.json", string(servers)); err != nil {
		return agentLaunch{}, nil, err
	}
	args := append(claudeArgs(directory, profile, run.MaxTurns), "--mcp-config", filepath.Join(directory, "mcp.json"), "--allowedTools", "mcp__"+toolServerName)
	env := cliEnvironment(config.ClaudeProvider, []string{"MCP_TOOL_TIMEOUT=" + strconv.FormatInt(run.ToolTimeout.Milliseconds(), 10), "ENABLE_TOOL_SEARCH=false", "DISABLE_AUTOUPDATER=1"})
	stream := &claudeStream{profile: profile, run: &AgentRunResult{Model: profile.Model}, agent: true, tools: toolSet(run.Tools)}
	return agentLaunch{args: args, env: env, input: run.Prompt}, stream, nil
}

// claudeToolPrefix is how Claude Code names the tool server's tools.
const claudeToolPrefix = "mcp__" + toolServerName + "__"

// claudeToolGuide tells the model the names Claude Code lists its tools
// under: Kasuri's prompts name them bare, as every CLI shares them.
func claudeToolGuide(tools []AgentTool) string {
	if len(tools) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n# Your tools\nYour tools are those of the %q MCP server, listed to you as %s<tool>. Always call them by those full names (%s%s, not %s), also where these instructions name them without the prefix.\n", toolServerName, claudeToolPrefix, claudeToolPrefix, tools[0].Name, tools[0].Name)
}

// maxMisnamedClaudeCalls is how many calls a run may make to its own tools
// by their bare names before it stops.
const maxMisnamedClaudeCalls = 3

// claudeEvent is one line of Claude Code's stream-json output; each type
// fills its own fields.
type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// system/init
	APIKeySource string `json:"apiKeySource"`
	Model        string `json:"model"`
	MCPServers   []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	// assistant and user
	Message         json.RawMessage `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Error           string          `json:"error"`
	// rate_limit_event
	RateLimit *struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
	// result
	IsError        bool     `json:"is_error"`
	NumTurns       int      `json:"num_turns"`
	Result         string   `json:"result"`
	Errors         []string `json:"errors"`
	TerminalReason string   `json:"terminal_reason"`
	APIErrorStatus *int     `json:"api_error_status"`
	Usage          *struct {
		Input         int `json:"input_tokens"`
		Output        int `json:"output_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		CacheRead     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// claudeStream follows Claude Code's stream-json output for a completion or
// an agent run.
type claudeStream struct {
	profile config.ModelProfile
	run     *AgentRunResult
	// agent is set for an agent run: the tool server must be connected, and
	// the model may call its tools and no others.
	agent bool
	tools map[string]bool
	// misnamed holds, by tool_use id, the calls to the run's tools by their
	// bare names that await Claude Code's refusal; misnamedCalls counts them all.
	misnamed          map[string]bool
	misnamedCalls     int
	started, finished bool
	// limit is the plan limit Claude Code reported while running.
	limit   *PlanLimitError
	failure error
}

func (d *claudeStream) result() *AgentRunResult { return d.run }

func (d *claudeStream) event(line []byte) error {
	var event claudeEvent
	if json.Unmarshal(line, &event) != nil {
		return unusableCLIResponse("malformed Claude Code event")
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			return d.init(event)
		}
	case "assistant":
		return d.assistant(event)
	case "user":
		return d.toolResults(event.Message)
	case "rate_limit_event":
		if info := event.RateLimit; info != nil && info.Status == "rejected" {
			d.limit = &PlanLimitError{Provider: config.ClaudeProvider}
			if info.ResetsAt > 0 {
				d.limit.ResetsAt = time.Unix(info.ResetsAt, 0)
			}
		}
	case "result":
		return d.finish(event)
	}
	return nil
}

func (d *claudeStream) init(event claudeEvent) error {
	if d.started {
		return unusableCLIResponse("Claude Code started twice")
	}
	switch event.APIKeySource {
	case "none", "oauth":
	default:
		source := event.APIKeySource
		if source == "" {
			source = "unreported"
		}
		return fmt.Errorf("llm client: %w: Claude Code would use an API key (source: %s); Kasuri runs Claude only on your Claude plan: remove the key and sign in with /login", ErrRequestRejected, source)
	}
	if !matchesClaudeModel(d.profile.Model, event.Model) {
		return unusableCLIResponse("Claude Code selected a different model than configured")
	}
	if d.agent && !claudeServerConnected(event) {
		return fmt.Errorf("llm client: %w: Claude Code could not connect to Kasuri's tool server", ErrUnusableResponse)
	}
	d.started, d.run.Model = true, event.Model
	return nil
}

func claudeServerConnected(event claudeEvent) bool {
	for _, server := range event.MCPServers {
		if server.Name == toolServerName {
			return server.Status == "connected"
		}
	}
	return false
}

// matchesClaudeModel accepts the full model Claude Code resolved an alias
// such as sonnet or opus[1m] to.
func matchesClaudeModel(configured, actual string) bool {
	family, _, _ := strings.Cut(configured, "[")
	switch {
	case actual == "":
		return false
	case actual == configured || actual == family:
		return true
	}
	return !strings.HasPrefix(family, "claude-") && strings.Contains(actual, family)
}

func (d *claudeStream) assistant(event claudeEvent) error {
	if !d.started || d.finished {
		return unusableCLIResponse("Claude Code answered outside its run")
	}
	if event.ParentToolUseID != nil && *event.ParentToolUseID != "" {
		return unusableCLIResponse("Claude Code started a subagent")
	}
	var message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	}
	if json.Unmarshal(event.Message, &message) != nil {
		return unusableCLIResponse("malformed Claude Code message")
	}
	var text strings.Builder
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if !d.agent {
				return unusableCLIResponse("Claude Code attempted tool use")
			}
			if err := d.toolUse(block.ID, block.Name); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(text.String()) != "" {
		d.run.Final = text.String()
	}
	d.assistantError(event.Error, text.String())
	return nil
}

// toolUse admits one tool call of an agent run: a call to one of the run's
// tools by the name Claude Code lists it under, or by its bare name (read_file
// for mcp__kasuri__read_file). Claude Code has no tool by a bare name, so it
// answers that call with an error and the model calls the tool again by its
// full name; toolResults checks that refusal. Any other tool ends the run.
func (d *claudeStream) toolUse(id, name string) error {
	if !strings.HasPrefix(name, claudeToolPrefix) || !ownTool(name, d.tools) {
		if id == "" || !d.tools[name] {
			return foreignTool("Claude Code", name)
		}
		if d.misnamedCalls == maxMisnamedClaudeCalls {
			return unusableCLIResponse(fmt.Sprintf("Claude Code kept calling %s without its %s prefix", name, claudeToolPrefix))
		}
		if d.misnamed == nil {
			d.misnamed = map[string]bool{}
		}
		d.misnamed[id] = true
		d.misnamedCalls++
	}
	d.run.ToolCalls++
	return nil
}

func (d *claudeStream) assistantError(kind, text string) {
	switch kind {
	case "rate_limit":
		if d.limit == nil {
			if d.limit = planLimitFromText(config.ClaudeProvider, text); d.limit == nil {
				d.limit = &PlanLimitError{Provider: config.ClaudeProvider}
			}
		}
	case "authentication_failed":
		d.failure = fmt.Errorf("llm client: %w: Claude Code is not signed in; run claude and sign in with /login", ErrRequestRejected)
	case "billing_error":
		d.failure = fmt.Errorf("llm client: %w: Claude Code reported a billing problem with the Claude account", ErrRequestRejected)
	}
}

// toolResults counts the tool calls that failed. A call by a bare name must
// have failed: a result that is not an error means a tool outside the run's
// ran, and that ends the run. A prompt echo has string content and no results.
func (d *claudeStream) toolResults(data json.RawMessage) error {
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	var blocks []struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
	}
	if json.Unmarshal(data, &message) != nil || json.Unmarshal(message.Content, &blocks) != nil {
		return nil
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		if d.misnamed[block.ToolUseID] && !block.IsError {
			return unusableCLIResponse("Claude Code ran a tool outside Kasuri's tool server")
		}
		delete(d.misnamed, block.ToolUseID)
		if block.IsError {
			d.run.ToolErrors++
		}
	}
	return nil
}

func (d *claudeStream) finish(event claudeEvent) error {
	if !d.started || d.finished {
		return unusableCLIResponse("Claude Code reported a result outside its run")
	}
	d.finished, d.run.Turns = true, event.NumTurns
	if usage := event.Usage; usage != nil {
		prompt := usage.Input + usage.CacheCreation + usage.CacheRead
		d.run.Usage, d.run.UsageKnown = ChatUsage{PromptTokens: prompt, CompletionTokens: usage.Output, TotalTokens: prompt + usage.Output}, true
	}
	switch {
	case event.Subtype == "success" && !event.IsError:
		if strings.TrimSpace(event.Result) != "" {
			d.run.Final = event.Result
		}
	case event.Subtype == "error_max_turns" || event.TerminalReason == "max_turns":
		d.run.TurnLimit = true
	case d.failure == nil:
		d.failure = d.stopped(event)
	}
	return nil
}

// stopped explains a result that is an error.
func (d *claudeStream) stopped(event claudeEvent) error {
	if d.limit != nil {
		return d.limit
	}
	if limit := planLimitFromText(config.ClaudeProvider, event.Result+"\n"+strings.Join(event.Errors, "\n")); limit != nil {
		return limit
	}
	if event.APIErrorStatus != nil && *event.APIErrorStatus == 429 {
		return &PlanLimitError{Provider: config.ClaudeProvider}
	}
	return unusableCLIResponse("Claude Code stopped with " + strings.ReplaceAll(event.Subtype, "_", " "))
}

func (d *claudeStream) end(exit error, stderr string) error {
	switch {
	case d.failure != nil:
		return d.failure
	case d.finished && d.run.TurnLimit:
		return nil
	case d.limit != nil && !d.finished:
		return d.limit
	case exit != nil:
		if limit := planLimitFromText(config.ClaudeProvider, stderr); limit != nil {
			return limit
		}
		return cliProcessError(config.ClaudeProvider, exit)
	case !d.started:
		return unusableCLIResponse("Claude Code did not start the agent run")
	case !d.finished:
		return unusableCLIResponse("Claude Code ended without a result")
	}
	return nil
}

// claudeEvent decodes Claude Code's stream for a one-turn completion.
func (d *cliResponseDecoder) claudeEvent(data []byte) error {
	if d.claude == nil {
		d.claude = &claudeStream{profile: d.profile, run: &AgentRunResult{Model: d.profile.Model}}
	}
	if err := d.claude.event(data); err != nil {
		return err
	}
	d.started = d.claude.started
	if !d.claude.finished || d.finished {
		return nil
	}
	d.finished = true
	switch run := d.claude.run; {
	case d.claude.failure != nil:
		return d.claude.failure
	case run.TurnLimit:
		return unusableCLIResponse("Claude Code needed more than one turn")
	default:
		d.response = cliChatResponse(run.Model, run.Final, run.Usage)
	}
	return nil
}
