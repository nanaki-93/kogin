package archgate

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
	"github.com/nanaki-93/kasuri/v2/internal/project/symbols"
	"github.com/nanaki-93/kasuri/v2/internal/project/syntax"
)

// Budget kinds.
const (
	BudgetFileLines          = "file_lines"
	BudgetFunctionLines      = "function_lines"
	BudgetFunctionComplexity = "function_complexity"
)

// BudgetFinding is one file or function over a budget.
type BudgetFinding struct {
	Kind     string `json:"kind"`
	Function string `json:"function,omitempty"`
	Line     int    `json:"line,omitempty"`
	Value    int    `json:"value"`
	// Base is the value before the task; zero for a new file or function.
	Base  int `json:"base,omitempty"`
	Limit int `json:"limit"`
}

func (f BudgetFinding) String() string {
	before := ""
	if f.Base > 0 && f.Base != f.Value {
		before = fmt.Sprintf(", %d before this task", f.Base)
	}
	switch f.Kind {
	case BudgetFileLines:
		return fmt.Sprintf("file has %d lines (max %d%s)", f.Value, f.Limit, before)
	case BudgetFunctionLines:
		return fmt.Sprintf("line %d: function %s has %d lines (max %d%s)", f.Line, f.Function, f.Value, f.Limit, before)
	default:
		return fmt.Sprintf("line %d: function %s has cyclomatic complexity %d (max %d%s)", f.Line, f.Function, f.Value, f.Limit, before)
	}
}

// BudgetReport is how one proposed file stands against the budgets its
// AGENTS.md files declare. Budgets ratchet: a task must keep what it writes
// within them, while a file or function that was already over a budget may
// stay over it as long as the task does not make it worse.
type BudgetReport struct {
	Path    string        `json:"path"`
	Budgets rules.Budgets `json:"budgets"`
	Lines   int           `json:"lines"`
	// BaseLines is the file's length before the task; zero for a new file.
	BaseLines int `json:"base_lines,omitempty"`
	// NotChecked says why the file's functions could not be measured.
	NotChecked string `json:"not_checked,omitempty"`
	// Over is what the task wrote over a budget; the task must fix it.
	Over []BudgetFinding `json:"over,omitempty"`
	// Debt was over a budget before the task and did not grow.
	Debt []BudgetFinding `json:"debt,omitempty"`
}

// OverFileLines reports whether the task made the file longer than its budget.
func (r BudgetReport) OverFileLines() bool {
	for _, finding := range r.Over {
		if finding.Kind == BudgetFileLines {
			return true
		}
	}
	return false
}

// BudgetReports measures every proposed file against its budgets. targets
// hold the files' content before the task.
func BudgetReports(root string, targets []ChangeTarget, edits []ChangeEdit) []BudgetReport {
	resolver := rules.NewResolver(root)
	reports := make([]BudgetReport, 0, len(edits))
	for _, edit := range edits {
		if edit.Deleted() {
			continue
		}
		base, hasBase := targetContent(targets, edit.Path)
		if edit.OldPath != "" {
			// A renamed file is measured against what it was before it moved.
			base, hasBase = targetContent(targets, edit.OldPath)
		}
		reports = append(reports, measureBudgets(resolver.For(edit.Path).Budgets, edit, base, hasBase))
	}
	return reports
}

func targetContent(targets []ChangeTarget, path string) (string, bool) {
	want := filepath.ToSlash(path)
	for _, target := range targets {
		if filepath.ToSlash(target.Path) == want {
			return target.Content, target.Exists
		}
	}
	return "", false
}

func measureBudgets(budgets rules.Budgets, edit ChangeEdit, base string, hasBase bool) BudgetReport {
	report := BudgetReport{Path: filepath.ToSlash(edit.Path), Budgets: budgets, Lines: countLines(edit.Content)}
	if hasBase {
		report.BaseLines = countLines(base)
	}
	report.ratchet(BudgetFinding{Kind: BudgetFileLines, Value: report.Lines, Base: report.BaseLines, Limit: budgets.FileLines}, hasBase)
	if budgets.FunctionLines <= 0 && budgets.FunctionComplexity <= 0 || !hasFunctions(symbols.DetectLanguage(edit.Path)) {
		return report
	}
	proposed := syntax.Analyze(edit.Path, []byte(edit.Content))
	if !proposed.Measured {
		report.NotChecked = proposed.Reason
		return report
	}
	var before syntax.File
	if hasBase {
		before = syntax.Analyze(edit.Path, []byte(base))
	}
	report.measureFunctions(proposed.Functions, before.Functions)
	return report
}

// measureFunctions holds changed and new functions to the budgets. A
// function whose text is unchanged is never the task's doing.
func (r *BudgetReport) measureFunctions(proposed, before []syntax.Function) {
	unchanged := map[string]bool{}
	for _, function := range before {
		unchanged[function.Hash] = true
	}
	prior := functionsByKey(before)
	for key, function := range functionsByKey(proposed) {
		old, existed := prior[key]
		if unchanged[function.Hash] {
			old, existed = function, true
		}
		r.ratchet(BudgetFinding{Kind: BudgetFunctionComplexity, Function: function.Name, Line: function.StartLine,
			Value: function.Complexity, Base: old.Complexity, Limit: r.Budgets.FunctionComplexity}, existed)
		r.ratchet(BudgetFinding{Kind: BudgetFunctionLines, Function: function.Name, Line: function.StartLine,
			Value: function.Lines(), Base: old.Lines(), Limit: r.Budgets.FunctionLines}, existed)
	}
	sortFindings(r.Over)
	sortFindings(r.Debt)
}

// functionsByKey keys functions by name and occurrence, so overloads with
// the same name are told apart.
func functionsByKey(functions []syntax.Function) map[string]syntax.Function {
	keyed := make(map[string]syntax.Function, len(functions))
	seen := map[string]int{}
	for _, function := range functions {
		seen[function.Name]++
		keyed[fmt.Sprintf("%s#%d", function.Name, seen[function.Name])] = function
	}
	return keyed
}

func (r *BudgetReport) ratchet(finding BudgetFinding, existed bool) {
	if finding.Limit <= 0 || finding.Value <= finding.Limit {
		return
	}
	if !existed {
		finding.Base = 0
	}
	if existed && finding.Base > finding.Limit && finding.Value <= finding.Base {
		r.Debt = append(r.Debt, finding)
		return
	}
	r.Over = append(r.Over, finding)
}

func sortFindings(findings []BudgetFinding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Kind < findings[j].Kind
	})
}

// budgetCheck renders the reports as the complexity gate.
func budgetCheck(reports []BudgetReport) CheckResult {
	var over, debt, notChecked []string
	for _, report := range reports {
		for _, finding := range report.Over {
			over = append(over, report.Path+": "+finding.String()+"; "+budgetRemedy(finding))
		}
		for _, finding := range report.Debt {
			debt = append(debt, report.Path+": "+finding.String())
		}
		if report.NotChecked != "" {
			notChecked = append(notChecked, report.Path)
		}
	}
	var notes []string
	if len(debt) > 0 {
		notes = append(notes, "Already over budget before this task and not grown (a refactor issue tracks each file):\n"+strings.Join(debt, "\n"))
	}
	if len(notChecked) > 0 {
		notes = append(notes, fmt.Sprintf("Function complexity and length not checked in %s: the parser could not read their functions; file lines were checked.", strings.Join(notChecked, ", ")))
	}
	if len(over) > 0 {
		return CheckResult{Name: "complexity gate", Required: true, State: CheckFailed, Output: strings.Join(append([]string{strings.Join(over, "\n")}, notes...), "\n\n")}
	}
	return CheckResult{Name: "complexity gate", Required: true, State: CheckPassed, Output: strings.Join(append([]string{"All proposed files are within their budgets or no worse than before."}, notes...), "\n\n")}
}

func budgetRemedy(finding BudgetFinding) string {
	if finding.Kind == BudgetFileLines {
		return "move cohesive parts into sibling files"
	}
	return "split it into smaller helpers"
}
