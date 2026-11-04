package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
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
	// Issues lists the linked issues of a task for several issues, which its
	// own worktree fixes one at a time.
	Issues []ChangeIssue `json:"issues,omitempty"`
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
	task := &IssueTask{Kind: "fix", Title: issueTitle("Fix: ", finding.Title), Paths: []string{}}
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
	task.Description = issueDescription(finding, file)
	task.AcceptanceCriteria = issueCriteria(finding, file)
	addTestCandidate(root, task, issueTestCandidate(finding))
	if finding.Category == project.FindingCategoryMaintainability {
		task.Title = issueTitle("", finding.Title)
	}
	return task
}

// issueTitle fits an issue's title into a task title. An AI suggestion can
// carry its whole message as its title, so a long one keeps its first sentence,
// or is cut at a word, within the limit a new task's title has; the
// description keeps the full message.
func issueTitle(prefix, title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if len(prefix)+len(title) <= maxTaskTitleBytes {
		return prefix + title
	}
	if sentence, _, found := strings.Cut(title, ". "); found && len(prefix)+len(sentence) <= maxTaskTitleBytes {
		return prefix + sentence
	}
	const ellipsis = "…"
	limit := maxTaskTitleBytes - len(prefix) - len(ellipsis)
	for limit > 0 && !utf8.RuneStart(title[limit]) {
		limit--
	}
	cut := title[:limit]
	if space := strings.LastIndexByte(cut, ' '); space > 0 {
		cut = cut[:space]
	}
	return prefix + strings.TrimRight(cut, " ,;:") + ellipsis
}

// issueDescription is the issue's brief and where it is.
func issueDescription(finding project.UnifiedFinding, file string) string {
	description := issueBriefText(finding)
	if file != "" {
		description += "\nWhere: " + file + issueLine(finding.Location) + issueSymbol(finding.Location)
	}
	return description
}

// issueBriefText is the issue in short labeled lines (see project.BriefOf);
// evidence that is not labeled lines, such as a tool's output, follows it.
func issueBriefText(finding project.UnifiedFinding) string {
	brief := project.BriefOf(finding)
	lines := []string{"Problem: " + brief.Problem}
	for _, part := range [][2]string{{"Impact", brief.Impact}, {"Fix", brief.Fix}, {"Verify", brief.Verify}} {
		if part[1] != "" {
			lines = append(lines, part[0]+": "+part[1])
		}
	}
	if evidence := strings.TrimSpace(finding.Evidence); evidence != "" && !project.EvidenceIsLabeled(evidence) {
		lines = append(lines, "Output:\n"+textutil.Truncate(evidence, maxIssueOutputBytes))
	}
	return strings.Join(lines, "\n")
}

// maxIssueOutputBytes bounds a tool's output in an issue's task.
const maxIssueOutputBytes = 2048

func issueLine(location project.FindingLocation) string {
	if location.StartLine > 0 {
		return fmt.Sprintf(":%d", location.StartLine)
	}
	return ""
}

func issueSymbol(location project.FindingLocation) string {
	if location.Symbol != "" {
		return " in `" + location.Symbol + "`"
	}
	return ""
}

// addTestCandidate starts the task from the regression test the analysis
// proposed: its file joins the task's files, the description shows it and a
// criterion asks for it to fail before the fix and pass after.
func addTestCandidate(root string, task *IssueTask, candidate *project.TestCandidateSpec) {
	if candidate == nil {
		return
	}
	if !slices.Contains(task.Paths, candidate.Path) {
		task.Paths = append(task.Paths, candidate.Path)
	}
	where := "a new test file " + candidate.Path
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate.Path))); err == nil && info.Mode().IsRegular() {
		where = candidate.Path + ", which exists: add the test to it"
	}
	task.Description += fmt.Sprintf("\n\nA regression test to start from, in %s (suggested by the analysis; check it before relying on it):\n```\n%s\n```", where, strings.TrimRight(candidate.Content, "\n"))
	task.AcceptanceCriteria = append(task.AcceptanceCriteria, fmt.Sprintf("%s in %s fails before the fix and passes after it", candidate.Name, candidate.Path))
}

// issueTestCandidate is the regression test an issue's task specification
// proposes, if any.
func issueTestCandidate(finding project.UnifiedFinding) *project.TestCandidateSpec {
	if finding.TaskSpec == nil {
		return nil
	}
	return finding.TaskSpec.TestCandidate
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
		return []string{fmt.Sprintf("Resolve “%s” without breaking existing callers", issueTitle("", finding.Title)), "Add a regression test that fails before the fix"}
	}
}
