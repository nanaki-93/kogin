package llm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func TestPiAgentRunsOfferOnlyTheRunsToolServer(t *testing.T) {
	directory := t.TempDir()
	launch, _, err := preparePiAgent(directory, config.ModelProfile{Provider: config.PiProvider, Model: "openai-codex/gpt-5.5", ReasoningEffort: "none"}, AgentRun{System: "Be careful.", MCPURL: "http://127.0.0.1:1/mcp", MCPToken: "secret-token", ToolTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	args := launch.args
	if !slices.Contains(launch.env, "KOGIN_MCP_TOKEN=secret-token") || !slices.Contains(launch.env, "KOGIN_TOOL_TIMEOUT=60") {
		t.Fatal("the token and limits reach the extension through its environment")
	}
	for _, pair := range [][2]string{{"--mode", "json"}, {"--tools", "mcp__kogin__*"}, {"--model", "openai-codex/gpt-5.5"}, {"--thinking", "off"}} {
		if index := slices.Index(args, pair[0]); index < 0 || args[index+1] != pair[1] {
			t.Fatalf("%s is not %s in %v", pair[0], pair[1], args)
		}
	}
	for _, flag := range []string{"--no-extensions", "--no-session", "--no-context-files", "--no-skills", "builtin:mcp"} {
		if !slices.Contains(args, flag) {
			t.Fatalf("%s missing from %v", flag, args)
		}
	}
	if excluded := args[slices.Index(args, "--exclude-tools")+1]; !strings.Contains(excluded, "codemode") || !strings.Contains(excluded, "tool_search") {
		t.Fatalf("excluded tools = %s", excluded)
	}
	extension, err := os.ReadFile(filepath.Join(directory, "kogin-tools.ts"))
	if err != nil || !strings.Contains(string(extension), `pi.registerMcpServer("kogin"`) || !strings.Contains(string(extension), `exposure: "direct"`) || !strings.Contains(string(extension), `block: true`) {
		t.Fatalf("extension = %s, %v", extension, err)
	}
	if strings.Contains(string(extension), "KOGIN_MCP_TOKEN=") {
		t.Fatal("the token must come from the environment, not the file")
	}
}

func decodeAgentEvents(t *testing.T, model string, lines ...string) (*piAgentStream, error) {
	t.Helper()
	decoder := &piAgentStream{profile: config.ModelProfile{Provider: config.PiProvider, Model: model}, run: &AgentRunResult{UsageKnown: true}}
	return decoder, readAgentStream(strings.NewReader(strings.Join(lines, "\n")+"\n"), decoder, config.PiProvider)
}

func TestAnAgentRunCountsTurnsToolsAndUsage(t *testing.T) {
	decoder, err := decodeAgentEvents(t, "openai-codex/gpt-5.5",
		`{"type":"session","version":3}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"user","content":"Task"}}`,
		`{"type":"message_end","message":{"role":"assistant","model":"gpt-5.5","stopReason":"toolUse","content":[{"type":"toolCall"}],"usage":{"input":1200,"output":80,"totalTokens":1280}}}`,
		`{"type":"tool_execution_end","toolName":"mcp__kogin__read_file","isError":false}`,
		`{"type":"tool_execution_end","toolName":"mcp__kogin__edit_file","isError":true}`,
		`{"type":"turn_end"}`,
		`{"type":"message_end","message":{"role":"assistant","model":"gpt-5.5","stopReason":"stop","content":[{"type":"text","text":"Done."}],"usage":{"input":1500,"output":20,"totalTokens":1520}}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_end","willRetry":false}`,
	)
	result := decoder.run
	if err != nil || !decoder.started || decoder.failure != nil || result.Turns != 2 || result.ToolCalls != 2 || result.ToolErrors != 1 || result.Final != "Done." {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if result.Usage != (ChatUsage{PromptTokens: 2700, CompletionTokens: 100, TotalTokens: 2800}) || !result.UsageKnown {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestAnAgentRunReportsProviderFailures(t *testing.T) {
	decoder, _ := decodeAgentEvents(t, "gpt-5.5", `{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","model":"gpt-5.5","stopReason":"error","errorMessage":"Your authentication token has expired. Please sign in.","content":[]}}`)
	if !errors.Is(decoder.failure, ErrPiAuthenticationExpired) || decoder.run.UsageKnown {
		t.Fatalf("failure = %v, usage known %v", decoder.failure, decoder.run.UsageKnown)
	}
	if _, err := decodeAgentEvents(t, "gpt-5.5", `{"type":"agent_start"}`, `{"type":"message_end","message":{"role":"assistant","model":"other-model","stopReason":"stop","content":[]}}`); err == nil {
		t.Fatal("a different model was accepted")
	}
	if _, err := decodeAgentEvents(t, "gpt-5.5", `not json`); err == nil {
		t.Fatal("a malformed event was accepted")
	}
	for provider, supported := range map[config.ModelProvider]bool{config.PiProvider: true, config.ClaudeProvider: true, config.AgyProvider: true, config.OpenAIProvider: false, config.GeminiProvider: false} {
		if NewClient(config.ModelProfile{Provider: provider}).SupportsAgentRuns() != supported {
			t.Fatalf("%s runs agents: %v", provider, !supported)
		}
	}
}
