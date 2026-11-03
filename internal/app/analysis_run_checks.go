package app

import (
	"context"
	"os/exec"
	"slices"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
)

// AnalysisChecksPlan is a run's project checks, run once after its files and
// before the docs: the parser's diagnostics and the AGENTS.md code budgets of
// every file, each subproject's lint and tests from its toolchain profile
// when the project lets checks run its code, and semgrep with the project's
// own rules when semgrep is installed. Their failures become issues.
type AnalysisChecksPlan struct {
	Commands []AnalysisCheckCommand `json:"commands"`
	// Reason says why commands that run the project's code are left out.
	Reason string `json:"reason,omitempty"`
}

// AnalysisCheckCommand is one command of the checks, run in its subproject.
type AnalysisCheckCommand struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Dir     string   `json:"dir,omitempty"`
	Command []string `json:"command"`
}

// Check command kinds; each reports issues under its own source.
const (
	checkKindLint    = "lint"
	checkKindTest    = "test"
	checkKindSemgrep = "semgrep"
)

// AnalysisChecks is what a run's checks did.
type AnalysisChecks struct {
	// Status is completed, canceled or failed (the checks could not start).
	Status string                `json:"status"`
	Checks []AnalysisCheckResult `json:"checks"`
	// Issues counts the issues the checks reported.
	Issues int       `json:"issues"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// AnalysisCheckResult is how one check went; parse and budgets have no
// command.
type AnalysisCheckResult struct {
	Name       string   `json:"name"`
	Command    []string `json:"command,omitempty"`
	State      string   `json:"state"`
	Issues     int      `json:"issues"`
	Output     string   `json:"output,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
}

const (
	checksUntrustedReason = "Allow checks to run project code to include each subproject's lint and tests."
	checksCopyReason      = "The project could not be copied to run its checks."
	maxPlannedChecks      = 64
	maxCheckArguments     = 64
)

var lookPath = exec.LookPath

// planAnalysisChecks plans the checks of a run that goes through its files.
func (s *Service) planAnalysisChecks(ctx context.Context, root, revision string, preview *AnalysisRunPreview) {
	if len(preview.Files) == 0 {
		return
	}
	trusted := false
	if trust, err := s.ExecutionTrust(ctx, revision, ""); err == nil {
		trusted = trust.Trusted
	}
	plan := &AnalysisChecksPlan{Commands: []AnalysisCheckCommand{}}
	for _, profile := range s.projectProfiles(root) {
		for _, command := range []struct {
			kind string
			argv []string
		}{{checkKindLint, profile.Lint}, {checkKindTest, profile.Test}} {
			if len(command.argv) == 0 || len(plan.Commands) == maxPlannedChecks {
				continue
			}
			if toolchain.ExecutesProjectCode(command.argv) && !trusted {
				plan.Reason = checksUntrustedReason
				continue
			}
			name := map[string]string{checkKindLint: "lint", checkKindTest: "tests"}[command.kind]
			plan.Commands = append(plan.Commands, AnalysisCheckCommand{Name: profileCheckName(name, profile), Kind: command.kind, Dir: profile.Dir, Command: command.argv})
		}
	}
	if config := toolchain.SemgrepConfig(root); config != "" && len(plan.Commands) < maxPlannedChecks {
		if _, err := lookPath("semgrep"); err == nil {
			plan.Commands = append(plan.Commands, AnalysisCheckCommand{Name: "semgrep", Kind: checkKindSemgrep, Command: toolchain.SemgrepCommand(config)})
		}
	}
	preview.Checks = plan
}

func validateStoredAnalysisChecks(run *AnalysisRun) error {
	if plan := run.Plan.Checks; plan != nil && !validChecksPlan(plan) {
		return errAnalysisRunCorrupt
	}
	if checks := run.Checks; checks != nil {
		if !oneOfString(checks.Status, "completed", "canceled", "failed") || checks.Issues < 0 || len(checks.Checks) > maxPlannedChecks+2 || !analysisMetadataReason(checks.Reason) {
			return errAnalysisRunCorrupt
		}
		for _, check := range checks.Checks {
			if check.Name == "" || len(check.Name) > 256 || len(check.Command) > maxCheckArguments || check.Issues < 0 || len(check.Output) > 2*maxCheckOutputBytes ||
				!oneOfString(check.State, CheckPassed, CheckFailed, CheckSkipped, CheckCanceled, CheckUnavailable) {
				return errAnalysisRunCorrupt
			}
		}
	}
	return nil
}

func validChecksPlan(plan *AnalysisChecksPlan) bool {
	if len(plan.Commands) > maxPlannedChecks || !analysisMetadataReason(plan.Reason) {
		return false
	}
	for _, command := range plan.Commands {
		if command.Name == "" || len(command.Name) > 256 || len(command.Command) == 0 || len(command.Command) > maxCheckArguments ||
			!oneOfString(command.Kind, checkKindLint, checkKindTest, checkKindSemgrep) {
			return false
		}
	}
	return true
}

func oneOfString(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

// runChecksAfterRun runs the checks at the end of a run that went through
// its files, with the plan the latest start or resume confirmed.
func (s *Service) runChecksAfterRun(ctx context.Context, identity AnalysisRunIdentity) {
	c := s.analysisRun
	c.mu.Lock()
	if c.run == nil || c.run.Identity != identity || c.fault != nil || c.run.Status != AnalysisRunRunning || ctx.Err() != nil || c.fileStopReason != "" || c.admission == nil || c.admission.Checks == nil {
		c.mu.Unlock()
		return
	}
	plan := *c.admission.Checks
	plan.Commands = append([]AnalysisCheckCommand(nil), plan.Commands...)
	revision := c.run.Identity.ProjectRevision
	c.mu.Unlock()
	checks := s.runProjectChecks(ctx, revision, plan)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run != nil && c.run.Identity == identity {
		c.run.Checks = &checks
	}
}
