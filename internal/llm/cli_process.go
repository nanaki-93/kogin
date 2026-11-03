package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

var errCLIOutputLimit = errors.New("CLI output exceeded its byte limit")

type cliOutputBuffer struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy bypass
	// Write and the output limit when exec drains a subprocess pipe.
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
	// keepHead keeps the first limit bytes and drops the rest instead of
	// ending the process.
	keepHead bool
}

func (b *cliOutputBuffer) Write(data []byte) (int, error) {
	if room := b.limit - b.buffer.Len(); len(data) > room {
		b.exceeded = true
		if b.keepHead {
			b.buffer.Write(data[:room])
			return len(data), nil
		}
		b.cancel()
		return 0, errCLIOutputLimit
	}
	return b.buffer.Write(data)
}

func runCLI(ctx context.Context, profile config.ModelProfile, invocation cliInvocation) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath(profile.CLIPath)
	if err != nil {
		return nil, fmt.Errorf("llm client: %w: %s CLI executable is unavailable; install it on the daemon's PATH or set cli_path", ErrRequestRejected, profile.Provider)
	}
	timed, cancel := providerRequestContext(ctx)
	defer cancel()
	process := exec.CommandContext(timed, executable, invocation.args...)
	process.Dir, process.Env = invocation.directory, cliEnvironment(profile.Provider, nil)
	process.Stdin = strings.NewReader(invocation.input)
	stdout := &cliOutputBuffer{limit: maxProviderResponseBytes, cancel: cancel}
	stderr := &cliOutputBuffer{limit: 64 * 1024, cancel: cancel}
	process.Stdout, process.Stderr = stdout, stderr
	process.WaitDelay = time.Second
	if err := configureCLIProcess(process); err != nil {
		return nil, err
	}
	err = process.Run()
	// Also stop inherited children after a normal exit; they cannot outlive a request.
	if process.Process != nil {
		if cleanupErr := process.Cancel(); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrProcessDone) {
			return nil, fmt.Errorf("llm client: CLI process-group cleanup failed")
		}
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, fmt.Errorf("llm client: %w: CLI output exceeds byte limit", ErrUnusableResponse)
	}
	if timed.Err() != nil {
		return nil, fmt.Errorf("llm client: CLI request ended: %w", timed.Err())
	}
	if err != nil {
		if limit := reportedPlanLimit(profile, stdout.buffer.Bytes(), stderr.buffer.String()); limit != nil {
			return nil, limit
		}
		return nil, cliProcessError(profile.Provider, err)
	}
	return stdout.buffer.Bytes(), nil
}

// reportedPlanLimit finds a plan limit that a failed CLI reported in its
// event stream or, failing that, in its diagnostics.
func reportedPlanLimit(profile config.ModelProfile, stdout []byte, stderr string) *PlanLimitError {
	var limit *PlanLimitError
	if _, err := decodeCLIResponse(profile, stdout); errors.As(err, &limit) {
		return limit
	}
	return planLimitFromText(profile.Provider, stderr)
}

// cliEnvironment is the environment a CLI runs with: the daemon's, plus
// extra. Claude Code would bill an API key or a cloud account if one were in
// the environment, so those settings are removed: it runs on its plan login.
func cliEnvironment(provider config.ModelProvider, extra []string) []string {
	if provider != config.ClaudeProvider && len(extra) == 0 {
		return nil
	}
	environment := os.Environ()
	if provider == config.ClaudeProvider {
		environment = slices.DeleteFunc(environment, func(entry string) bool {
			name, _, _ := strings.Cut(entry, "=")
			return strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_USE_") || name == "AWS_BEARER_TOKEN_BEDROCK"
		})
	}
	return append(environment, extra...)
}

func cliProcessError(provider config.ModelProvider, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("llm client: %s CLI exited with status %d; check its login, model, and supported CLI options", provider, exit.ExitCode())
	}
	// Subprocess errors and stderr may contain source, credentials or private paths.
	return fmt.Errorf("llm client: %w: %s CLI could not complete; check its installation and permissions", ErrRequestRejected, provider)
}
