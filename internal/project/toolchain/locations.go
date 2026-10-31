package toolchain

import (
	"regexp"
	"strconv"
	"strings"
)

// Location is one place a linter, compiler or test run points at. Path is as
// the tool printed it: relative to the directory it ran in, or absolute.
type Location struct {
	Path    string
	Line    int
	Message string
}

const maxLocationMessageBytes = 300

var (
	// path:line[:col][:] message — go vet, gcc, ruff, flake8, mypy, pylint,
	// rubocop, golangci-lint, eslint's unix format.
	lineDiagnostic = regexp.MustCompile(`^\s*(?:\./)?([^\s:()]+\.[A-Za-z0-9]+):(\d+)(?::\d+)?:?\s+(.+?)\s*$`)
	// path(line,col): message — tsc and MSBuild.
	parenDiagnostic = regexp.MustCompile(`^\s*([^\s()]+\.[A-Za-z0-9]+)\((\d+)(?:,\d+)?\):\s*(.+?)\s*$`)
	// eslint's default format: the file on its own line, then "line:col
	// severity message rule" lines under it.
	eslintFile  = regexp.MustCompile(`^(/?[^\s:]+\.[A-Za-z0-9]+)\s*$`)
	eslintEntry = regexp.MustCompile(`^\s+(\d+):\d+\s+(?:error|warning)\s+(.+?)\s*$`)
	// rustc and clippy: "error[E0308]: message", then "  --> path:line:col".
	rustHeader = regexp.MustCompile(`^(?:error|warning)(?:\[[^\]]+\])?: (.+?)\s*$`)
	rustArrow  = regexp.MustCompile(`^\s*--> ([^\s:]+\.[A-Za-z0-9]+):(\d+)(?::\d+)?\s*$`)
	// Anywhere in a line, as test runners print a failing assertion.
	anyLocation = regexp.MustCompile(`([A-Za-z0-9_./@+\-]+\.[A-Za-z0-9]+):(\d+)`)
)

// Locations reads the diagnostics in a linter's or compiler's output, in the
// formats most of them print, with each one's line. Lines it cannot read are
// skipped.
func Locations(output string) []Location {
	var found []Location
	eslintPath, rustMessage := "", ""
	for _, raw := range strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n") {
		line := strings.TrimRight(raw, "\r")
		if match := rustArrow.FindStringSubmatch(line); match != nil && rustMessage != "" {
			found = append(found, location(match[1], match[2], rustMessage))
			rustMessage = ""
			continue
		}
		if match := rustHeader.FindStringSubmatch(line); match != nil {
			rustMessage = match[1]
			continue
		}
		if match := eslintEntry.FindStringSubmatch(line); match != nil && eslintPath != "" {
			found = append(found, location(eslintPath, match[1], match[2]))
			continue
		}
		if match := eslintFile.FindStringSubmatch(line); match != nil {
			eslintPath = match[1]
			continue
		}
		if match := lineDiagnostic.FindStringSubmatch(line); match != nil {
			found = append(found, location(match[1], match[2], match[3]))
		} else if match := parenDiagnostic.FindStringSubmatch(line); match != nil {
			found = append(found, location(match[1], match[2], match[3]))
		}
		if strings.TrimSpace(line) == "" {
			eslintPath = ""
		}
	}
	return found
}

// FailureLocations lists the places a test run's output points at for one
// failing test, best first: the file a pytest-style name carries, then the
// file:line mentions on the lines that follow the test's name.
func FailureLocations(output, name string) []Location {
	var found []Location
	if file, _, ok := strings.Cut(name, "::"); ok {
		found = append(found, Location{Path: file})
	}
	lines := strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n")
	if index := firstMention(lines, name); index >= 0 {
		for _, near := range lines[index:min(len(lines), index+failureLines)] {
			for _, match := range anyLocation.FindAllStringSubmatch(near, -1) {
				found = append(found, location(match[1], match[2], ""))
			}
		}
	}
	return found
}

// failureLines is how much output after a failing test's name is its own.
const failureLines = 20

// FailureOutput is the part of a test run's output about one failing test:
// the lines from the first that names it.
func FailureOutput(output, name string) string {
	lines := strings.Split(ansiPattern.ReplaceAllString(output, ""), "\n")
	index := firstMention(lines, name)
	if index < 0 {
		return ""
	}
	return strings.Join(lines[index:min(len(lines), index+failureLines)], "\n")
}

// firstMention finds the first line naming a test, by its whole name or
// its last part (go test prints TestX for example.com/app.TestX).
func firstMention(lines []string, name string) int {
	short := ""
	if index := strings.LastIndex(name, "."); index >= 0 && !strings.ContainsAny(name, " :") {
		short = name[index+1:]
	}
	for index, line := range lines {
		if strings.Contains(line, name) || short != "" && strings.Contains(line, short) {
			return index
		}
	}
	return -1
}

func location(path, line, message string) Location {
	number, _ := strconv.Atoi(line)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxLocationMessageBytes {
		message = strings.ToValidUTF8(message[:maxLocationMessageBytes], "") + "…"
	}
	return Location{Path: strings.TrimPrefix(path, "./"), Line: number, Message: message}
}
