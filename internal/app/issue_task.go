package app

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
)

// IssueTask is a task drafted from an issue, for the composer to show before
// anything is saved: what to change, which files and when it is done.
type IssueTask struct {
	Kind               string                  `json:"kind"`
	Title              string                  `json:"title"`
	Description        string                  `json:"description"`
	Paths              []string                `json:"paths"`
	AcceptanceCriteria []string                `json:"acceptance_criteria"`
	FindingTargets     []project.FindingTarget `json:"finding_targets,omitempty"`
}

// DraftIssueTask turns an issue into a task: its file and that file's
// conventional test in any language, a kind from its category, and the
// criteria its task specification gives or that fit the kind. The task links
// the issue, so merging it marks the issue fixed.
func (s *Service) DraftIssueTask(id string) (*IssueTask, error) {
	findings, err := s.ListFindings(FindingFilter{})
	if err != nil {
		return nil, err
	}
	for _, finding := range findings {
		if finding.ID == id {
			return issueTask(s.manager.Root(), finding), nil
		}
	}
	return nil, fmt.Errorf("%w: the issue is no longer listed; refresh the issues", project.ErrRevisionConflict)
}

func issueTask(root string, finding project.UnifiedFinding) *IssueTask {
	file := finding.Location.Path
	if file == "" && finding.TaskSpec != nil {
		file = finding.TaskSpec.TargetPath
	}
	task := &IssueTask{Kind: "fix", Title: "Fix: " + finding.Title, Paths: []string{}}
	switch finding.Category {
	case project.FindingCategorySecurity:
		task.Kind = "security"
	case project.FindingCategoryPerformance:
		task.Kind = "performance"
	}
	if file != "" {
		task.Paths = append(task.Paths, file)
		if companion := rules.CompanionTest(root, file); companion != "" {
			task.Paths = append(task.Paths, companion)
		}
	}
	if finding.ProjectID != "" && finding.Location.Path != "" {
		task.FindingTargets = []project.FindingTarget{{ProjectID: finding.ProjectID, Kind: "semantic", ID: finding.ID, Path: finding.Location.Path}}
	}
	var description strings.Builder
	description.WriteString(finding.Message)
	if finding.Evidence != "" {
		description.WriteString("\n\nEvidence:\n" + finding.Evidence)
	}
	if file != "" {
		description.WriteString("\n\nLocation: " + file)
		if finding.Location.StartLine > 0 {
			description.WriteString(fmt.Sprintf(":%d", finding.Location.StartLine))
		}
		if finding.Location.Symbol != "" {
			description.WriteString(" (in " + finding.Location.Symbol + ")")
		}
	}
	task.Description = description.String()
	task.AcceptanceCriteria = issueCriteria(finding, file)
	if finding.Category == project.FindingCategoryMaintainability {
		task.Title = finding.Title
	}
	return task
}

// issueCriteria are the task specification's criteria, or ones that fit the
// issue: a refactor within the code budgets, or a fix with a regression test.
func issueCriteria(finding project.UnifiedFinding, file string) []string {
	switch {
	case finding.TaskSpec != nil && len(finding.TaskSpec.AcceptanceCriteria) > 0:
		return append([]string(nil), finding.TaskSpec.AcceptanceCriteria...)
	case finding.Category == project.FindingCategoryMaintainability:
		return []string{
			fmt.Sprintf("Bring %s within the budgets of its AGENTS.md, splitting it into cohesive sibling files where needed", file),
			"Keep its behavior and public interface; existing tests pass unchanged",
		}
	default:
		return []string{fmt.Sprintf("Resolve “%s” without breaking existing callers", finding.Title), "Add a regression test that fails before the fix"}
	}
}
