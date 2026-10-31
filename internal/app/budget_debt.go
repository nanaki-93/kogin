package app

import (
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app/archgate"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// recordBudgetDebt opens one refactor issue per file a task found already over
// its budgets. The issue's identity is the file, so later tasks refresh its
// evidence and keep its triage instead of opening another. Recording is best
// effort: a project without analysis state has no Issues page to show it on.
func (s *Service) recordBudgetDebt(reports []archgate.BudgetReport) {
	var findings []project.UnifiedFinding
	for _, report := range reports {
		if len(report.Debt) > 0 {
			findings = append(findings, budgetDebtFinding(report))
		}
	}
	if len(findings) == 0 {
		return
	}
	_, _, input, err := s.workspaceState()
	if err != nil {
		return
	}
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		return
	}
	for i := range findings {
		findings[i].FileHash = input.FileHashes[findings[i].Location.Path]
	}
	_ = store.RecordFindings(input, project.FindingSourceRules, findings)
}

func budgetDebtFinding(report archgate.BudgetReport) project.UnifiedFinding {
	evidence := make([]string, 0, len(report.Debt))
	// The issue is the file's, so its lines stay out of its identity; the
	// first function over a budget names where to start.
	symbol := ""
	for _, debt := range report.Debt {
		evidence = append(evidence, debt.String())
		if symbol == "" {
			symbol = debt.Function
		}
	}
	return project.UnifiedFinding{
		Category:   project.FindingCategoryMaintainability,
		Source:     project.FindingSourceRules,
		Confidence: project.FindingConfidenceToolReported,
		Severity:   "low",
		Rule:       "code-budget",
		Title:      "Refactor " + report.Path + " to fit its code budgets",
		Message: report.Path + " was already over the budgets in AGENTS.md when a task changed it. " +
			"Tasks may change it without making it worse; split it to bring it within budget.",
		Evidence: strings.Join(evidence, "\n"),
		Location: project.FindingLocation{Path: report.Path, Symbol: symbol},
	}
}
