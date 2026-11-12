// Package rules reads the code rules a project declares in its AGENTS.md
// files: size and complexity budgets, import boundaries and the names of its
// test files. Every directory may have its own AGENTS.md; for one file the
// nearest guide that sets a budget wins, and import rules and test file
// patterns add up from the project root down.
package rules

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DefaultFunctionComplexity applies when no AGENTS.md sets a complexity budget.
const DefaultFunctionComplexity = 15

// maxGuideBytes bounds one AGENTS.md read.
const maxGuideBytes = 256 << 10

// Budgets are the size limits for one file. Zero means no limit.
type Budgets struct {
	FileLines          int `json:"file_lines,omitempty"`
	FunctionLines      int `json:"function_lines,omitempty"`
	FunctionComplexity int `json:"function_complexity,omitempty"`
}

// ImportRule forbids files under From (a project-relative path prefix) to
// import the listed paths or modules.
type ImportRule struct {
	From  string   `json:"from"`
	Never []string `json:"never"`
}

// DeclaredImportRule pairs an ImportRule with the AGENTS.md file that declared it.
type DeclaredImportRule struct {
	From   string   `json:"from"`
	Never  []string `json:"never"`
	Source string   `json:"source"`
}

// Rules are the rules that apply to one file.
type Rules struct {
	Budgets Budgets      `json:"budgets"`
	Imports []ImportRule `json:"imports,omitempty"`
	// Sources lists the AGENTS.md files the rules came from, root first.
	Sources []string `json:"sources,omitempty"`
}

// Checks are the commands an AGENTS.md declares for the subproject rooted at
// its directory. Declared reports whether the field was set; a declared
// empty command means the subproject has no such check.
type Checks struct {
	Test         []string
	TestDeclared bool
	Lint         []string
	LintDeclared bool
}

// ChecksIn returns the check commands the AGENTS.md in dir (project-relative,
// "" for the root) declares, if any.
func (r *Resolver) ChecksIn(dir string) Checks {
	g := r.load(dir)
	if g == nil {
		return Checks{}
	}
	var checks Checks
	if test := g.declared.Checks.Test; test != nil {
		checks.Test, checks.TestDeclared = SplitCommand(*test), true
	}
	if lint := g.declared.Checks.Lint; lint != nil {
		checks.Lint, checks.LintDeclared = SplitCommand(*lint), true
	}
	return checks
}

// SplitCommand splits a declared check command into arguments. It honors
// single and double quotes and refuses shell syntax, since checks run without
// a shell.
func SplitCommand(command string) []string {
	var args []string
	var current strings.Builder
	var quote rune
	inArg := false
	for _, r := range command {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				args = append(args, current.String())
				current.Reset()
				inArg = false
			}
		case strings.ContainsRune("|&;<>()$`\n\\", r):
			return nil
		default:
			current.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil
	}
	if inArg {
		args = append(args, current.String())
	}
	return args
}

// Problem is an AGENTS.md whose rules could not be read.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Resolver reads AGENTS.md files below one project root, each at most once.
type Resolver struct {
	root   string
	mu     sync.Mutex
	guides map[string]*guide
}

type guide struct {
	path      string
	declared  block
	err       error
	testGlobs []*regexp.Regexp
}

// NewResolver reads rules for the project at root.
func NewResolver(root string) *Resolver {
	return &Resolver{root: root, guides: map[string]*guide{}}
}

// For returns the rules for the project-relative file path.
func (r *Resolver) For(file string) Rules {
	result := Rules{Budgets: Budgets{FunctionComplexity: DefaultFunctionComplexity}}
	for _, g := range r.chain(file) {
		result.Sources = append(result.Sources, g.path)
		budgets := g.declared.Budgets
		for _, apply := range []struct {
			declared *int
			target   *int
		}{
			{budgets.FileLines, &result.Budgets.FileLines},
			{budgets.FunctionLines, &result.Budgets.FunctionLines},
			{budgets.FunctionComplexity, &result.Budgets.FunctionComplexity},
		} {
			if apply.declared != nil {
				*apply.target = *apply.declared
			}
		}
		dir := path.Dir(g.path)
		for _, rule := range g.declared.Imports {
			result.Imports = append(result.Imports, ImportRule{From: joinDir(dir, normalizePrefix(rule.From)), Never: normalizePaths(rule.Never)})
		}
	}
	return result
}

// IsConventionalTestFile reports whether the path names a test file by the
// common conventions alone.
func IsConventionalTestFile(file string) bool {
	return matchesAny(conventionalPatterns, strings.TrimPrefix(filepath.ToSlash(file), "./"))
}

// IsTestFile reports whether the project-relative path names a test file by
// common convention or by a test_files pattern of an AGENTS.md above it.
func (r *Resolver) IsTestFile(file string) bool {
	file = strings.TrimPrefix(filepath.ToSlash(file), "./")
	if IsConventionalTestFile(file) {
		return true
	}
	for _, g := range r.chain(file) {
		if matchesAny(g.testGlobs, file) {
			return true
		}
	}
	return false
}

// TestPatterns lists the test file patterns of the project: the common
// conventions, then the test_files patterns of the given AGENTS.md files
// made project-relative.
func (r *Resolver) TestPatterns(guides ...string) []string {
	patterns := append([]string(nil), conventionalTestFiles...)
	seen := map[string]bool{}
	for _, guidePath := range guides {
		g := r.load(path.Dir(strings.TrimPrefix(filepath.ToSlash(guidePath), "./")))
		if g == nil {
			continue
		}
		for _, glob := range g.declared.TestFiles {
			if joined := joinGlob(cleanDir(path.Dir(g.path)), glob); !seen[joined] {
				seen[joined] = true
				patterns = append(patterns, joined)
			}
		}
	}
	return patterns
}

// DeclaredImports lists the project-relative import boundaries declared across
// the given AGENTS.md files, in the order given.
func (r *Resolver) DeclaredImports(guides ...string) []DeclaredImportRule {
	var out []DeclaredImportRule
	for _, guidePath := range guides {
		dir := path.Dir(strings.TrimPrefix(filepath.ToSlash(guidePath), "./"))
		g := r.load(dir)
		if g == nil {
			continue
		}
		for _, rule := range g.declared.Imports {
			out = append(out, DeclaredImportRule{
				From:   joinDir(dir, normalizePrefix(rule.From)),
				Never:  normalizePaths(rule.Never),
				Source: g.path,
			})
		}
	}
	return out
}

// Problems lists the AGENTS.md files read so far whose rules could not be read.
func (r *Resolver) Problems() []Problem {
	r.mu.Lock()
	defer r.mu.Unlock()
	var problems []Problem
	for _, g := range r.guides {
		if g != nil && g.err != nil {
			problems = append(problems, Problem{Path: g.path, Message: g.err.Error()})
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return problems
}

// chain returns the guides from the project root down to file's directory.
func (r *Resolver) chain(file string) []*guide {
	dir := path.Dir(strings.TrimPrefix(filepath.ToSlash(file), "./"))
	var dirs []string
	for {
		dirs = append(dirs, cleanDir(dir))
		if dir == "." || dir == "/" || dir == "" {
			break
		}
		dir = path.Dir(dir)
	}
	var guides []*guide
	for i := len(dirs) - 1; i >= 0; i-- {
		if g := r.load(dirs[i]); g != nil {
			guides = append(guides, g)
		}
	}
	return guides
}

func (r *Resolver) load(dir string) *guide {
	dir = cleanDir(dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.guides[dir]; ok {
		return g
	}
	g := r.read(dir)
	r.guides[dir] = g
	return g
}

func (r *Resolver) read(dir string) *guide {
	if strings.HasPrefix(dir, "..") || strings.TrimSpace(r.root) == "" {
		return nil
	}
	relative := path.Join(dir, "AGENTS.md")
	content, err := readGuide(filepath.Join(r.root, filepath.FromSlash(relative)))
	if os.IsNotExist(err) {
		return nil
	}
	g := &guide{path: relative, err: err}
	if err != nil {
		return g
	}
	g.declared, _, g.err = parseAgents(content)
	for _, glob := range g.declared.TestFiles {
		if compiled, ok := compileGlob(joinGlob(dir, glob)); ok {
			g.testGlobs = append(g.testGlobs, compiled)
		}
	}
	return g
}

func readGuide(file string) (string, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("AGENTS.md is not a regular file")
	}
	handle, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	data, err := io.ReadAll(io.LimitReader(handle, maxGuideBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxGuideBytes {
		return "", fmt.Errorf("AGENTS.md is larger than %d KiB", maxGuideBytes>>10)
	}
	return string(data), nil
}

func cleanDir(dir string) string {
	dir = strings.Trim(path.Clean("/"+dir), "/")
	return dir
}

func joinDir(dir, prefix string) string {
	if dir = cleanDir(dir); dir == "" {
		return prefix
	}
	return path.Join(dir, prefix)
}

var conventionalPatterns = func() []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(conventionalTestFiles))
	for _, glob := range conventionalTestFiles {
		if re, ok := compileGlob(glob); ok {
			compiled = append(compiled, re)
		}
	}
	return compiled
}()

func matchesAny(patterns []*regexp.Regexp, file string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(file) {
			return true
		}
	}
	return false
}
