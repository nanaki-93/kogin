package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The test binary plays pi, Claude Code or agy when KOGIN_FAKE_CLI is
// set, telling them apart by their flags. As an agent it checks that it was
// started with only Kogin's tools, connects to the run's tool server and
// calls the tools of a script, writing the CLI's own event stream. As a
// one-turn completion it approves, as a reviewer would.
const fakeCLIEnv = "KOGIN_FAKE_CLI"

// fakeCLIDirEnv names the directory with the scripts (coder.json,
// tester.json) and where the fake records what it saw.
const fakeCLIDirEnv = "KOGIN_FAKE_CLI_DIR"

const fakeReview = `{"verdict":"approve","summary":"Sub is correct and tested.","findings":[]}`

// fakePiStep is one model turn: a tool call, or the final text.
type fakePiStep struct {
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Text      string         `json:"text,omitempty"`
	// Exit ends the fake with this status, as a crashed CLI would.
	Exit int `json:"exit,omitempty"`
	// Limit ends the run as the CLI does when its plan reached its usage
	// limit. The step is used up: the next run continues the script.
	Limit bool `json:"limit,omitempty"`
	// Next, on a text step, ends this run there; the next run of the role
	// continues the script after it.
	Next bool `json:"next,omitempty"`
}

// fakePiRecord is what the fake saw and got, for the test to read.
type fakePiRecord struct {
	CLI     string   `json:"cli"`
	Role    string   `json:"role"`
	Prompt  string   `json:"prompt"`
	Results []string `json:"results"`
}

type piBearer struct{ token string }

func (b piBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(request)
}

// fakeCLIArgs reads the fake's command line.
type fakeCLIArgs []string

func (args fakeCLIArgs) value(flag string) string {
	if index := slices.Index(args, flag); index >= 0 && index+1 < len(args) {
		return args[index+1]
	}
	return ""
}

// fakeCLIRun is one fake run: its role, script and record, and how it reaches
// the tool server.
type fakeCLIRun struct {
	cli, role string
	steps     []fakePiStep
	record    fakePiRecord
	dir       string
	session   *mcp.ClientSession
}

func fakeCLI() int {
	args := fakeCLIArgs(os.Args[1:])
	switch {
	case slices.Contains(args, "--agent"):
		return fakeAgy(args)
	case slices.Contains(args, "--system-prompt-file"):
		return fakeClaude(args)
	default:
		return fakePi(args)
	}
}

func fakeRole(system string) string {
	switch {
	case strings.Contains(system, "You are Kogin's testing agent"):
		return "tester"
	case strings.Contains(system, "You are Kogin's guide to a codebase"):
		return "asker"
	case strings.Contains(system, "You are Kogin's"):
		return "coder"
	default:
		return "reviewer"
	}
}

// startFakeRun loads the role's script and connects to the tool server.
func startFakeRun(cli, system, prompt, endpoint, token string) (*fakeCLIRun, error) {
	run := &fakeCLIRun{cli: cli, role: fakeRole(system), dir: os.Getenv(fakeCLIDirEnv)}
	appendFakeLog("runs.log", cli+":"+run.role)
	run.record = fakePiRecord{CLI: cli, Role: run.role, Prompt: prompt}
	if data, err := os.ReadFile(filepath.Join(run.dir, run.role+".json")); err == nil {
		_ = json.Unmarshal(data, &run.steps)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "fake-" + cli, Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: piBearer{token}}, MaxRetries: -1}, nil)
	if err != nil {
		return nil, fmt.Errorf("fake %s: connect: %w", cli, err)
	}
	run.session = session
	return run, nil
}

// call calls a tool and records its result text.
func (run *fakeCLIRun) call(step fakePiStep) (failed bool) {
	result, err := run.session.CallTool(context.Background(), &mcp.CallToolParams{Name: step.Tool, Arguments: step.Arguments})
	text, failed := "", true
	if err == nil {
		failed = result.IsError
		for _, content := range result.Content {
			if part, ok := content.(*mcp.TextContent); ok {
				text += part.Text
			}
		}
	} else {
		text = err.Error()
	}
	run.record.Results = append(run.record.Results, text)
	return failed
}

// useLimit removes the script's plan-limit step, so the next run continues.
func (run *fakeCLIRun) useLimit(index int) {
	rest := slices.Delete(slices.Clone(run.steps), index, index+1)
	data, _ := json.Marshal(rest)
	_ = os.WriteFile(filepath.Join(run.dir, run.role+".json"), data, 0o600)
}

// useSteps removes the script's steps through index, so the next run
// continues after them.
func (run *fakeCLIRun) useSteps(index int) {
	data, _ := json.Marshal(run.steps[index+1:])
	_ = os.WriteFile(filepath.Join(run.dir, run.role+".json"), data, 0o600)
}

func (run *fakeCLIRun) close() {
	run.session.Close()
	data, _ := json.Marshal(run.record)
	_ = os.WriteFile(filepath.Join(run.dir, run.role+".log.json"), data, 0o600)
}

// recordFakeReview counts the one-turn completions a fake CLI answered.
func recordFakeReview(cli string) { appendFakeLog("reviews.log", cli) }

// appendFakeLog adds a line to one of the fake's logs.
func appendFakeLog(name, line string) {
	file, err := os.OpenFile(filepath.Join(os.Getenv(fakeCLIDirEnv), name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(file, line)
		file.Close()
	}
}

func emitJSON(event any) {
	data, _ := json.Marshal(event)
	fmt.Println(string(data))
}

func fakePi(args fakeCLIArgs) int {
	system, _ := os.ReadFile(args.value("--system-prompt"))
	prompt, _ := io.ReadAll(os.Stdin)
	model := args.value("--model")
	model = model[strings.LastIndex(model, "/")+1:]
	assistant := func(stop, text, failure string) map[string]any {
		return map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "model": model, "stopReason": stop, "errorMessage": failure,
			"content": []map[string]any{{"type": "text", "text": text}}, "usage": map[string]any{"input": 1000, "output": 50, "totalTokens": 1050}}}
	}
	if slices.Contains(args, "--no-tools") {
		emitJSON(map[string]any{"type": "agent_start"})
		if fakeWalkthroughRequest(string(prompt), args) {
			emitJSON(assistant("stop", fakeWalkthrough, ""))
			emitJSON(map[string]any{"type": "agent_end", "willRetry": false})
			return 0
		}
		recordFakeReview("pi")
		// A reviewer.limit file makes the next review reach the plan's limit.
		if os.Remove(filepath.Join(os.Getenv(fakeCLIDirEnv), "reviewer.limit")) == nil {
			emitJSON(assistant("error", "", "You have hit your ChatGPT usage limit (plus plan). Try again in ~60 min."))
			emitJSON(map[string]any{"type": "agent_end", "willRetry": false})
			return 1
		}
		emitJSON(assistant("stop", fakeReview, ""))
		emitJSON(map[string]any{"type": "agent_end", "willRetry": false})
		return 0
	}
	if args.value("--tools") != "mcp__kogin__*" || !slices.Contains(args, "builtin:mcp") || args.value("--mode") != "json" {
		fmt.Fprintln(os.Stderr, "fake pi: not limited to Kogin's tools:", args)
		return 3
	}
	run, err := startFakeRun("pi", string(system), string(prompt), os.Getenv("KOGIN_MCP_URL"), os.Getenv("KOGIN_MCP_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 4
	}
	defer run.close()
	emitJSON(map[string]any{"type": "session", "version": 3})
	emitJSON(map[string]any{"type": "agent_start"})
	for index, step := range run.steps {
		switch {
		case step.Exit != 0:
			return step.Exit
		case step.Limit:
			run.useLimit(index)
			emitJSON(assistant("error", "", "You have hit your ChatGPT usage limit (plus plan). Try again in ~60 min."))
			emitJSON(map[string]any{"type": "agent_end", "willRetry": false})
			return 1
		case step.Tool == "":
			if step.Next {
				run.useSteps(index)
			}
			emitJSON(assistant("stop", step.Text, ""))
			emitJSON(map[string]any{"type": "turn_end"})
		default:
			emitJSON(assistant("toolUse", "", ""))
			failed := run.call(step)
			emitJSON(map[string]any{"type": "tool_execution_end", "toolName": "mcp__kogin__" + step.Tool, "isError": failed})
			emitJSON(map[string]any{"type": "turn_end"})
			continue
		}
		break
	}
	emitJSON(map[string]any{"type": "agent_end", "willRetry": false})
	emitJSON(map[string]any{"type": "agent_settled", "aborted": false})
	return 0
}

// fakeClaudeModel is the model Claude Code resolves the test's alias to.
func fakeClaudeModel(alias string) string { return "claude-" + alias + "-fake-1" }

func fakeClaude(args fakeCLIArgs) int {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ANTHROPIC_") {
			fmt.Fprintln(os.Stderr, "fake claude: started with API-key settings")
			return 3
		}
	}
	if index := slices.Index(args, "--tools"); index < 0 || args[index+1] != "" || !slices.Contains(args, "--strict-mcp-config") || args.value("--permission-mode") != "dontAsk" {
		fmt.Fprintln(os.Stderr, "fake claude: its own tools are on:", args)
		return 3
	}
	system, _ := os.ReadFile(args.value("--system-prompt-file"))
	prompt, _ := io.ReadAll(os.Stdin)
	model := fakeClaudeModel(args.value("--model"))
	usage := map[string]int{"input_tokens": 200, "cache_read_input_tokens": 800, "output_tokens": 50}
	init := map[string]any{"type": "system", "subtype": "init", "apiKeySource": "none", "model": model, "tools": []string{}, "mcp_servers": []map[string]string{}}
	assistant := func(content ...map[string]any) map[string]any {
		return map[string]any{"type": "assistant", "message": map[string]any{"model": model, "content": content}, "parent_tool_use_id": nil}
	}
	configFile := args.value("--mcp-config")
	if configFile == "" {
		reply := fakeReview
		if fakeWalkthroughRequest(string(system), nil) {
			reply = fakeWalkthrough
		} else {
			recordFakeReview("claude")
		}
		emitJSON(init)
		emitJSON(assistant(map[string]any{"type": "text", "text": reply}))
		emitJSON(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "result": reply, "usage": usage})
		return 0
	}
	if args.value("--allowedTools") != "mcp__kogin" {
		fmt.Fprintln(os.Stderr, "fake claude: Kogin's tools are not allowed:", args)
		return 3
	}
	var servers struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(configFile)
	_ = json.Unmarshal(data, &servers)
	server := servers.MCPServers["kogin"]
	run, err := startFakeRun("claude", string(system), string(prompt), server.URL, strings.TrimPrefix(server.Headers["Authorization"], "Bearer "))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 4
	}
	defer run.close()
	init["mcp_servers"] = []map[string]string{{"name": "kogin", "status": "connected"}}
	emitJSON(init)
	turns := 0
	for index, step := range run.steps {
		turns++
		switch {
		case step.Exit != 0:
			return step.Exit
		case step.Limit:
			run.useLimit(index)
			emitJSON(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "resetsAt": fakeResetEpoch, "rateLimitType": "five_hour"}})
			emitJSON(map[string]any{"type": "assistant", "message": map[string]any{"model": "<synthetic>", "content": []map[string]any{{"type": "text", "text": "You've hit your limit · resets 3pm"}}}, "parent_tool_use_id": nil, "error": "rate_limit"})
			emitJSON(map[string]any{"type": "result", "subtype": "success", "is_error": true, "num_turns": turns, "result": "You've hit your limit · resets 3pm", "usage": usage})
			return 1
		case step.Tool == "":
			emitJSON(assistant(map[string]any{"type": "text", "text": step.Text}))
			emitJSON(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": turns, "result": step.Text, "usage": usage})
			return 0
		}
		emitJSON(assistant(map[string]any{"type": "tool_use", "id": fmt.Sprint("toolu_", index), "name": "mcp__kogin__" + step.Tool, "input": step.Arguments}))
		failed := run.call(step)
		emitJSON(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": fmt.Sprint("toolu_", index), "is_error": failed}}}, "parent_tool_use_id": nil})
	}
	emitJSON(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": turns, "result": "", "usage": usage})
	return 0
}

// fakeResetEpoch is when the fakes say a plan's limit resets.
const fakeResetEpoch = 1798761600

func fakeAgy(args fakeCLIArgs) int {
	if slices.Contains(args, "--dangerously-skip-permissions") {
		fmt.Fprintln(os.Stderr, "fake agy: started without its permission policy")
		return 3
	}
	agent, _ := os.ReadFile(filepath.Join(".agents", "agents", "kogin", "agent.md"))
	if !strings.Contains(string(agent), "tools: []") || !strings.Contains(string(agent), "commandExecutionPolicy: off") {
		fmt.Fprintln(os.Stderr, "fake agy: the agent has built-in tools")
		return 3
	}
	var input struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	data, _ := io.ReadAll(os.Stdin)
	_ = json.Unmarshal(data, &input)
	model := args.value("--model")
	init := map[string]any{"event": "init", "init": map[string]any{"agent": "kogin", "model": model, "tools": []string{"run_command", "write_to_file"}, "permission_mode": "request-review"}}
	usage := map[string]int{"input_tokens": 1000, "output_tokens": 50, "total_tokens": 1050}
	result := func(status, response, failure string) map[string]any {
		// Like agy, one conversation turn per prompt, whatever the model did.
		return map[string]any{"event": "result", "result": map[string]any{"status": status, "response": response, "error": failure, "num_turns": 1, "usage": usage}}
	}
	_, system, _ := strings.Cut(string(agent), "# System Prompt\n")
	if strings.Contains(string(agent), "inheritMcp: false") {
		reply := fakeReview
		if fakeWalkthroughRequest(string(agent), nil) {
			reply = fakeWalkthrough
		} else {
			recordFakeReview("agy")
		}
		emitJSON(init)
		emitJSON(result("SUCCESS", reply, ""))
		return 0
	}
	var servers struct {
		MCPServers map[string]struct {
			ServerURL string            `json:"serverUrl"`
			Headers   map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	config, _ := os.ReadFile(filepath.Join(".agents", "mcp_config.json"))
	_ = json.Unmarshal(config, &servers)
	server := servers.MCPServers["kogin"]
	run, err := startFakeRun("agy", system, input.Message.Content, server.ServerURL, strings.TrimPrefix(server.Headers["Authorization"], "Bearer "))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 4
	}
	defer run.close()
	emitJSON(init)
	step := func(index int, state, kind, tool string, failed bool) {
		update := map[string]any{"step_index": index, "state": state, "step_type": kind}
		if tool != "" {
			// agy calls every MCP tool through call_mcp_tool.
			info := map[string]any{"name": "call_mcp_tool", "parameters": map[string]any{"ServerName": "kogin", "ToolName": tool, "Arguments": map[string]any{}}}
			if failed {
				info["error"] = map[string]string{"type": "tool_error", "message": "the tool reported an error"}
			}
			update["tool_name"], update["tool_info"] = "call_mcp_tool", info
		}
		emitJSON(map[string]any{"event": "step_update", "step_update": update})
	}
	for index, current := range run.steps {
		step(2*index, "DONE", "agent_response", "", false)
		switch {
		case current.Exit != 0:
			return current.Exit
		case current.Limit:
			run.useLimit(index)
			emitJSON(result("ERROR", "", "You have exhausted your capacity on this model. Your quota will reset after 1h0m0s."))
			return 1
		case current.Tool == "":
			emitJSON(result("SUCCESS", current.Text, ""))
			return 0
		}
		step(2*index+1, "ACTIVE", "tool", current.Tool, false)
		step(2*index+1, "DONE", "tool", current.Tool, run.call(current))
	}
	emitJSON(result("SUCCESS", "", ""))
	return 0
}

// fakeWalkthroughRequest tells the review's walkthrough from the reviews.
func fakeWalkthroughRequest(prompt string, args []string) bool {
	return strings.Contains(prompt+strings.Join(args, " "), "You explain a code change")
}
