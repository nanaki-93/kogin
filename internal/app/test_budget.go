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
// file (see newTestFileProblem for when it may), or whose tests grow by
// more than max(120, 4 × changed code lines).
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
// when it may. A test file named after a source file may always be added,
// and so may another: while its directory keeps at most two test files per
// source file, when the change removes at least as many test files there as
// it adds (gathering spread tests), or when the test file it extends is
// near its line budget (x_<behavior>_test.go next to a full x_test.go).
func newTestFileProblem(root, file string, resolver *rules.Resolver, targets []ChangeTarget, edits []ChangeEdit) string {
	stem := testStem(file)
	dir := path.Dir(file)
	if slices.Contains(sharedTestStems, stem) || sourceWithStemExists(root, dir, stem, resolver, edits) {
		return ""
	}
	tests, added, removed := testFilesAfter(root, dir, resolver, targets, edits)
	sources := sourceFilesIn(root, dir, resolver)
	if tests <= max(2, 2*sources) || removed >= added {
		return ""
	}
	home := ownerTestFile(root, dir, stem, resolver)
	if home != "" && nearLineBudget(root, home, resolver, edits) {
		return ""
	}
	if home == "" {
		home = changedCompanionTest(root, edits)
	}
	return fmt.Sprintf("%s would make %d test files for %d source files in %s, and it is not named after a source file; add its tests to %s, the test file of the code this change touches (a second file only once that one is near its line budget).", file, tests, sources, dir, home)
}

// testFilesAfter counts dir's test files as the change leaves them, and the
// test files it adds and removes there. root may hold the change already (a
// worktree) or not (the main checkout).
func testFilesAfter(root, dir string, resolver *rules.Resolver, targets []ChangeTarget, edits []ChangeEdit) (int, int, int) {
	existed := map[string]bool{}
	for _, target := range targets {
		existed[target.Path] = target.Exists
	}
	tests := map[string]bool{}
	for _, test := range testFilesIn(root, dir, resolver) {
		tests[test] = true
	}
	added, removed := 0, 0
	for _, edit := range edits {
		if path.Dir(edit.Path) != dir || !resolver.IsTestFile(edit.Path) {
			continue
		}
		if edit.Deleted() {
			delete(tests, edit.Path)
			removed++
		} else if !existed[edit.Path] && edit.OldPath == "" {
			tests[edit.Path] = true
			added++
		}
	}
	return len(tests), added, removed
}

func changedCompanionTest(root string, edits []ChangeEdit) string {
	for _, edit := range edits {
		if candidate := rules.CompanionTest(root, edit.Path); candidate != "" {
			return candidate
		}
	}
	return ""
}

// ownerTestFile is the companion test of the source file a test file's
// name extends (add_test.go for add_rollback_test.go); empty when none.
func ownerTestFile(root, dir, stem string, resolver *rules.Resolver) string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return ""
	}
	owner, best := "", ""
	for _, entry := range entries {
		file := path.Join(dir, entry.Name())
		if !entry.Type().IsRegular() || resolver.IsTestFile(file) || !hasSourceExtension(file) {
			continue
		}
		if source := testStem(file); strings.HasPrefix(stem, source+"_") && len(source) > len(best) {
			best, owner = source, file
		}
	}
	if owner == "" {
		return ""
	}
	return rules.CompanionTest(root, owner)
}

// nearLineBudget reports a file, as the change leaves it, within a tenth
// of its file line budget or over it.
func nearLineBudget(root, file string, resolver *rules.Resolver, edits []ChangeEdit) bool {
	budget := resolver.For(file).Budgets.FileLines
	if budget <= 0 {
		return false
	}
	content := ""
	if index := slices.IndexFunc(edits, func(edit ChangeEdit) bool { return edit.Path == file }); index >= 0 {
		content = edits[index].Content
	} else if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file))); err == nil {
		content = string(data)
	}
	return fileLineCount(content) >= budget-budget/10
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

// fileLineCount counts a file's lines as file line budgets do.
func fileLineCount(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
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
