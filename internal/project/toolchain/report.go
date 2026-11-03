package toolchain

import (
	"regexp"
	"sort"
	"strings"
)

var (
	ansiPattern     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	durationPattern = regexp.MustCompile(`\s*([\(\[]\d+(\.\d+)?\s*(ms|s|sec|secs|seconds)[\)\]]|\s\d+(\.\d+)?\s*m?s)\s*$`)
	goTestFail      = regexp.MustCompile(`^--- FAIL: (\S+)`)
	goPackageResult = regexp.MustCompile(`^(ok|FAIL|\?)\s+(\S+)(\s+\[(build|setup) failed\])?`)
	failurePatterns = []*regexp.Regexp{
		regexp.MustCompile(`^(?:FAILED|ERROR) (\S+::\S+)`),                           // pytest short summary
		regexp.MustCompile(`^(?:FAIL|ERROR): (\S+ \(\S+\))$`),                        // unittest
		regexp.MustCompile(`^test (\S+) \.\.\. FAILED$`),                             // cargo
		regexp.MustCompile(`^\s*FAIL {1,2}(\S.*?)\s*$`),                              // jest and vitest files and tests
		regexp.MustCompile(`^\s*● (.+?)\s*$`),                                        // jest test headers
		regexp.MustCompile(`^\s*[×✕] (.+?)\s*$`),                                     // vitest and jest test lines
		regexp.MustCompile(`^\[ERROR\]\s+([A-Za-z_][\w$]*(?:\.[\w$]+)+)(?::\d+)?\b`), // Maven Surefire
		regexp.MustCompile(`^(\S+ > .+) FAILED$`),                                    // Gradle
		regexp.MustCompile(`^\s*Failed (\S+) \[`),                                    // dotnet test
		regexp.MustCompile(`^\s+\d+\) (.+?)\s*$`),                                    // mocha
	}
)

// Failures names the tests a test run reports as failing, from the output of
// go test, pytest, unittest, cargo, jest, vitest, mocha, Maven, Gradle or
// dotnet. A Go package that does not build is named by its import path. It
// returns nothing when the output names no failure it can read.
func Failures(output string) []string {
	seen := map[string]bool{}
	var pendingGo []string
	for _, raw := range strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n") {
		line := strings.TrimRight(raw, "\r")
		if match := goTestFail.FindStringSubmatch(line); match != nil {
			pendingGo = append(pendingGo, match[1])
			continue
		}
		if match := goPackageResult.FindStringSubmatch(line); match != nil && strings.Contains(line, "\t") {
			if match[1] == "FAIL" {
				if len(pendingGo) == 0 {
					seen[match[2]] = true
				}
				for _, test := range pendingGo {
					seen[match[2]+"."+test] = true
				}
			}
			pendingGo = nil
			continue
		}
		for _, pattern := range failurePatterns {
			if match := pattern.FindStringSubmatch(line); match != nil {
				if name := strings.TrimSpace(durationPattern.ReplaceAllString(match[1], "")); name != "" {
					seen[name] = true
				}
				break
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GoUnreportedFailures names the Go packages whose tests failed without any
// test reporting a failure: the test binary exited (os.Exit, log.Fatal) or
// crashed while a test ran. A package that did not build is not one.
func GoUnreportedFailures(output string) []string {
	var names []string
	reported := false
	for _, raw := range strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n") {
		line := strings.TrimRight(raw, "\r")
		if goTestFail.MatchString(line) {
			reported = true
			continue
		}
		if match := goPackageResult.FindStringSubmatch(line); match != nil && strings.Contains(line, "\t") {
			if match[1] == "FAIL" && !reported && match[3] == "" {
				names = append(names, match[2])
			}
			reported = false
		}
	}
	return names
}

// NewFailures returns the failures of after that before does not have.
func NewFailures(before, after []string) []string {
	known := make(map[string]bool, len(before))
	for _, name := range before {
		known[name] = true
	}
	var added []string
	for _, name := range after {
		if !known[name] {
			added = append(added, name)
		}
	}
	return added
}

var diagnosticPattern = regexp.MustCompile(`^\s*(?:\./)?([^\s:]+\.[A-Za-z0-9]+):\d+(?::\d+)?:?\s+(.+?)\s*$`)

// Diagnostics reads "file:line[:col]: message" lines, as linters and
// compilers print them, without their line numbers: a finding that only
// moved because code above it changed is the same finding.
func Diagnostics(output string) []string {
	seen := map[string]bool{}
	for _, line := range strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n") {
		if match := diagnosticPattern.FindStringSubmatch(strings.TrimRight(line, "\r")); match != nil {
			seen[match[1]+": "+match[2]] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
