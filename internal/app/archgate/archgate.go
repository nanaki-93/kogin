package archgate

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/diff"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

const (
	CheckNotRun      = "not-run"
	CheckPassed      = "passed"
	CheckFailed      = "failed"
	CheckSkipped     = "skipped"
	CheckCanceled    = "canceled"
	CheckUnavailable = "unavailable"
)

// CheckResult is one parser, formatter, lint, or test result. Command is a
// display-only argv preview; it is never executed through a shell.
type CheckResult struct {
	Outcome    string   `json:"outcome,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	Name       string   `json:"name"`
	Required   bool     `json:"required"`
	State      string   `json:"state"`
	Command    []string `json:"command,omitempty"`
	Output     string   `json:"output,omitempty"`
	ExitCode   int      `json:"exit_code,omitempty"`
}

// CheckOptions keeps expensive checks explicit. Parsing and formatting
// are required where supported; lint and tests run only when requested.
type CheckOptions struct {
	RunLint  bool `json:"run_lint,omitempty"`
	RunTests bool `json:"run_tests,omitempty"`
	// Focused runs only the tests of the packages the change touches, where
	// the toolchain can (Go), as an issue step's quick check; such checks do
	// not stand for the project's test suite.
	Focused bool `json:"focused,omitempty"`
}

// ChangeTarget captures the pre-edit snapshot of a file in a change session.
type ChangeTarget struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
	// Test marks a test file, by convention or by the test_files patterns of
	// the project's AGENTS.md when the target was captured.
	Test bool `json:"test,omitempty"`
}

// ChangeEdit holds one proposed file edit and its computed unified diff.
type ChangeEdit struct {
	Criteria []int  `json:"criteria,omitempty"`
	Path     string `json:"path"`
	// Status is added, modified, deleted or renamed; empty in proposals
	// stored before it existed, whose edits add or modify their file. A
	// deleted file has no content; a renamed one moved from OldPath.
	Status  string           `json:"status,omitempty"`
	OldPath string           `json:"old_path,omitempty"`
	Content string           `json:"content"`
	Hash    string           `json:"hash"`
	Summary string           `json:"summary,omitempty"`
	Diff    diff.UnifiedDiff `json:"diff"`
}

// Deleted reports an edit that removes its file.
func (edit ChangeEdit) Deleted() bool {
	return edit.Status == "deleted"
}

type parsedEditFacts struct {
	lang       string
	rawImports []string
}

var commonJSRequirePattern = regexp.MustCompile(`\brequire\s*\(\s*['"]([^'"]+)['"]\s*\)`)

// HasGoSourceEdits reports whether any edit in the slice targets a .go file.
func HasGoSourceEdits(edits []ChangeEdit) bool {
	for _, edit := range edits {
		if filepath.Ext(edit.Path) == ".go" {
			return true
		}
	}
	return false
}

// ChangeArchitectureAndComplexityChecks evaluates proposed edits against the
// import rules and budgets the project's AGENTS.md files declare (see
// internal/project/rules). targets hold the files before the task, so budgets
// ratchet: what was already over a budget may stay so if it does not grow. A
// project that declares no import rules gets none.
func ChangeArchitectureAndComplexityChecks(root string, targets []ChangeTarget, edits []ChangeEdit) ([]CheckResult, []BudgetReport) {
	if len(edits) == 0 {
		return nil, nil
	}
	factsByPath := extractAllEditFacts(edits)
	resolver := rules.NewResolver(root)
	checks := []CheckResult{checkArchitectureBoundaries(edits, resolver, factsByPath)}
	reports := BudgetReports(root, targets, edits)
	if changeHasCodeSource(edits, factsByPath) || anyFileLineBudget(reports) {
		checks = append(checks, budgetCheck(reports))
	}
	return checks, reports
}

func anyFileLineBudget(reports []BudgetReport) bool {
	for _, report := range reports {
		if report.Budgets.FileLines > 0 {
			return true
		}
	}
	return false
}

func extractAllEditFacts(edits []ChangeEdit) map[string]parsedEditFacts {
	facts := make(map[string]parsedEditFacts, len(edits))
	for _, edit := range edits {
		if filepath.Ext(edit.Path) == ".go" {
			continue
		}
		lang, rawImports := symbols.ExtractFileFacts(edit.Path, []byte(edit.Content))
		facts[edit.Path] = parsedEditFacts{
			lang:       lang,
			rawImports: rawImports,
		}
	}
	return facts
}

func checkArchitectureBoundaries(edits []ChangeEdit, resolver *rules.Resolver, factsByPath map[string]parsedEditFacts) CheckResult {
	var violations []string
	for _, edit := range edits {
		violations = append(violations, editArchitectureViolations(edit, resolver.For(edit.Path).Imports, factsByPath[edit.Path])...)
	}
	if len(violations) > 0 {
		return CheckResult{
			Name:     "architecture boundaries",
			Required: true,
			State:    CheckFailed,
			Output:   strings.Join(violations, "\n"),
		}
	}
	return CheckResult{
		Name:     "architecture boundaries",
		Required: true,
		State:    CheckPassed,
		Output:   "All proposed imports respect architecture boundaries.",
	}
}

func editArchitectureViolations(edit ChangeEdit, importRules []rules.ImportRule, facts parsedEditFacts) []string {
	slashPath := strings.TrimPrefix(filepath.ToSlash(edit.Path), "./")
	imports := extractEditImports(edit.Path, edit.Content, facts.rawImports)
	if len(imports) == 0 {
		return nil
	}
	var violations []string
	seen := map[string]bool{}
	for _, rule := range importRules {
		if !pathMatchesPrefix(slashPath, rule.From) {
			continue
		}
		for _, imp := range imports {
			for _, forbidden := range rule.Never {
				if importMatchesForbidden(imp, forbidden) {
					msg := fmt.Sprintf("%s imports %q: violates architecture boundary (%s must never import %s)", slashPath, imp, rule.From, forbidden)
					if !seen[msg] {
						seen[msg] = true
						violations = append(violations, msg)
					}
				}
			}
		}
	}
	return violations
}

func pathMatchesPrefix(slashPath, prefix string) bool {
	if prefix == "" {
		return false
	}
	return slashPath == prefix || strings.HasPrefix(slashPath, prefix+"/")
}

func importMatchesForbidden(importPath, forbidden string) bool {
	normImp := strings.Trim(filepath.ToSlash(importPath), "/")
	if normImp == forbidden || strings.HasPrefix(normImp, forbidden+"/") || strings.HasSuffix(normImp, "/"+forbidden) {
		return true
	}
	return strings.Contains(normImp, "/"+forbidden+"/")
}

var rawQuotedImportSpec = regexp.MustCompile(`['"<]([^'">]+)['">]`)

func extractEditImports(path, content string, rawImports []string) []string {
	if filepath.Ext(path) == ".go" {
		return extractGoEditImports(path, content)
	}
	slashDir := filepath.Dir(strings.TrimPrefix(filepath.ToSlash(path), "./"))
	if rawImports == nil {
		_, rawImports = symbols.ExtractFileFacts(path, []byte(content))
	}
	imports := make([]string, 0, len(rawImports)*2)
	appendNorm := func(raw string) {
		if m := rawQuotedImportSpec.FindStringSubmatch(raw); len(m) == 2 {
			spec := strings.ReplaceAll(strings.TrimSpace(m[1]), "\\", "/")
			if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
				resolved := filepath.ToSlash(filepath.Clean(filepath.Join(slashDir, spec)))
				if resolved != "" && resolved != "." {
					imports = append(imports, resolved)
				}
			}
		}
		if norm := symbols.NormalizeGenericImportPath(raw); norm != "" {
			imports = append(imports, norm)
		}
	}
	for _, raw := range rawImports {
		appendNorm(raw)
	}
	for _, m := range commonJSRequirePattern.FindAllStringSubmatch(content, -1) {
		if len(m) == 2 {
			appendNorm(`"` + m[1] + `"`)
		}
	}
	return imports
}

func extractGoEditImports(path, content string) []string {
	file, err := parser.ParseFile(token.NewFileSet(), path, content, parser.ImportsOnly)
	if err != nil || file == nil {
		return nil
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		if spec != nil && spec.Path != nil {
			imports = append(imports, strings.Trim(spec.Path.Value, "`\""))
		}
	}
	return imports
}

func changeHasCodeSource(edits []ChangeEdit, factsByPath map[string]parsedEditFacts) bool {
	if HasGoSourceEdits(edits) {
		return true
	}
	for _, edit := range edits {
		f := factsByPath[edit.Path]
		if f.lang != "" && f.lang != "Markdown" && f.lang != "Text" {
			return true
		}
	}
	return false
}

// dataLanguages have no functions to measure.
var dataLanguages = map[string]bool{
	"Text": true, "Markdown": true, "YAML": true, "JSON": true, "TOML": true, "INI": true, "Config": true,
	"XML": true, "HTML": true, "CSS": true, "SCSS": true, "GraphQL": true, "Protobuf": true,
	"Terraform": true, "HCL": true, "Dockerfile": true, "Makefile": true, "CMake": true,
}

// hasFunctions reports a programming language with functions to measure.
func hasFunctions(lang string) bool {
	return lang != "" && !dataLanguages[lang]
}

// countLines counts lines as editors and wc -l do: a final newline ends the
// last line rather than starting another one.
func countLines(content string) int {
	if content == "" {
		return 0
	}
	lines := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		lines++
	}
	return lines
}
