package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

const (
	claudeInit       = `{"type":"system","subtype":"init","apiKeySource":"none","model":"claude-fixture-model-1","tools":[],"mcp_servers":[{"name":"kogin","status":"connected"}]}`
	claudeResetEpoch = 1798761600
)

// claudeFixtureOutput is Claude Code's stream-json output for one fixture mode.
func claudeFixtureOutput(mode, content string) string {
	init := claudeInit
	switch mode {
	case "api-key":
		init = strings.Replace(init, `"apiKeySource":"none"`, `"apiKeySource":"ANTHROPIC_API_KEY"`, 1)
	case "wrong-model":
		init = strings.Replace(init, `claude-fixture-model-1`, `claude-other-1`, 1)
	}
	lines := []string{init}
	switch mode {
	case "partial":
		return init + "\n"
	case "tool":
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{}}]},"parent_tool_use_id":null}`)
	case "plan-limit":
		lines = append(lines,
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1798761600,"rateLimitType":"five_hour"}}`,
			`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"You've hit your limit · resets 3pm"}]},"parent_tool_use_id":null,"error":"rate_limit"}`,
			`{"type":"result","subtype":"success","is_error":true,"num_turns":1,"result":"You've hit your limit · resets 3pm","usage":{"input_tokens":0,"output_tokens":0}}`)
	case "plan-limit-text":
		lines = append(lines, `{"type":"result","subtype":"success","is_error":true,"num_turns":1,"result":"Claude AI usage limit reached|1798761600"}`)
	case "agent":
		lines = append(lines,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading."},{"type":"tool_use","name":"mcp__kogin__read_file","input":{"path":"calc.go"}}]},"parent_tool_use_id":null}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]},"parent_tool_use_id":null}`,
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__kogin__finish","input":{}}]},"parent_tool_use_id":null}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":true}]},"parent_tool_use_id":null}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]},"parent_tool_use_id":null}`,
			`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"Done.","usage":{"input_tokens":100,"cache_creation_input_tokens":1000,"cache_read_input_tokens":4000,"output_tokens":250}}`)
	default:
		result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": mode == "failed", "num_turns": 1, "result": content, "usage": map[string]int{"input_tokens": 10, "output_tokens": 20}})
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"not the answer"},{"type":"text","text":"draft"}]},"parent_tool_use_id":null}`, string(result))
	}
	return strings.Join(lines, "\n") + "\n"
}

func claudeProfile(t *testing.T, mode string) (config.ModelProfile, string) {
	t.Helper()
	profile, capture := cliFixture(t, config.ClaudeProvider, mode)
	profile.Model, profile.ReasoningEffort = "fixture-model", "high"
	return profile, capture
}

func TestClaudeCompletionsRunOnThePlanWithoutToolsOrAPIKeys(t *testing.T) {
	for name, value := range map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "ANTHROPIC_AUTH_TOKEN": "token", "ANTHROPIC_BASE_URL": "http://127.0.0.1:1", "CLAUDE_CODE_USE_BEDROCK": "1"} {
		t.Setenv(name, value)
	}
	profile, capture := claudeProfile(t, "success")
	response, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "system", Content: "Be exact."}, {Role: "user", Content: "Explain the supplied source."}})
	if err != nil || response.Choices[0].Message.Content != cliFixtureContent || response.Model != "claude-fixture-model-1" || response.Usage.TotalTokens != 30 {
		t.Fatalf("response = %+v, %v", response, err)
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if len(request.AnthropicEnv) != 0 {
		t.Fatalf("Claude Code saw API-key settings: %v", request.AnthropicEnv)
	}
	for _, pair := range [][2]string{{"--tools", ""}, {"--permission-mode", "dontAsk"}, {"--setting-sources", "project"}, {"--max-turns", "1"}, {"--model", "fixture-model"}, {"--effort", "high"}, {"--output-format", "stream-json"}} {
		if index := slices.Index(request.Args, pair[0]); index < 0 || request.Args[index+1] != pair[1] {
			t.Fatalf("%s is not %q in %q", pair[0], pair[1], request.Args)
		}
	}
	for _, flag := range []string{"-p", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--verbose"} {
		if !slices.Contains(request.Args, flag) {
			t.Fatalf("%s missing from %q", flag, request.Args)
		}
	}
	if slices.Contains(request.Args, "--mcp-config") || !strings.Contains(request.System, "Be exact.") || !strings.Contains(request.Input, "Explain the supplied source.") {
		t.Fatalf("request = %+v", request)
	}
}

func TestClaudeCompletionsRejectAPIKeysToolsAndOtherModels(t *testing.T) {
	for _, mode := range []string{"api-key", "tool", "wrong-model", "partial", "failed"} {
		t.Run(mode, func(t *testing.T) {
			profile, _ := claudeProfile(t, mode)
			_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Explain."}})
			want := ErrUnusableResponse
			if mode == "api-key" {
				want = ErrRequestRejected
			}
			if !errors.Is(err, want) || errors.Is(err, ErrPlanLimit) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if mode == "api-key" && !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
				t.Fatalf("the error does not name the key's source: %v", err)
			}
		})
	}
}

func TestEveryCLIReportsAnExhaustedPlanWithItsResetTime(t *testing.T) {
	for _, mode := range []string{"plan-limit", "plan-limit-text"} {
		t.Run(mode, func(t *testing.T) {
			profile, _ := claudeProfile(t, mode)
			_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Explain."}})
			var limit *PlanLimitError
			if !errors.As(err, &limit) || !errors.Is(err, ErrPlanLimit) || limit.Provider != config.ClaudeProvider || !limit.ResetsAt.Equal(time.Unix(claudeResetEpoch, 0)) {
				t.Fatalf("error = %v (%+v)", err, limit)
			}
			if !strings.Contains(err.Error(), "Claude plan reached its usage limit; it resets at") {
				t.Fatalf("message = %q", err)
			}
		})
	}
	t.Run("agy", func(t *testing.T) {
		profile, _ := cliFixture(t, config.AgyProvider, "plan-limit")
		started := time.Now()
		_, err := NewClient(profile).Chat(context.Background(), []ChatMessage{{Role: "user", Content: "Explain."}})
		var limit *PlanLimitError
		if !errors.As(err, &limit) || limit.Provider != config.AgyProvider || limit.ResetsAt.Before(started.Add(89*time.Minute)) || limit.ResetsAt.After(time.Now().Add(91*time.Minute)) {
			t.Fatalf("error = %v (%+v)", err, limit)
		}
	})
	t.Run("pi", func(t *testing.T) {
		err := piCompletionError("error", "You have hit your ChatGPT usage limit (plus plan). Try again in ~158 min.")
		var limit *PlanLimitError
		if !errors.As(err, &limit) || limit.Provider != config.PiProvider || time.Until(limit.ResetsAt) < 157*time.Minute {
			t.Fatalf("error = %v (%+v)", err, limit)
		}
	})
}

func TestPlanLimitTextIsRecognizedOnlyWhenItSaysSo(t *testing.T) {
	fixed := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = time.Now })
	for text, resets := range map[string]time.Duration{
		"Your quota will reset after 1h29m29s.":                   time.Hour + 29*time.Minute + 29*time.Second,
		"RESOURCE_EXHAUSTED: try again in 30 seconds":             30 * time.Second,
		"429 Too Many Requests":                                   0,
		`{"error":{"type":"usage_limit","resets_in_seconds":60}}`: time.Minute,
		"You have exhausted your capacity on this model.":         0,
		"You've hit your limit. Retry in 2 hours":                 2 * time.Hour,
	} {
		limit := planLimitFromText(config.AgyProvider, text)
		if limit == nil || (resets == 0) != limit.ResetsAt.IsZero() || (resets != 0 && !limit.ResetsAt.Equal(fixed.Add(resets))) {
			t.Fatalf("%q = %+v", text, limit)
		}
	}
	for _, text := range []string{"", "exit status 1", "invalid model selection", "the model refused"} {
		if limit := planLimitFromText(config.AgyProvider, text); limit != nil {
			t.Fatalf("%q was taken for a plan limit", text)
		}
	}
}

func decodeClaudeAgent(t *testing.T, tools []string, lines ...string) (*claudeStream, error) {
	t.Helper()
	stream := &claudeStream{profile: config.ModelProfile{Provider: config.ClaudeProvider, Model: "sonnet"}, run: &AgentRunResult{}, agent: true, tools: toolSet(testTools(tools...))}
	return stream, readAgentStream(strings.NewReader(strings.Join(lines, "\n")+"\n"), stream, config.ClaudeProvider)
}

func TestAClaudeAgentRunCountsToolsTurnsAndCachedUsage(t *testing.T) {
	stream, err := decodeClaudeAgent(t, []string{"read_file", "finish"}, strings.Split(strings.TrimSpace(strings.ReplaceAll(claudeFixtureOutput("agent", ""), "claude-fixture-model-1", "claude-sonnet-5")), "\n")...)
	if err != nil || stream.end(nil, "") != nil {
		t.Fatalf("stream = %+v, %v", stream, err)
	}
	run := stream.run
	if run.Model != "claude-sonnet-5" || run.Turns != 3 || run.ToolCalls != 2 || run.ToolErrors != 1 || run.Final != "Done." || !run.UsageKnown || run.Usage != (ChatUsage{PromptTokens: 5100, CompletionTokens: 250, TotalTokens: 5350}) {
		t.Fatalf("run = %+v", run)
	}
}

func TestAClaudeAgentRunStopsOutsideItsToolServerAndPlan(t *testing.T) {
	init := strings.Replace(claudeInit, "claude-fixture-model-1", "claude-sonnet-5", 1)
	for name, lines := range map[string][]string{
		"built-in tool":    {init, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]},"parent_tool_use_id":null}`},
		"another server":   {init, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__github__create_issue"}]},"parent_tool_use_id":null}`},
		"unlisted tool":    {init, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__kogin__shell"}]},"parent_tool_use_id":null}`},
		"subagent":         {init, `{"type":"assistant","message":{"content":[]},"parent_tool_use_id":"toolu_1"}`},
		"server not ready": {strings.Replace(init, `"status":"connected"`, `"status":"failed"`, 1)},
		"api key":          {strings.Replace(init, `"apiKeySource":"none"`, `"apiKeySource":"/login managed key"`, 1)},
		"no key source":    {strings.Replace(init, `"apiKeySource":"none",`, ``, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeClaudeAgent(t, []string{"read_file"}, lines...); err == nil {
				t.Fatal("the run went on")
			}
		})
	}

	stream, err := decodeClaudeAgent(t, nil, init, `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":12}`)
	if err != nil || stream.end(errors.New("exit status 1"), "") != nil || !stream.run.TurnLimit || stream.run.Turns != 12 {
		t.Fatalf("a run at its turn limit = %+v, %v", stream.run, err)
	}
	stream, _ = decodeClaudeAgent(t, nil, init, `{"type":"assistant","message":{"content":[]},"parent_tool_use_id":null,"error":"authentication_failed"}`, `{"type":"result","subtype":"success","is_error":true,"num_turns":1,"result":"Not logged in"}`)
	if err := stream.end(errors.New("exit status 1"), ""); !errors.Is(err, ErrRequestRejected) || !strings.Contains(err.Error(), "/login") {
		t.Fatalf("signed-out run = %v", err)
	}
	stream, _ = decodeClaudeAgent(t, nil, init, `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1798761600}}`)
	var limit *PlanLimitError
	if err := stream.end(errors.New("killed"), ""); !errors.As(err, &limit) || !limit.ResetsAt.Equal(time.Unix(claudeResetEpoch, 0)) {
		t.Fatalf("a run that stopped at the plan's limit = %v", err)
	}
}

func TestClaudeAgentRunsGetOnlyTheRunsToolServer(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	directory := t.TempDir()
	launch, _, err := prepareClaudeAgent(directory, config.ModelProfile{Provider: config.ClaudeProvider, Model: "opus"}, AgentRun{System: "Be careful.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token", MaxTurns: 40, ToolTimeout: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"--mcp-config", filepath.Join(directory, "mcp.json")}, {"--allowedTools", "mcp__kogin"}, {"--tools", ""}, {"--max-turns", "40"}, {"--permission-mode", "dontAsk"}} {
		if index := slices.Index(launch.args, pair[0]); index < 0 || launch.args[index+1] != pair[1] {
			t.Fatalf("%s is not %q in %q", pair[0], pair[1], launch.args)
		}
	}
	if !slices.Contains(launch.args, "--strict-mcp-config") || !slices.Contains(launch.env, "MCP_TOOL_TIMEOUT=180000") || slices.ContainsFunc(launch.env, func(entry string) bool { return strings.HasPrefix(entry, "ANTHROPIC_") }) {
		t.Fatalf("launch = %q / env without the key: %v", launch.args, !slices.Contains(launch.env, "ANTHROPIC_API_KEY=sk-ant-test"))
	}
	info, err := os.Stat(filepath.Join(directory, "mcp.json"))
	data, _ := os.ReadFile(filepath.Join(directory, "mcp.json"))
	if err != nil || info.Mode().Perm() != 0o600 || string(data) != `{"mcpServers":{"kogin":{"headers":{"Authorization":"Bearer run-token"},"type":"http","url":"http://127.0.0.1:9/mcp"}}}` {
		t.Fatalf("mcp.json = %s (%v)", data, err)
	}
}

func TestAClaudeAgentRunEndToEndThroughTheCLI(t *testing.T) {
	profile, capture := cliFixture(t, config.ClaudeProvider, "agent")
	profile.Model = "claude-fixture-model-1"
	result, err := NewClient(profile).RunAgent(context.Background(), AgentRun{System: "Be careful.", Prompt: "Add Sub.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token", Tools: testTools("read_file", "finish"), MaxTurns: 10})
	if err != nil || result.ToolCalls != 2 || result.Turns != 3 || result.TurnLimit {
		t.Fatalf("result = %+v, %v", result, err)
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if request.Input != "Add Sub." || request.System != "Be careful." {
		t.Fatalf("request = %+v", request)
	}
	profile, _ = cliFixture(t, config.ClaudeProvider, "plan-limit")
	profile.Model = "claude-fixture-model-1"
	if _, err := NewClient(profile).RunAgent(context.Background(), AgentRun{Prompt: "Add Sub.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token"}); !errors.Is(err, ErrPlanLimit) {
		t.Fatalf("plan limit = %v", err)
	}
}

func TestToolNamesAreMatchedWithEachCLIsServerPrefix(t *testing.T) {
	tools := toolSet(testTools("read_file", "finish"))
	for name, own := range map[string]bool{
		"mcp__kogin__read_file": true, "mcp__kogin__finish": true, "kogin/read_file": true, "read_file": true,
		"mcp__kogin__shell": false, "mcp__github__read_file_x": false, "Bash": false, "kogin-evil/read_file": false, "": false,
	} {
		if ownTool(name, tools) != own {
			t.Fatalf("%q own = %v", name, !own)
		}
	}
	if !ownTool("mcp__kogin__anything", nil) || ownTool("mcp__github__anything", nil) {
		t.Fatal("without a tool list, only the server's prefix decides")
	}
}

func TestClaudeModelsAreItsAliasesWhenItIsInstalled(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	models, err := DiscoverClaudeModels(context.Background(), executable)
	if err != nil || len(models) != 4 || models[0].ID != "opus" || !slices.ContainsFunc(models, func(model ClaudeModel) bool { return model.ID == "sonnet" }) {
		t.Fatalf("models = %+v, %v", models, err)
	}
	for _, model := range models {
		if !config.ValidClaudeModelID(model.ID) {
			t.Fatalf("%s is not a valid model ID", model.ID)
		}
	}
	if _, err := DiscoverClaudeModels(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing Claude Code listed models")
	}
}

// testTools describes tools by name, each taking a path.
func testTools(names ...string) []AgentTool {
	tools := make([]AgentTool, 0, len(names))
	for _, name := range names {
		tools = append(tools, AgentTool{Name: name, Description: "The " + name + " tool.", Schema: json.RawMessage(`{"type": "object", "properties": {"path": {"type": "string"}}}`)})
	}
	return tools
}
