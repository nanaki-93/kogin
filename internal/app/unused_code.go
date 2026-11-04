package app

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/reach"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
)

// Code nothing calls is found before the analysis reads the files: each
// file's unused functions become one issue whose fix is to delete them, and
// what the analysis says about code inside them is left out, since fixing
// code that never runs is work for nothing.

// unusedCodeCache keeps the unused code of the latest indexed revision; it
// reads every Go file, so it is found once per revision.
type unusedCodeCache struct {
	mu             sync.Mutex
	root, revision string
	found          []reach.Unused
}

// unusedCode is the indexed project's code nothing calls.
func (s *Service) unusedCode(index *project.ProjectIndex) []reach.Unused {
	root := s.manager.Root()
	cache := &s.unusedCodes
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.root != root || cache.revision != index.ProjectRevision || index.ProjectRevision == "" {
		cache.found = reach.Find(reachFiles(root, index))
		cache.root, cache.revision = root, index.ProjectRevision
	}
	return cache.found
}

// reachFiles are the indexed Go files and go.mod files, as they are now.
func reachFiles(root string, index *project.ProjectIndex) []reach.File {
	resolver := rules.NewResolver(root)
	var files []reach.File
	for _, file := range index.Files {
		if file.Binary || path.Ext(file.Path) != ".go" && path.Base(file.Path) != "go.mod" {
			continue
		}
		info, err := project.GetFileInfo(root, file.Path)
		if err != nil || info.Binary {
			continue
		}
		files = append(files, reach.File{Path: file.Path, Content: []byte(info.Content), Test: resolver.IsTestFile(file.Path)})
	}
	return files
}

// unusedByPath groups unused functions by their file.
func unusedByPath(unused []reach.Unused) map[string][]reach.Unused {
	byPath := map[string][]reach.Unused{}
	for _, fn := range unused {
		byPath[fn.Path] = append(byPath[fn.Path], fn)
	}
	return byPath
}

// insideUnused reports whether a finding is about code in an unused
// function; such a finding is left out of the issues.
func insideUnused(finding project.UnifiedFinding, byPath map[string][]reach.Unused) bool {
	line := finding.Location.StartLine
	for _, fn := range byPath[finding.Location.Path] {
		if line >= fn.StartLine && line <= fn.EndLine || line == 0 && finding.Location.Symbol == fn.Function {
			return true
		}
	}
	return false
}

// withoutUnused drops the findings about code nothing calls.
func withoutUnused(findings []project.UnifiedFinding, byPath map[string][]reach.Unused) []project.UnifiedFinding {
	kept := findings[:0:0]
	for _, finding := range findings {
		if !insideUnused(finding, byPath) {
			kept = append(kept, finding)
		}
	}
	return kept
}

// unusedCodeFindings is one delete issue per file with code nothing calls.
func unusedCodeFindings(unused []reach.Unused, hashes map[string]string) []project.UnifiedFinding {
	byPath := unusedByPath(unused)
	findings := make([]project.UnifiedFinding, 0, len(byPath))
	for file, functions := range byPath {
		findings = append(findings, unusedCodeFinding(file, functions, hashes[file]))
	}
	return findings
}

func unusedCodeFinding(file string, functions []reach.Unused, hash string) project.UnifiedFinding {
	names := make([]string, 0, len(functions))
	var testOnly []string
	for _, fn := range functions {
		names = append(names, fmt.Sprintf("`%s` (line %d)", fn.Function, fn.StartLine))
		if fn.TestOnly {
			testOnly = append(testOnly, "`"+fn.Function+"`")
		}
	}
	message := fmt.Sprintf("%s %s no caller from `main`, `init` or package-level code.", joinNames(names), plural(len(names), "has", "have"))
	impact := "Dead code is read, reviewed and fixed for nothing."
	if len(testOnly) > 0 {
		impact = fmt.Sprintf("Only tests call %s, so they pass on code no command runs.", joinNames(testOnly))
	}
	return project.UnifiedFinding{
		Category:   project.FindingCategoryMaintainability,
		Source:     project.FindingSourceUnused,
		Confidence: project.FindingConfidenceToolReported,
		Severity:   "low",
		Rule:       unusedCodeRule,
		Title:      "Delete unused code in " + file,
		Message:    message,
		Evidence: "Impact: " + impact + "\nFix: Delete them, and the tests that only call them.\n" +
			"How to verify: The project builds and its remaining tests pass.",
		FileHash: hash,
		Location: project.FindingLocation{Path: file, StartLine: functions[0].StartLine, EndLine: functions[0].EndLine, Symbol: functions[0].Function},
	}
}

// unusedCodeRule names the issues of code nothing calls.
const unusedCodeRule = "unused-code"

// unusedCodeCriteria are what a task deleting unused code must do: delete
// the functions its issue names, with the tests that only call them.
func unusedCodeCriteria(finding project.UnifiedFinding, file string) []string {
	var names []string
	for _, match := range backticked.FindAllStringSubmatch(finding.Message, -1) {
		if name := match[1]; name != "main" && name != "init" {
			names = append(names, "`"+name+"`")
		}
	}
	return []string{
		"Delete " + joinNames(names) + " from " + file + ", with the tests that only call them",
		"The project builds and its remaining tests pass; no other function is left without a caller",
	}
}

var backticked = regexp.MustCompile("`([^`]+)`")

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// unusedCodeBrief tells a file's analysis which of its functions nothing
// calls, so it spends no risks on them; empty when there are none.
func unusedCodeBrief(functions []reach.Unused) string {
	if len(functions) == 0 {
		return ""
	}
	names := make([]string, 0, len(functions))
	for _, fn := range functions {
		names = append(names, fmt.Sprintf("`%s` (lines %d-%d)", fn.Function, fn.StartLine, fn.EndLine))
	}
	return "\n\nUNUSED_CODE: nothing calls " + joinNames(names) + "; Kogin reports them as one issue whose fix is to delete them. Report no risks, suggestions or insights about code inside them."
}

// unusedCodeCheckName names the check that a change adds no code nothing calls.
const unusedCodeCheckName = "unused code"

// unusedCodeCheck fails a change that leaves code nothing calls: a function
// it adds without a caller, or one whose last caller it removes. Code that
// was already unused before the change is its own issue, not this change's.
// It runs on Go changes only; nil when the change has none.
func unusedCodeCheck(root string, targets []ChangeTarget, edits []ChangeEdit) *CheckResult {
	if !slices.ContainsFunc(edits, func(edit ChangeEdit) bool { return path.Ext(edit.Path) == ".go" }) {
		return nil
	}
	proposed := projectGoSources(root)
	for _, edit := range edits {
		if edit.Deleted() {
			delete(proposed, edit.Path)
		} else if path.Ext(edit.Path) == ".go" || path.Base(edit.Path) == "go.mod" {
			proposed[edit.Path] = edit.Content
		}
	}
	base := maps.Clone(proposed)
	for _, edit := range edits {
		delete(base, edit.Path)
	}
	for _, target := range targets {
		if target.Exists && (path.Ext(target.Path) == ".go" || path.Base(target.Path) == "go.mod") {
			base[target.Path] = target.Content
		}
	}
	resolver := rules.NewResolver(root)
	before := map[string]bool{}
	for _, fn := range reach.Find(reachSources(base, resolver)) {
		before[fn.Path+"\x00"+fn.Function] = true
	}
	check := &CheckResult{Name: unusedCodeCheckName, Required: true, State: CheckPassed}
	var lines []string
	for _, fn := range reach.Find(reachSources(proposed, resolver)) {
		if !before[fn.Path+"\x00"+fn.Function] {
			lines = append(lines, unusedCodeLine(fn))
		}
	}
	if len(lines) > 0 {
		check.State = CheckFailed
		check.Output = strings.Join(lines, "\n") + "\nCall each where the change needs it, or delete it; a helper only tests use belongs in a _test.go file."
	}
	return check
}

func unusedCodeLine(fn reach.Unused) string {
	line := fmt.Sprintf("%s:%d: %s has no caller from main, init or package-level code", fn.Path, fn.StartLine, fn.Function)
	if fn.TestOnly {
		line += " (only tests call it)"
	}
	return line
}

func reachSources(sources map[string]string, resolver *rules.Resolver) []reach.File {
	files := make([]reach.File, 0, len(sources))
	for file, content := range sources {
		files = append(files, reach.File{Path: file, Content: []byte(content), Test: resolver.IsTestFile(file)})
	}
	return files
}

// projectGoSources are the Go and go.mod files under root, outside hidden,
// vendored and dependency directories.
func projectGoSources(root string) map[string]string {
	sources := map[string]string{}
	_ = filepath.WalkDir(root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if full != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || filepath.Ext(name) != ".go" && name != "go.mod" {
			return nil
		}
		relative, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		if data, err := os.ReadFile(full); err == nil {
			sources[filepath.ToSlash(relative)] = string(data)
		}
		return nil
	})
	return sources
}
