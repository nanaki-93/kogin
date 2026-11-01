package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/toolserver"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// The live smoke tests run the Claude Code or agy installed on this machine
// on its signed-in plan, for one small agent run against a tool server whose
// tools only record their calls. They use a little of the plan, so they run
// only when asked:
//
//	KASURI_CLAUDE_SMOKE=1 go test ./internal/app -run TestRealClaude -v
//	KASURI_AGY_SMOKE=gemini-model-slug go test ./internal/app -run TestRealAgy -v
//
// The agy tests also check that agy's own tools stay off.

// smokeSchema is the arguments schema of both smoke tools.
var smokeSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Project-relative path of the file to read."},"summary":{"type":"string","description":"One line on what was done."}},"additionalProperties":false}`)

// smokeToolList describes the smoke tools to the CLI.
var smokeToolList = []llm.AgentTool{
	{Name: "read_file", Description: "Read a file of the task.", Schema: smokeSchema},
	{Name: "finish", Description: "End the task with a summary.", Schema: smokeSchema},
}

// smokeTools serves read_file and finish, recording each call.
func smokeTools(t *testing.T) (*toolserver.Server, func() []string) {
	t.Helper()
	var calls []string
	var mu sync.Mutex
	record := func(name string) func(context.Context, json.RawMessage) (string, error) {
		return func(_ context.Context, _ json.RawMessage) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name)
			if name == "read_file" {
				return "notes.txt:\n1: The answer is 42.", nil
			}
			return "Finished.", nil
		}
	}
	server, err := toolserver.Start(context.Background(), "Smoke test tools.", []toolserver.Tool{
		{Name: "read_file", Description: smokeToolList[0].Description, Schema: smokeSchema, ReadOnly: true, Call: record("read_file")},
		{Name: "finish", Description: smokeToolList[1].Description, Schema: smokeSchema, Call: record("finish")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

// runSmokeAgent runs one small agent run and checks that it used the tool
// server's tools, and only those, and reported its turns and usage. trace,
// when set, shows what the CLI wrote if the run fails.
func runSmokeAgent(t *testing.T, profile config.ModelProfile, trace func()) {
	t.Helper()
	if trace != nil {
		defer func() {
			if t.Failed() {
				trace()
			}
		}()
	}
	server, calls := smokeTools(t)
	client := llm.NewClient(profile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := client.RunAgent(ctx, llm.AgentRun{
		System: "You work only through the tools you are given.", Prompt: "Call read_file with path notes.txt, then call finish with a one-line summary of what it says. Then reply Done.",
		MCPURL: server.URL, MCPToken: server.Token, Tools: smokeToolList, MaxTurns: 8, ToolTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("%s run: %v (result %+v)", profile.Provider, err, result)
	}
	if got := calls(); !slices.Contains(got, "read_file") || !slices.Contains(got, "finish") {
		t.Fatalf("tool server calls = %v", got)
	}
	if result.ToolCalls < 2 || result.Turns < 2 || !result.UsageKnown || result.Usage.TotalTokens == 0 {
		t.Fatalf("result = %+v", result)
	}
	t.Logf("%s: %d turns, %d tool calls, %d tokens, model %s", profile.Provider, result.Turns, result.ToolCalls, result.Usage.TotalTokens, result.Model)
}

func TestRealClaudeCodeRunsOnItsPlanWithOnlyKasurisTools(t *testing.T) {
	if os.Getenv("KASURI_CLAUDE_SMOKE") != "1" {
		t.Skip("set KASURI_CLAUDE_SMOKE=1 to run the installed Claude Code on your Claude plan")
	}
	profile := config.ModelProfile{Provider: config.ClaudeProvider, Model: "haiku", CLIPath: "claude"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	response, err := llm.NewClient(profile).Chat(ctx, []llm.ChatMessage{{Role: "user", Content: "Reply with the single word ok."}})
	if err != nil || !strings.Contains(strings.ToLower(response.Choices[0].Message.Content), "ok") {
		t.Fatalf("completion = %+v, %v", response, err)
	}
	runSmokeAgent(t, profile, nil)
}

func TestRealAgyRunsOnItsPlanWithOnlyKasurisTools(t *testing.T) {
	model := os.Getenv("KASURI_AGY_SMOKE")
	if model == "" {
		t.Skip("set KASURI_AGY_SMOKE to a model slug from agy models to run the installed agy on your Google AI plan")
	}
	cli, trace := tracedCLI(t, "agy")
	runSmokeAgent(t, config.ModelProfile{Provider: config.AgyProvider, Model: model, CLIPath: cli}, trace)
}

// TestRealAgyKeepsItsBuiltInToolsOff asks agy's model to use agy's own file
// or terminal tools. The run's agent allows none, so the model must answer
// without them; a built-in tool call stops the run and fails the test.
func TestRealAgyKeepsItsBuiltInToolsOff(t *testing.T) {
	model := os.Getenv("KASURI_AGY_SMOKE")
	if model == "" {
		t.Skip("set KASURI_AGY_SMOKE to a model slug from agy models to run the installed agy on your Google AI plan")
	}
	cli, trace := tracedCLI(t, "agy")
	defer func() {
		if t.Failed() {
			trace()
		}
	}()
	server, calls := smokeTools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := llm.NewClient(config.ModelProfile{Provider: config.AgyProvider, Model: model, CLIPath: cli}).RunAgent(ctx, llm.AgentRun{
		System: "Answer the user.", Prompt: "List the files in your current directory with your own file or terminal tools (not MCP tools) and say what you see. If you have no such tools, reply exactly NO_BUILTIN_TOOLS.",
		MCPURL: server.URL, MCPToken: server.Token, Tools: smokeToolList, MaxTurns: 4, ToolTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("agy run: %v (result %+v); a call outside Kasuri's server means agy's built-in tools are reachable", err, result)
	}
	if !strings.Contains(result.Final, "NO_BUILTIN_TOOLS") || len(calls()) != 0 {
		t.Fatalf("final = %q, tool server calls %v", result.Final, calls())
	}
}

// tracedCLI wraps an installed CLI so that its output and diagnostics are
// also kept, and returns the wrapper and a func that logs them. The smoke
// runs use only the test's own prompt and tools.
func tracedCLI(t *testing.T, name string) (string, func()) {
	t.Helper()
	real, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s is not installed: %v", name, err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, name+"-traced")
	script := fmt.Sprintf("#!/bin/bash\n%q \"$@\" 2> >(tee %q >&2) | tee %q\nexit ${PIPESTATUS[0]}\n", real, filepath.Join(dir, "stderr.txt"), filepath.Join(dir, "stream.jsonl"))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper, func() {
		for _, file := range []string{"stream.jsonl", "stderr.txt"} {
			data, _ := os.ReadFile(filepath.Join(dir, file))
			t.Logf("--- %s %s:", name, file)
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if len(line) > 600 {
					line = line[:600] + "…"
				}
				t.Log(line)
			}
		}
	}
}
