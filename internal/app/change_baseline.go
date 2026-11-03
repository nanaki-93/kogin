package app

import (
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
)

// classifyRegressionBaseline records what new tests did on the unmodified
// project, from the raw state of that run. Baseline observations do not
// replace candidate checks or prove a root cause. reproduced reports whether a
// failed run shows the new tests themselves failing.
func classifyRegressionBaseline(check *CheckResult, reproduced func(CheckResult) bool) {
	check.Required = false
	switch {
	case check.State == CheckCanceled:
		check.Outcome = "could_not_execute"
	case check.State == CheckPassed && check.ExitCode == 0:
		check.State, check.Outcome = CheckSkipped, "already_passes"
	case check.State == CheckFailed && reproduced(*check):
		check.State, check.Outcome = CheckPassed, "failed_before_change"
	default:
		check.State, check.Outcome = CheckUnavailable, "could_not_execute"
	}
}

// newTestsReproduced reports whether a run of the old code with the new tests
// added shows those tests failing. A Go package that does not build is not a
// reproduction: missing new APIs make feature tests fail to compile before
// the implementation exists. Failures that already happened without the new
// tests (known) do not count. When the output names no failure, the failed
// run is the only evidence there is, and it counts.
func newTestsReproduced(known []string, output string) bool {
	if strings.Contains(output, "[build failed]") {
		return false
	}
	failures := toolchain.Failures(output)
	if len(failures) == 0 {
		return true
	}
	return len(toolchain.NewFailures(known, failures)) > 0
}
