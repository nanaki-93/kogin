package app

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/toolchain"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

// failureLinePattern matches output lines that say what failed: a failing
// test, a file:line message, a panic or an error.
var failureLinePattern = regexp.MustCompile(`--- FAIL|^\s*FAIL\b|\bFAILED\b|panic:|\bError\b|\berror:|AssertionError|\S+\.[A-Za-z]+:\d+(:\d+)?:`)

// checkFailureExcerpt is what an agent reads of a failed check's output,
// within limit bytes: all of it when it fits; else the lines that say what
// failed, then the end of the output. A long log of the program's own
// output then does not hide which test failed. A Go test binary that exited
// before any test reported a failure is said so.
func checkFailureExcerpt(output string, limit int) string {
	output = strings.TrimSpace(output)
	var notes strings.Builder
	for _, pkg := range toolchain.GoUnreportedFailures(output) {
		fmt.Fprintf(&notes, "Note: the tests of %s exited before any test reported a failure (os.Exit, log.Fatal or a crash during a test); the output just before its FAIL line comes from the test that was running.\n", pkg)
	}
	if len(output) <= limit {
		return notes.String() + output
	}
	tail := textutil.TruncateTail(output, limit/2)
	var marked []string
	for _, line := range strings.Split(output[:len(output)-len(tail)], "\n") {
		if line = strings.TrimRight(line, " \t\r"); failureLinePattern.MatchString(line) && !slices.Contains(marked, line) {
			marked = append(marked, line)
		}
	}
	if len(marked) == 0 {
		return notes.String() + "…\n" + textutil.TruncateTail(output, limit)
	}
	return notes.String() + "Lines that say what failed:\n" + textutil.Truncate(strings.Join(marked, "\n"), limit-len(tail)) + "\n…\nEnd of the output:\n" + tail
}
