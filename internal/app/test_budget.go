package app

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/diff"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
)

// testBudgetCheckName names the check that a change's tests stay in
// proportion to its code: each fix adding its own test files and growing
// tests faster than code is how a small project ends up with more tests to
// review than code.
const testBudgetCheckName = "test budget"

const (
	// minTestLineBudget is the test growth any change may have.
	minTestLineBudget = 120
	// testLinesPerCodeLine is how many added test lines one changed code
	// line pays for.
	testLinesPerCodeLine = 4
)

// testBudgetCheck fails a change that adds a test file not named after a
// source file to a directory that already has two test files per source
// file, or whose tests grow by more than max(120, 4 × changed code lines).
// nil when the change has no test edits.
func testBudgetCheck(root string, targets []ChangeTarget, edits []ChangeEdit) *CheckResult {
	resolver := rules.NewResolver(root)
	before := map[string]ChangeTarget{}
	for _, target := range targets {
		before[target.Path] = target
	}
	var problems []string
	growth, codeLines, touchesTests := 0, 0, false
	for _, edit := range edits {
		target := before[edit.Path]
		old := target.Content
		if edit.OldPath != "" {
			old = before[edit.OldPath].Content
		}
		if !resolver.IsTestFile(edit.Path) && !(edit.OldPath != "" && resolver.IsTestFile(edit.OldPath)) {
			codeLines += changedLines(edit.Path, old, edit.Content)
			continue
		}
		touchesTests = true
		growth += codeLineCount(edit.Content) - codeLineCount(old)
		if !edit.Deleted() && !target.Exists && edit.OldPath == "" {
			if problem := newTestFileProblem(root, edit.Path, resolver, targets, edits); problem != "" {
				problems = append(problems, problem)
			}
		}
	}
	if !touchesTests {
		return nil
	}
	check := &CheckResult{Name: testBudgetCheckName, Required: true, State: CheckPassed}
	if limit := max(minTestLineBudget, testLinesPerCodeLine*codeLines); growth > limit {
		problems = append(problems, fmt.Sprintf("Tests grow by %d lines for %d changed lines of code (at most %d). Keep the tests that prove this change: one that fails without it, as a new case of an existing table where there is one; drop cases other tests already pin.", growth, codeLines, limit))
	}
	if len(problems) > 0 {
		check.State, check.Output = CheckFailed, strings.Join(problems, "\n")
	}
	return check
}

// newTestFileProblem explains why a new test file should not exist; empty
// when it may. A test file named after a source file may always be added;
// another may while its directory keeps at most two test files per source
// file, so tests stay where a reader looks for them.
func newTestFileProblem(root, file string, resolver *rules.Resolver, targets []ChangeTarget, edits []ChangeEdit) string {
	stem := testStem(file)
	dir := path.Dir(file)
	if slices.Contains(sharedTestStems, stem) || sourceWithStemExists(root, dir, stem, resolver, edits) {
		return ""
	}
	// root may hold the change already (a worktree): files it adds are not
	// the directory's existing tests.
	added := func(test string) bool {
		return slices.ContainsFunc(targets, func(target ChangeTarget) bool { return target.Path == test && !target.Exists })
	}
	existing := slices.DeleteFunc(testFilesIn(root, dir, resolver), added)
	tests := len(existing)
	for _, edit := range edits {
		if !edit.Deleted() && path.Dir(edit.Path) == dir && resolver.IsTestFile(edit.Path) && !slices.Contains(existing, edit.Path) {
			tests++
		}
	}
	sources := sourceFilesIn(root, dir, resolver)
	if len(existing) == 0 || tests <= max(2, 2*sources) {
		return ""
	}
	home := existing[0]
	for _, edit := range edits {
		if candidate := rules.CompanionTest(root, edit.Path); candidate != "" && slices.Contains(existing, candidate) {
			home = candidate
			break
		}
	}
	return fmt.Sprintf("%s would make %d test files for %d source files in %s, and it is not named after a source file; add its tests to %s, the test file of the code this change touches.", file, tests, sources, dir, home)
}

func sourceFilesIn(root, dir string, resolver *rules.Resolver) int {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		file := path.Join(dir, entry.Name())
		if entry.Type().IsRegular() && !resolver.IsTestFile(file) && hasSourceExtension(file) {
			count++
		}
	}
	return count
}

// hasSourceExtension reports a file a test file can be named after.
func hasSourceExtension(file string) bool {
	switch strings.ToLower(path.Ext(file)) {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".java", ".kt", ".scala", ".rb", ".rs", ".cs", ".swift", ".php", ".c", ".cc", ".cpp", ".h", ".hpp":
		return true
	}
	return false
}

// sharedTestStems name test files that serve a whole package rather than
// one source file.
var sharedTestStems = []string{"main", "example", "examples", "integration", "e2e", "export", "helpers", "helper", "testutil", "fixtures", "setup", "conftest", "benchmark", "bench", "fuzz"}

// testStem is a file's name without its extension and test markers:
// utils_test.go, test_utils.py, utils.test.ts and UtilsTest.java are "utils".
func testStem(file string) string {
	name := path.Base(file)
	name = strings.TrimSuffix(name, path.Ext(name))
	for _, suffix := range []string{"_test", ".test", ".spec", "_spec", "Tests", "Test"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.ToLower(strings.TrimPrefix(name, "test_"))
}

func testFilesIn(root, dir string, resolver *rules.Resolver) []string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil
	}
	var tests []string
	for _, entry := range entries {
		file := path.Join(dir, entry.Name())
		if entry.Type().IsRegular() && resolver.IsTestFile(file) {
			tests = append(tests, file)
		}
	}
	return tests
}

// sourceWithStemExists reports a non-test file named stem in dir on disk,
// or anywhere in the change (for tests kept in their own directory).
func sourceWithStemExists(root, dir, stem string, resolver *rules.Resolver, edits []ChangeEdit) bool {
	for _, edit := range edits {
		if !edit.Deleted() && !resolver.IsTestFile(edit.Path) && testStem(edit.Path) == stem {
			return true
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		file := path.Join(dir, entry.Name())
		if entry.Type().IsRegular() && !resolver.IsTestFile(file) && testStem(file) == stem {
			return true
		}
	}
	return false
}

// changedLines counts the lines a change adds or removes in one file.
func changedLines(file, before, after string) int {
	count := 0
	for _, line := range diff.BuildUnifiedDiff(file, before, after).Lines {
		if line.Kind != "context" && strings.TrimSpace(line.Text) != "" {
			count++
		}
	}
	return count
}

// codeLineCount counts a file's non-blank lines.
func codeLineCount(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
