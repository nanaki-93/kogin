package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

const cliFixtureContent = `{"answer":"café 世界"}`

type cliChildCapture struct {
	PID     int
	Address string
}

func serveCLIHelperChild(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	data, _ := json.Marshal(cliChildCapture{PID: os.Getpid(), Address: listener.Addr().String()})
	if err := os.WriteFile(os.Getenv("KASURI_TEST_CAPTURE")+".child", data, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = connection.Close()
	}
}

type cliCapture struct {
	Args        []string
	Directory   string
	PrivateMode uint32
	Input       string
	System      string
	Settings    string
	// AnthropicEnv lists the API-key and cloud settings the CLI could see.
	AnthropicEnv []string
}

func cliFixture(t *testing.T, provider config.ModelProvider, mode string) (config.ModelProfile, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CLI providers require process-group support")
	}
	t.Setenv("KASURI_TEST_CLI", mode)
	t.Setenv("KASURI_TEST_PROVIDER", string(provider))
	capture := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("KASURI_TEST_CAPTURE", capture)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "fake cli")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run '^TestCLIHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return config.ModelProfile{Provider: provider, CLIPath: command, Model: "fixture-model"}, capture
}

func TestCLIHelperProcess(t *testing.T) {
	mode := os.Getenv("KASURI_TEST_CLI")
	if mode == "" {
		return
	}
	if mode == "child" {
		serveCLIHelperChild(t)
		os.Exit(0)
	}
	if mode == "success-child" || mode == "wait-child" {
		child := exec.Command(os.Args[0], "-test.run=^TestCLIHelperProcess$")
		child.Env = append(os.Environ(), "KASURI_TEST_CLI=child")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		waitForCLIFile(t, os.Getenv("KASURI_TEST_CAPTURE")+".child")
	}
	input, _ := io.ReadAll(os.Stdin)
	directory, _ := os.Getwd()
	info, _ := os.Stat(directory)
	agentFile := ".agents/agents/kasuri/agent.md"
	if provider := os.Getenv("KASURI_TEST_PROVIDER"); provider == "pi" || provider == "claude" {
		agentFile = "system.txt"
	}
	system, _ := os.ReadFile(filepath.Join(directory, agentFile))
	settings, _ := os.ReadFile(filepath.Join(directory, ".pi/settings.json"))
	var anthropic []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ANTHROPIC_") || strings.HasPrefix(entry, "CLAUDE_CODE_USE_") {
			anthropic = append(anthropic, entry)
		}
	}
	capture, _ := json.Marshal(cliCapture{os.Args[4:], directory, uint32(info.Mode().Perm()), string(input), string(system), string(settings), anthropic})
	if os.WriteFile(os.Getenv("KASURI_TEST_CAPTURE"), capture, 0600) != nil {
		os.Exit(99)
	}
	fmt.Fprint(os.Stderr, "credential-private-marker source-private-marker")
	switch mode {
	case "catalog":
		if os.Getenv("KASURI_TEST_PROVIDER") == "agy" {
			fmt.Print(agyCatalogFixture)
		} else {
			fmt.Print(piCatalogFixture)
		}
	case "exit":
		os.Exit(7)
	case "wait", "wait-child":
		for {
			time.Sleep(time.Hour)
		}
	case "malformed":
		fmt.Print("credential-private-marker")
	case "oversized":
		fmt.Print(strings.Repeat("x", maxProviderResponseBytes+1))
	case "stderr-limit":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 64*1024+1))
	default:
		fmt.Print(cliFixtureOutput(config.ModelProvider(os.Getenv("KASURI_TEST_PROVIDER")), mode))
		if strings.HasPrefix(mode, "plan-limit") {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

const agyFinishEvents = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"finish","tool_info":{"name":"finish"}}}` + "\n" +
	`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"finish","tool_info":{"name":"finish"}}}` + "\n"

// Pi 1.0 emits its prompt context before the final assistant message.
const piContextEvents = `{"type":"message_end","message":{"role":"system","content":"","sections":{"preamble":"source-private-marker","cwd":"<cwd>\n/private/fixture\n</cwd>"}}}` + "\n" +
	`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"source-private-marker"}]}}` + "\n"

func cliFixtureOutput(provider config.ModelProvider, mode string) string {
	content := cliFixtureContent
	if mode == "empty" {
		content = ""
	}
	switch mode {
	case "fenced-json":
		content = "```json\n" + cliFixtureContent + "\n```"
	case "invalid-json", "native-only":
		content = "Task completed."
	case "invalid-schema":
		content = `{"answer":42}`
	case "null-json":
		content = `null`
	case "extra-property":
		content = `{"answer":"done","toolAction":"Completing task"}`
	}
	if provider == config.ClaudeProvider {
		return claudeFixtureOutput(mode, content)
	}
	if provider == config.AgyProvider && (strings.HasPrefix(mode, "agent") || strings.HasPrefix(mode, "plan-limit")) {
		return agyAgentFixture(mode)
	}
	if provider == config.AgyProvider {
		// Agy reports its global catalog even when the custom agent denies these tools.
		init := `{"event":"init","init":{"agent":"kasuri","model":"fixture-model","tools":["view_file","run_command","finish"]}}` + "\n"
		if mode == "partial" {
			return init
		}
		if mode == "tool" {
			return init + `{"event":"step_update","step_update":{"step_type":"tool","tool_name":"run_command"}}` + "\n"
		}
		status := "SUCCESS"
		if mode == "failed" {
			status = "WAITING"
		}
		payload := map[string]any{"status": status, "response": content, "usage": map[string]int{"input_tokens": 10, "output_tokens": 20, "total_tokens": 30}}
		if mode == "native-only" {
			payload["structured_output"] = json.RawMessage(cliFixtureContent)
		}
		result, _ := json.Marshal(map[string]any{"event": "result", "result": payload})
		if mode == "finish" {
			return init + agyFinishEvents + string(result) + "\n"
		}
		return init + string(result) + "\n"
	}
	start := "{\"type\":\"agent_start\"}\n" + piContextEvents
	if mode == "partial" {
		return start
	}
	if mode == "tool" {
		return start + "{\"type\":\"tool_execution_start\"}\n"
	}
	stop := "stop"
	if mode == "failed" {
		stop = "length"
	}
	failure := ""
	if mode == "auth-expired" {
		stop = "error"
		failure = "Your authentication token has expired. Please try refreshing it. credential-private-marker source-private-marker"
	}
	message, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "model": "fixture-model", "stopReason": stop, "errorMessage": failure, "content": []any{map[string]string{"type": "thinking", "thinking": "not the answer"}, map[string]string{"type": "text", "text": content}}, "usage": map[string]int{"input": 10, "output": 20, "totalTokens": 30}}})
	return start + string(message) + "\n{\"type\":\"agent_end\",\"willRetry\":false}\n"
}

func assertCLIFlags(t *testing.T, provider config.ModelProvider, args []string) {
	t.Helper()
	joined := strings.Join(args, " ")
	flags := []string{"--model fixture-model"}
	if provider == config.PiProvider {
		flags = append(flags, "--print", "--mode json", "--no-tools", "--no-session", "--no-extensions", "--no-skills", "--no-context-files", "--offline", "--thinking high")
	} else {
		flags = append(flags, "--input-format stream-json", "--output-format stream-json", "--agent kasuri", "--disable-slash-commands", "--effort high")
	}
	for _, flag := range flags {
		if !strings.Contains(joined, flag) {
			t.Errorf("missing %s in %q", flag, joined)
		}
	}
}

func readTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func waitForCLIFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && json.Valid(data) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("CLI did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
