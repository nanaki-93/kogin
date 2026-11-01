package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

// ErrAgentRunsUnsupported marks a provider that cannot run a tool-using agent.
var ErrAgentRunsUnsupported = errors.New("this model's provider cannot run a tool-using agent")

// errStopAgentRun ends the CLI process without failing the run: the stream
// decoder has already recorded why (for example, the turn limit).
var errStopAgentRun = errors.New("stop the agent run")

// toolServerName is the name every CLI knows the run's tool server by.
const toolServerName = "kasuri"

// AgentRun is one tool-using model run inside a CLI. The CLI's own tools are
// off; the model's only tools are those of the MCP server the run names.
type AgentRun struct {
	System, Prompt string
	// MCPURL and MCPToken reach the run's tool server.
	MCPURL, MCPToken string
	// Tools are the tool server's tools. CLIs that list a server's tools up
	// front get only these, a CLI that does not show the model their schemas
	// gets them in its prompt, and a call to any other tool ends the run.
	Tools []AgentTool
	// MaxTurns ends the run after that many model turns.
	MaxTurns int
	// ToolTimeout bounds one tool call.
	ToolTimeout time.Duration
}

// AgentTool describes one of the tool server's tools.
type AgentTool struct {
	Name, Description string
	// Schema is the JSON Schema of the tool's arguments.
	Schema json.RawMessage
}

func toolNames(tools []AgentTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// AgentRunResult is what a run did, without its transcript.
type AgentRunResult struct {
	Model string
	// Usage sums the model's turns; UsageKnown is false when a turn did not report it.
	Usage      ChatUsage
	UsageKnown bool
	Turns      int
	ToolCalls  int
	ToolErrors int
	// Final is the model's last text.
	Final string
	// TurnLimit reports that the run stopped at MaxTurns.
	TurnLimit bool
}

const maxAgentOutputBytes = 256 << 20

// SupportsAgentRuns reports whether the provider can run a tool-using agent.
func (c *Client) SupportsAgentRuns() bool {
	switch c.profile.Provider {
	case config.PiProvider, config.ClaudeProvider, config.AgyProvider:
		return true
	default:
		return false
	}
}

// RunAgent runs the model in its CLI until it ends its turn, reaches
// MaxTurns, or ctx ends. The result is returned with an error too when the
// run used the model before failing, so its usage can be charged. A run that
// stops because the subscription plan reached its limit returns a
// *PlanLimitError.
func (c *Client) RunAgent(ctx context.Context, run AgentRun) (*AgentRunResult, error) {
	if !c.SupportsAgentRuns() {
		return nil, ErrAgentRunsUnsupported
	}
	if strings.TrimSpace(run.Prompt) == "" || run.MCPURL == "" || run.MCPToken == "" {
		return nil, fmt.Errorf("llm client: %w: an agent run needs a prompt and a tool server", ErrRequestRejected)
	}
	executable, err := exec.LookPath(c.profile.CLIPath)
	if err != nil {
		return nil, fmt.Errorf("llm client: %w: %s CLI executable is unavailable; install it on the daemon's PATH or set cli_path", ErrRequestRejected, c.profile.Provider)
	}
	directory, err := os.MkdirTemp("", "kasuri-agent-")
	if err != nil {
		return nil, fmt.Errorf("llm client: cannot create private CLI working directory")
	}
	defer os.RemoveAll(directory)
	if run.ToolTimeout <= 0 {
		run.ToolTimeout = 15 * time.Minute
	}
	var launch agentLaunch
	var stream agentStream
	switch c.profile.Provider {
	case config.ClaudeProvider:
		launch, stream, err = prepareClaudeAgent(directory, c.profile, run)
	case config.AgyProvider:
		launch, stream, err = prepareAgyAgent(ctx, directory, c.profile, run)
	default:
		launch, stream, err = preparePiAgent(directory, c.profile, run)
	}
	if err != nil {
		return nil, err
	}
	launch.executable, launch.directory, launch.provider = executable, directory, c.profile.Provider
	result, err := runAgentProcess(ctx, launch, stream)
	if result != nil && run.MaxTurns > 0 && result.Turns >= run.MaxTurns {
		result.TurnLimit = true
	}
	return result, err
}

// agentLaunch is how one CLI is started for an agent run.
type agentLaunch struct {
	executable, directory string
	args, env             []string
	input                 string
	provider              config.ModelProvider
}

// agentStream follows one CLI's event stream during an agent run.
type agentStream interface {
	// event reads one line of the stream. An error ends the run; errStopAgentRun
	// ends it without failing it.
	event(line []byte) error
	// end decides the run's outcome after the CLI exited with exit and wrote
	// stderr. It is not called when the stream itself failed.
	end(exit error, stderr string) error
	result() *AgentRunResult
}

func runAgentProcess(ctx context.Context, launch agentLaunch, stream agentStream) (*AgentRunResult, error) {
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	process := exec.CommandContext(owned, launch.executable, launch.args...)
	process.Dir, process.Env = launch.directory, launch.env
	process.Stdin = strings.NewReader(launch.input)
	// A long run may write more diagnostics than a completion; keep the start.
	stderr := &cliOutputBuffer{limit: 64 * 1024, cancel: cancel, keepHead: true}
	process.Stderr = stderr
	process.WaitDelay = time.Second
	if err := configureCLIProcess(process); err != nil {
		return nil, err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("llm client: cannot read the CLI's output")
	}
	if err := process.Start(); err != nil {
		return nil, cliProcessError(launch.provider, err)
	}
	readErr := readAgentStream(io.LimitReader(stdout, maxAgentOutputBytes), stream, launch.provider)
	if readErr != nil {
		cancel()
	}
	exit := process.Wait()
	var cleanupErr error
	if process.Process != nil {
		if err := process.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = fmt.Errorf("llm client: CLI process-group cleanup failed")
		}
	}
	// A failed cleanup is reported beside the run's own outcome, not instead of it.
	return stream.result(), errors.Join(agentOutcome(ctx, readErr, exit, stream, stderr.buffer.String()), cleanupErr)
}

func agentOutcome(ctx context.Context, readErr, exit error, stream agentStream, stderr string) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("llm client: agent run ended: %w", context.Cause(ctx))
	case errors.Is(readErr, errStopAgentRun):
		return stream.end(nil, stderr)
	case readErr != nil:
		return readErr
	}
	return stream.end(exit, stderr)
}

func readAgentStream(output io.Reader, stream agentStream, provider config.ModelProvider) error {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), maxProviderResponseBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := stream.event(line); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return unusableCLIResponse(fmt.Sprintf("%s's event stream exceeded its limits", provider))
	}
	return nil
}

// ownTool reports whether a CLI's name for a tool names one of the run's tool
// server's tools. CLIs prefix MCP tools with the server's name in their own
// way (mcp__kasuri__read_file, mcp__kasuri__read_file, kasuri/read_file).
// Without a list of tools, any tool under the server's name is the run's.
func ownTool(name string, tools map[string]bool) bool {
	prefixed := false
	for _, server := range []string{toolServerName, strings.ReplaceAll(toolServerName, "-", "_")} {
		if index := strings.LastIndex(name, server); index >= 0 {
			name, prefixed = strings.TrimLeft(name[index+len(server):], "_/:."), true
			break
		}
	}
	if len(tools) == 0 {
		return prefixed && name != ""
	}
	return tools[name]
}

// foreignTool stops a run whose model called a tool that is not the run's,
// naming the tool (bounded and quoted: the model chose it) so the cause shows.
func foreignTool(cli, name string) error {
	label := []rune(strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, name))
	if len(label) > 64 {
		label = append(label[:64], '…')
	}
	return unusableCLIResponse(fmt.Sprintf("%s called %q, a tool outside Kasuri's tool server", cli, string(label)))
}

func toolSet(tools []AgentTool) map[string]bool {
	set := make(map[string]bool, len(tools))
	for _, tool := range tools {
		set[tool.Name] = true
	}
	return set
}
