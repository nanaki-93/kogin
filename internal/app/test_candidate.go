package app

import (
	"go/parser"
	"go/token"
	"path"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
	"github.com/nanaki-93/mini-orca/v2/internal/project/syntax"
)

const maxTestCandidateNameBytes = 200

// validateTestCandidate keeps a proposed test only when it is a test file in
// the target's language by the common conventions, parses, and names its
// test; a Go test must also sit beside the target in its package. Anything
// else is dropped: the risk keeps its other fields.
func validateTestCandidate(candidate *project.TestCandidateSpec, target project.IndexFile, source string) *project.TestCandidateSpec {
	if candidate == nil {
		return nil
	}
	file := path.Clean(strings.TrimPrefix(strings.ReplaceAll(candidate.Path, "\\", "/"), "./"))
	name := strings.TrimSpace(candidate.Name)
	if !candidatePathFits(file, target) || !candidateNamed(name, candidate.Content) {
		return nil
	}
	if !candidateParses(file, candidate.Content) || target.Language == "Go" && !goCandidateFits(file, candidate.Content, target, source) {
		return nil
	}
	return &project.TestCandidateSpec{Path: file, Name: name, Content: candidate.Content}
}

// candidatePathFits keeps a project-relative test file, by the common
// conventions, in the target's language.
func candidatePathFits(file string, target project.IndexFile) bool {
	return file != "." && !path.IsAbs(file) && !strings.HasPrefix(file, "../") && len(file) <= 1024 && file != target.Path &&
		rules.IsConventionalTestFile(file) && symbols.DetectLanguage(file) == target.Language
}

// candidateNamed keeps a one-line name the content spells.
func candidateNamed(name, content string) bool {
	return name != "" && len(name) <= maxTestCandidateNameBytes && !strings.ContainsAny(name, "\n\r") &&
		len(content) <= project.MaxBugTaskCandidateBytes && strings.Contains(content, name)
}

// candidateParses reports a file a grammar reads without a syntax error.
// Tests such as it('…', () => …) are calls, not named functions, so finding
// the name in the content stands for finding the test.
func candidateParses(file, content string) bool {
	parsed := syntax.Analyze(file, []byte(content))
	return parsed.Grammar != "" && parsed.SyntaxError == ""
}

// goCandidateFits keeps a Go test in the target's directory and package (or
// its external _test package).
func goCandidateFits(file, content string, target project.IndexFile, source string) bool {
	if path.Dir(file) != path.Dir(target.Path) {
		return false
	}
	fset := token.NewFileSet()
	test, err := parser.ParseFile(fset, file, content, parser.PackageClauseOnly)
	if err != nil {
		return false
	}
	owner, err := parser.ParseFile(fset, target.Path, source, parser.PackageClauseOnly)
	if err != nil {
		return false
	}
	return test.Name.Name == owner.Name.Name || test.Name.Name == owner.Name.Name+"_test"
}

// anchoredLines finds the line of TARGET_SOURCE a risk's anchor copies,
// comparing with whitespace collapsed; zero when the anchor is not there.
func anchoredLines(source, anchor string) (int, int) {
	parts := strings.Split(strings.TrimSpace(anchor), "\n")
	first := strings.Join(strings.Fields(parts[0]), " ")
	if len(first) < 6 {
		return 0, 0
	}
	lines := strings.Split(source, "\n")
	for index, line := range lines {
		if strings.Contains(strings.Join(strings.Fields(line), " "), first) {
			return index + 1, min(len(lines), index+len(parts))
		}
	}
	return 0, 0
}
