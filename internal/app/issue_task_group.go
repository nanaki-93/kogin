package app

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
)

const (
	// maxIssuesPerTask bounds how many issues one task fixes together.
	maxIssuesPerTask = 10
	// maxGroupCriteria bounds the "done when" list of such a task.
	maxGroupCriteria = 16
)

// ErrIssueSelection is returned for issues that cannot be fixed in one task.
var ErrIssueSelection = errors.New("choose up to ten open issues of one file")

// DraftIssuesTask drafts one task that fixes several issues of the same file:
// the file and its conventional test, every issue in the description with its
// line and evidence, their criteria together, and links to all of them, so
// merging the task marks each one fixed. One issue drafts as DraftIssueTask.
func (s *Service) DraftIssuesTask(ids []string) (*IssueTask, error) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 || len(ids) > maxIssuesPerTask {
		return nil, fmt.Errorf("%w: %d chosen", ErrIssueSelection, len(ids))
	}
	if len(ids) == 1 {
		return s.DraftIssueTask(ids[0])
	}
	findings, err := s.ListFindings(FindingFilter{})
	if err != nil {
		return nil, err
	}
	chosen, err := chooseIssues(findings, ids)
	if err != nil {
		return nil, err
	}
	return issuesTask(s.manager.Root(), chosen), nil
}

// chooseIssues finds the listed issues by ID; all must be open and in one file.
func chooseIssues(findings []project.UnifiedFinding, ids []string) ([]project.UnifiedFinding, error) {
	byID := make(map[string]project.UnifiedFinding, len(findings))
	for _, finding := range findings {
		byID[finding.ID] = finding
	}
	chosen := make([]project.UnifiedFinding, 0, len(ids))
	file := ""
	for _, id := range ids {
		finding, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: an issue is no longer listed; refresh the issues", project.ErrRevisionConflict)
		}
		if file == "" {
			file = finding.Location.Path
		}
		if finding.Status != project.FindingStatusOpen || finding.Location.Path == "" || finding.Location.Path != file {
			return nil, fmt.Errorf("%w: %s is not an open issue of the same file", ErrIssueSelection, id)
		}
		chosen = append(chosen, finding)
	}
	return chosen, nil
}

func uniqueIDs(ids []string) []string {
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	return unique
}

// issuesTask is the task for several issues of one file, in line order.
func issuesTask(root string, findings []project.UnifiedFinding) *IssueTask {
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Location.StartLine < findings[j].Location.StartLine })
	file := findings[0].Location.Path
	task := &IssueTask{Kind: groupKind(findings), Title: issueTitle("", fmt.Sprintf("Fix %d issues in %s", len(findings), file)), Paths: []string{file}}
	if companion := rules.CompanionTest(root, file); companion != "" {
		task.Paths = append(task.Paths, companion)
	}
	var description strings.Builder
	fmt.Fprintf(&description, "Fix these %d issues in %s. Analysis found them; check each against the code first and leave out any that does not hold.\n", len(findings), file)
	for index, finding := range findings {
		fmt.Fprintf(&description, "\n%d. %s%s\n%s", index+1, issueTitle("", finding.Title), issuePlace(finding.Location), finding.Message)
		if finding.Evidence != "" {
			description.WriteString("\nEvidence:\n" + finding.Evidence)
		}
		description.WriteString("\n")
		if finding.ProjectID != "" {
			task.FindingTargets = append(task.FindingTargets, project.FindingTarget{ProjectID: finding.ProjectID, Kind: "semantic", ID: finding.ID, Path: file})
		}
	}
	task.Description = strings.TrimRight(description.String(), "\n")
	for _, finding := range findings {
		for _, criterion := range issueCriteria(finding, file) {
			if len(task.AcceptanceCriteria) < maxGroupCriteria && !slices.Contains(task.AcceptanceCriteria, criterion) {
				task.AcceptanceCriteria = append(task.AcceptanceCriteria, criterion)
			}
		}
	}
	for _, finding := range findings {
		if candidate := issueTestCandidate(finding); candidate != nil && (slices.Contains(task.Paths, candidate.Path) || len(task.Paths) < maxTaskPaths) {
			addTestCandidate(root, task, candidate)
		}
	}
	return task
}

// issuePlace names where in its file an issue is: " (line 96, in savePassword)".
func issuePlace(location project.FindingLocation) string {
	var parts []string
	if location.StartLine > 0 {
		parts = append(parts, fmt.Sprintf("line %d", location.StartLine))
	}
	if location.Symbol != "" {
		parts = append(parts, "in "+location.Symbol)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// groupKind is the kind the issues share (security, performance), else fix.
func groupKind(findings []project.UnifiedFinding) string {
	kind := ""
	for _, finding := range findings {
		next := "fix"
		switch finding.Category {
		case project.FindingCategorySecurity:
			next = "security"
		case project.FindingCategoryPerformance:
			next = "performance"
		}
		if kind != "" && kind != next {
			return "fix"
		}
		kind = next
	}
	return kind
}
