package app

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app/archgate"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

// budgetBrief tells the writing agents the budgets of the files in scope, with
// each file's numbers as they stand, before they write anything.
func budgetBrief(root string, targets []ChangeTarget) string {
	edits := make([]ChangeEdit, 0, len(targets))
	for _, target := range targets {
		edits = append(edits, ChangeEdit{Path: target.Path, Content: target.Content})
	}
	var brief strings.Builder
	for _, report := range archgate.BudgetReports(root, targets, edits) {
		if report.Budgets == (rules.Budgets{}) {
			continue
		}
		brief.WriteString("- " + report.Path + ": " + describeBudgets(report.Budgets) + ".")
		if report.Budgets.FileLines > 0 && report.BaseLines > 0 {
			fmt.Fprintf(&brief, " Now %d lines (%d left).", report.BaseLines, max(report.Budgets.FileLines-report.BaseLines, 0))
		}
		for _, debt := range report.Debt {
			brief.WriteString(" Already over, must not grow: " + debt.String() + ".")
		}
		brief.WriteString("\n")
	}
	if brief.Len() == 0 {
		return ""
	}
	return "\nCode budgets from AGENTS.md, checked by a gate after you write (what was already over may stay over but must not grow):\n" + brief.String() +
		"Keep what you write within them. If a file must pass its line budget, write it anyway: a fix-up step will split it into sibling files.\n"
}

func describeBudgets(budgets rules.Budgets) string {
	var parts []string
	if budgets.FileLines > 0 {
		parts = append(parts, fmt.Sprintf("%d lines per file", budgets.FileLines))
	}
	if budgets.FunctionLines > 0 {
		parts = append(parts, fmt.Sprintf("%d lines per function", budgets.FunctionLines))
	}
	if budgets.FunctionComplexity > 0 {
		parts = append(parts, fmt.Sprintf("cyclomatic complexity %d per function", budgets.FunctionComplexity))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func proposedEdit(session *ChangeSession, path string) *ChangeEdit {
	for index := range session.Changes {
		if session.Changes[index].Path == path {
			return &session.Changes[index]
		}
	}
	return nil
}
