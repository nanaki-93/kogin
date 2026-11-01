package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/archgate"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

const (
	checkCopyBufferSize = 32 * 1024
	MaxOutputBytes      = 8 * 1024
	// CaptureBytes is how much command output is read, so test reports can be
	// parsed; stored diagnostics are still cut to MaxOutputBytes.
	CaptureBytes      = 1024 * 1024
	MaxWorkspaceFiles = 4 * 1024
	MaxWorkspaceBytes = 64 * 1024 * 1024
)

type checkResourceLimits struct {
	maxOutputBytes    int
	maxWorkspaceFiles int
	maxWorkspaceBytes int64
	pipeDrainWait     time.Duration
}

// checkLimits is the single resource budget for copied check workspaces and
// their command diagnostics. A copy that exceeds this budget is not evidence
// for a successful check.
var checkLimits = checkResourceLimits{
	maxOutputBytes:    MaxOutputBytes,
	maxWorkspaceFiles: MaxWorkspaceFiles,
	maxWorkspaceBytes: MaxWorkspaceBytes,
	pipeDrainWait:     250 * time.Millisecond,
}

var (
	checkAuthorization = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\b\s*[:=]\s*[^\r\n]*`)
	checkSecret        = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|password|secret|credential)\b\s*[:=]\s*[^\s,;]+`)
)

type CommandResult struct {
	Output    string
	ExitCode  int
	Truncated bool
}

func ParseGoSource(path, source string) archgate.CheckResult {
	check := archgate.CheckResult{Name: "parse", Required: true}
	if _, err := parser.ParseFile(token.NewFileSet(), path, source, parser.AllErrors); err != nil {
		check.State = archgate.CheckFailed
		check.Output = err.Error()
		return check
	}
	check.State = archgate.CheckPassed
	return check
}

func StopCommandGroup(process *exec.Cmd) error {
	if process == nil || process.Process == nil || process.Cancel == nil {
		return nil
	}
	if err := process.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("process-group cleanup failed: %w", err)
	}
	return nil
}

func RunCommand(ctx context.Context, directory string, command []string) (CommandResult, error) {
	return RunCommandWithStart(ctx, directory, command, false, nil)
}

func RunCommandWithStart(ctx context.Context, directory string, command []string, ownsDescendants bool, beforeStart func() error) (CommandResult, error) {
	return runCommandWithEnvironment(ctx, directory, command, ownsDescendants, beforeStart, ChildEnvironment(os.Environ()))
}

// runCommandWithEnvironment accepts a caller-captured, already-sanitized
// environment when two commands must use the exact same toolchain inputs.
func runCommandWithEnvironment(ctx context.Context, directory string, command []string, ownsDescendants bool, beforeStart func() error, environment []string) (CommandResult, error) {
	if len(command) == 0 {
		return CommandResult{}, fmt.Errorf("empty check command")
	}
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Dir = directory
	process.Env = append([]string(nil), environment...)
	if ownsDescendants {
		if err := ConfigureCommand(process); err != nil {
			return CommandResult{}, err
		}
	}
	process.WaitDelay = checkLimits.pipeDrainWait
	output := NewBoundedOutput(CaptureBytes)
	process.Stdout = output
	process.Stderr = output
	if beforeStart != nil {
		if err := beforeStart(); err != nil {
			return CommandResult{}, err
		}
	}
	err := process.Run()
	if ownsDescendants {
		if cleanupErr := StopCommandGroup(process); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}
	result := CommandResult{Output: output.String(), Truncated: output.Truncated()}
	if exitError, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitError.ExitCode()
	}
	return result, err
}

var checkEnvironmentAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
	"GOROOT": true, "GOPATH": true, "GOCACHE": true, "GOENV": true,
	"CGO_ENABLED": true, "CC": true, "CXX": true, "PKG_CONFIG": true,
	"SYSTEMROOT": true, "SystemRoot": true, "COMSPEC": true, "ComSpec": true,
}

// ChildEnvironment starts from a small toolchain allowlist. In
// particular it never inherits provider credentials or arbitrary host secrets.
func ChildEnvironment(parent []string) []string {
	child := make([]string, 0, len(checkEnvironmentAllowlist))
	for _, entry := range parent {
		name, _, found := strings.Cut(entry, "=")
		if found && checkEnvironmentAllowlist[name] {
			child = append(child, entry)
		}
	}
	return child
}

// BoundedOutput owns both command streams. Write always reports a full
// write so os/exec continues draining either pipe after the diagnostic budget
// is exhausted.
type BoundedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func NewBoundedOutput(limit int) *BoundedOutput {
	return &BoundedOutput{limit: limit}
}

func (output *BoundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - output.buffer.Len()
	if remaining <= 0 {
		output.truncated = true
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = output.buffer.Write(data[:remaining])
		output.truncated = true
		return len(data), nil
	}
	_, _ = output.buffer.Write(data)
	return len(data), nil
}

func (output *BoundedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func (output *BoundedOutput) Truncated() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.truncated
}

func RequiredChecksPassed(checks []archgate.CheckResult) bool {
	for _, check := range checks {
		if check.Required && check.State != archgate.CheckPassed {
			return false
		}
	}
	return true
}

func SanitizeOutput(output string, truncated bool, workspace, root string) string {
	if workspace != "" {
		output = strings.ReplaceAll(output, workspace, "<workspace>")
	}
	if root != "" {
		output = strings.ReplaceAll(output, root, "<project>")
	}
	output = checkAuthorization.ReplaceAllString(output, "[redacted]")
	output = checkSecret.ReplaceAllString(output, "[redacted]")
	if len(output) > checkLimits.maxOutputBytes {
		output = textutil.Truncate(output, checkLimits.maxOutputBytes)
		truncated = true
	}
	if truncated {
		output += "\n[output truncated]"
	}
	return strings.TrimSpace(output)
}
