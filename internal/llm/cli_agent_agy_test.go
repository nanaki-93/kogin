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

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

const agyAgentInit = `{"event":"init","init":{"agent":"kasuri","model":"fixture-model","tools":["run_command","write_to_file"],"permission_mode":"request-review"}}`

// agyToolStep is a tool step as agy writes it. A name with a slash is an MCP
// call (server/tool), which agy makes through call_mcp_tool.
func agyToolStep(index int, state, name, failure string) string {
	info := map[string]any{"name": name}
	if server, tool, mcp := strings.Cut(name, "/"); mcp {
		name = "call_mcp_tool"
		info = map[string]any{"name": name, "parameters": map[string]any{"Arguments": map[string]string{"path": "notes.txt"}, "ServerName": server, "ToolName": tool}}
	}
	if failure != "" {
		info["error"] = map[string]string{"type": "tool_error", "message": failure}
	}
	step, _ := json.Marshal(map[string]any{"event": "step_update", "step_update": map[string]any{"step_index": index, "state": state, "step_type": "tool", "tool_name": name, "tool_info": info}})
	return string(step)
}

func agyResponseStep(index int) string {
	return `{"event":"step_update","step_update":{"step_index":` + string(rune('0'+index)) + `,"state":"ACTIVE","step_type":"agent_response","text_delta":"…"}}`
}

// agyAgentFixture is agy's stream-json output for an agent run.
func agyAgentFixture(mode string) string {
	lines := []string{agyAgentInit}
	switch mode {
	case "plan-limit":
		lines = append(lines, `{"event":"result","result":{"status":"ERROR","error":"You have exhausted your capacity on this model. Your quota will reset after 1h30m0s.","num_turns":0}}`)
	default:
		lines = append(lines, agyResponseStep(1), `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"unknown"}}`, agyToolStep(3, "ACTIVE", "kasuri/read_file", ""), agyToolStep(3, "DONE", "kasuri/read_file", ""),
			agyResponseStep(4), agyToolStep(5, "DONE", "kasuri/edit_file", "old_text was not found"), agyToolStep(6, "DONE", "kasuri/finish", ""), agyResponseStep(7),
			// agy counts one conversation turn per prompt, whatever the model did.
			`{"event":"result","result":{"status":"SUCCESS","response":"Done.","num_turns":1,"usage":{"input_tokens":9000,"output_tokens":300,"total_tokens":9300}}}`)
	}
	return strings.Join(lines, "\n") + "\n"
}

func decodeAgyAgent(t *testing.T, maxTurns int, lines ...string) (*agyAgentStream, error) {
	t.Helper()
	stream := &agyAgentStream{profile: config.ModelProfile{Provider: config.AgyProvider, Model: "fixture-model"}, run: &AgentRunResult{}, tools: toolSet(testTools("read_file", "edit_file", "finish")), maxTurns: maxTurns, responses: map[int]bool{}}
	return stream, readAgentStream(strings.NewReader(strings.Join(lines, "\n")+"\n"), stream, config.AgyProvider)
}

func TestAnAgyAgentRunCountsToolsTurnsAndUsage(t *testing.T) {
	stream, err := decodeAgyAgent(t, 0, strings.Split(strings.TrimSpace(agyAgentFixture("agent")), "\n")...)
	if err != nil || stream.end(nil, "") != nil {
		t.Fatalf("stream = %+v, %v", stream, err)
	}
	if run := stream.run; run.ToolCalls != 3 || run.ToolErrors != 1 || run.Turns != 3 || run.Final != "Done." || !run.UsageKnown || run.Usage.TotalTokens != 9300 {
		t.Fatalf("run = %+v", run)
	}
}

func TestAnAgyAgentRunStopsOutsideItsToolServerTurnsAndPermissions(t *testing.T) {
	for name, lines := range map[string][]string{
		"built-in tool": {agyAgentInit, agyToolStep(1, "ACTIVE", "run_command", "")},
		"other server":  {agyAgentInit, agyToolStep(1, "ACTIVE", "github/read_file", "")},
		"unlisted tool": {agyAgentInit, agyToolStep(1, "ACTIVE", "kasuri/shell", "")},
		"no parameters": {agyAgentInit, `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"call_mcp_tool"}}`},
		"subagent":      {agyAgentInit, `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"subagent"}}`},
		"other agent":   {strings.Replace(agyAgentInit, `"agent":"kasuri"`, `"agent":"default"`, 1)},
		"before init":   {agyToolStep(1, "DONE", "kasuri/read_file", "")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAgyAgent(t, 0, lines...); !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("the run went on: %v", err)
			}
		})
	}
	if _, err := decodeAgyAgent(t, 0, agyAgentInit, agyToolStep(1, "DONE", "kasuri/read_file", "Permission denied by policy")); !errors.Is(err, errAgyToolsDenied) {
		t.Fatalf("a denied tool = %v", err)
	}
	stream, _ := decodeAgyAgent(t, 0, agyAgentInit, `{"event":"result","result":{"status":"SUCCESS","response":"I cannot use the tools."}}`)
	if err := stream.end(nil, "Tool kasuri/read_file was soft-denied; allow it with permissions.allow"); !errors.Is(err, errAgyToolsDenied) || !strings.Contains(err.Error(), `mcp(kasuri/*)`) {
		t.Fatalf("a soft denial on stderr = %v", err)
	}
	stream, err := decodeAgyAgent(t, 2, agyAgentInit, agyResponseStep(1), agyResponseStep(2), agyResponseStep(3))
	if !errors.Is(err, errStopAgentRun) || !stream.run.TurnLimit || stream.run.Turns != 2 || stream.end(errors.New("killed"), "") != nil {
		t.Fatalf("turn limit = %+v, %v", stream.run, err)
	}
}

func TestAgyAgentRunsGetTheirToolServerAsTheWorkspacesOnly(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 29*time.Minute+30*time.Second)
	defer cancel()
	launch, _, err := prepareAgyAgent(ctx, directory, config.ModelProfile{Provider: config.AgyProvider, Model: "gemini-pro", ReasoningEffort: "high"}, AgentRun{System: "Be careful.", Prompt: "Add Sub.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token", Tools: testTools("read_file", "finish"), ToolTimeout: 4 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"--agent", "kasuri"}, {"--model", "gemini-pro"}, {"--print-timeout", "31m"}, {"--effort", "high"}} {
		if index := slices.Index(launch.args, pair[0]); index < 0 || launch.args[index+1] != pair[1] {
			t.Fatalf("%s is not %q in %q", pair[0], pair[1], launch.args)
		}
	}
	if slices.Contains(launch.args, "--dangerously-skip-permissions") || !strings.Contains(launch.input, `"content":"Add Sub."`) {
		t.Fatalf("launch = %q, input %q", launch.args, launch.input)
	}
	servers, _ := os.ReadFile(filepath.Join(directory, ".agents/mcp_config.json"))
	if string(servers) != `{"mcpServers":{"kasuri":{"enabledTools":["read_file","finish"],"headers":{"Authorization":"Bearer run-token"},"serverUrl":"http://127.0.0.1:9/mcp","timeoutSeconds":240}}}` {
		t.Fatalf("mcp_config.json = %s", servers)
	}
	agent, _ := os.ReadFile(filepath.Join(directory, ".agents/agents/kasuri/agent.md"))
	for _, line := range []string{"tools: []", "subagent: false", "inheritMcp: true", "commandExecutionPolicy: off", "# System Prompt\nBe careful.\n\n# Your tools\nCall each tool with call_mcp_tool, ServerName \"kasuri\"",
		"- read_file: The read_file tool.\n  Arguments schema: {\"type\":\"object\",\"properties\":{\"path\":{\"type\":\"string\"}}}\n", "- finish: The finish tool."} {
		if !strings.Contains(string(agent), line) {
			t.Fatalf("agent.md lacks %q:\n%s", line, agent)
		}
	}
	if agyPrintTimeout(context.Background()) != "120m" {
		t.Fatal("a run without a deadline waits two hours")
	}
}

func TestAnAgyAgentRunEndToEndThroughTheCLI(t *testing.T) {
	profile, capture := cliFixture(t, config.AgyProvider, "agent")
	result, err := NewClient(profile).RunAgent(context.Background(), AgentRun{System: "Be careful.", Prompt: "Add Sub.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token", Tools: testTools("read_file", "edit_file", "finish")})
	if err != nil || result.ToolCalls != 3 || result.Turns != 3 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if !strings.Contains(request.System, "inheritMcp: true") || !strings.Contains(request.Input, "Add Sub.") {
		t.Fatalf("request = %+v", request)
	}
	profile, _ = cliFixture(t, config.AgyProvider, "plan-limit")
	_, err = NewClient(profile).RunAgent(context.Background(), AgentRun{Prompt: "Add Sub.", MCPURL: "http://127.0.0.1:9/mcp", MCPToken: "run-token"})
	var limit *PlanLimitError
	if !errors.As(err, &limit) || limit.Provider != config.AgyProvider || time.Until(limit.ResetsAt) < 89*time.Minute {
		t.Fatalf("plan limit = %v", err)
	}
}
