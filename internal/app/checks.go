package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/archgate"
	"github.com/nanaki-93/kogin/v2/internal/app/sandbox"
)

const (
	CheckNotRun      = archgate.CheckNotRun
	CheckPassed      = archgate.CheckPassed
	CheckFailed      = archgate.CheckFailed
	CheckSkipped     = archgate.CheckSkipped
	CheckCanceled    = archgate.CheckCanceled
	CheckUnavailable = archgate.CheckUnavailable
)

type CheckResult = archgate.CheckResult
type CheckOptions = archgate.CheckOptions

func parseGoSource(path, source string) CheckResult {
	return sandbox.ParseGoSource(path, source)
}

func (s *Service) runCheck(ctx context.Context, workspace, revision, name string, required bool, command []string, failOnOutput, executesProjectCode bool) CheckResult {
	check, _ := s.runCheckWithOutput(ctx, workspace, revision, name, required, command, failOnOutput, executesProjectCode)
	return check
}

// runCheckWithOutput also returns the command's whole captured output, for
// reading test reports; the check keeps the sanitized, shortened copy.
func (s *Service) runCheckWithOutput(ctx context.Context, workspace, revision, name string, required bool, command []string, failOnOutput, executesProjectCode bool) (CheckResult, string) {
	check, raw, err := s.executeCheck(ctx, workspace, revision, CheckResult{Name: name, Required: required, Command: append([]string(nil), command...)}, executesProjectCode)
	return finishCheck(check, err, failOnOutput, workspace, s.manager.Root()), raw
}

func finishCheck(check CheckResult, err error, failOnOutput bool, workspace, root string) CheckResult {
	if check.State == CheckCanceled {
		return check
	}
	if err != nil || failOnOutput && check.Output != "" {
		check.State = CheckFailed
		if check.Output == "" && err != nil {
			check.Output = sanitizeCheckOutput(err.Error(), false, workspace, root)
		}
		return check
	}
	check.State = CheckPassed
	return check
}

func (s *Service) executeCheck(ctx context.Context, workspace, revision string, check CheckResult, executesProjectCode bool) (CheckResult, string, error) {
	if err := ctx.Err(); err != nil {
		check.State = CheckCanceled
		check.Output = err.Error()
		return check, "", err
	}
	started := time.Now()
	timed, cancel := context.WithTimeout(ctx, s.conf().focusedCheckTimeout)
	defer cancel()
	result, err := s.runCheckCommand(timed, workspace, check.Command, revision, executesProjectCode)
	check.Output = sanitizeCheckOutput(result.Output, result.Truncated, workspace, s.manager.Root())
	check.ExitCode = result.ExitCode
	check.DurationMS = time.Since(started).Milliseconds()
	if timed.Err() != nil {
		check.State = CheckCanceled
		check.Output = timed.Err().Error()
		if ctx.Err() == nil && timed.Err() == context.DeadlineExceeded {
			check.Output = fmt.Sprintf("%s timed out after %s. Raise the check time limit in Settings → Models & agents and retry checks; no code failure was established.", check.Name, s.conf().focusedCheckTimeout)
		}
		return check, result.Output, timed.Err()
	}
	return check, result.Output, err
}

func (s *Service) runCheckCommand(ctx context.Context, workspace string, command []string, revision string, executesProjectCode bool) (sandbox.CommandResult, error) {
	if !executesProjectCode {
		return sandbox.RunCommand(ctx, workspace, command)
	}
	return sandbox.RunCommandWithStart(ctx, workspace, command, true, func() error {
		return s.requireProjectExecutionTrust(ctx, revision)
	})
}

func requiredChecksPassed(checks []CheckResult) bool {
	return sandbox.RequiredChecksPassed(checks)
}

func sanitizeCheckOutput(output string, truncated bool, workspace, root string) string {
	return sandbox.SanitizeOutput(output, truncated, workspace, root)
}

func copyCheckWorkspace(ctx context.Context, source, destination string) error {
	return sandbox.CopyWorkspace(ctx, source, destination)
}
