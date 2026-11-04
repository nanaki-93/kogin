package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func groupFinding(id, path string, line int, category project.FindingCategory) project.UnifiedFinding {
	return project.UnifiedFinding{ID: id, ProjectID: "p", Status: project.FindingStatusOpen, Category: category, Title: "Issue " + id, Message: "What " + id + " gets wrong.",
		Location: project.FindingLocation{Path: path, StartLine: line, Symbol: "fn" + id}}
}

func TestSeveralIssuesOfAFileDraftOneTaskThatLinksThemAll(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "cmd/master.go", "package cmd\n")
	writeProjectFile(t, root, "cmd/master_test.go", "package cmd\n")
	second := groupFinding("b", "cmd/master.go", 140, project.FindingCategorySecurity)
	second.Evidence = "Remediation: Write the vault first."
	second.TaskSpec = &project.BugTaskSpec{AcceptanceCriteria: []string{"The vault is written before the key file is removed"}}
	task := issuesTask(root, []project.UnifiedFinding{second, groupFinding("a", "cmd/master.go", 26, project.FindingCategorySecurity)})
	if task.Kind != "security" || task.Title != "Fix 2 issues in cmd/master.go" || !slices.Equal(task.Paths, []string{"cmd/master.go", "cmd/master_test.go"}) {
		t.Fatalf("task = %+v", task)
	}
	// In line order, each with where it is and its brief.
	first, later := strings.Index(task.Description, "1. Issue a — cmd/master.go:26 in `fna`\nProblem: What a gets wrong."), strings.Index(task.Description, "2. Issue b — cmd/master.go:140 in `fnb`\nProblem: What b gets wrong.\nFix: Write the vault first.")
	if first < 0 || later < first || strings.Contains(task.Description, "Evidence") {
		t.Fatalf("description = %q", task.Description)
	}
	if len(task.FindingTargets) != 2 || task.FindingTargets[0].ID != "a" || task.FindingTargets[1].ID != "b" {
		t.Fatalf("targets = %+v", task.FindingTargets)
	}
	// Each issue is a step of its own: where it is, its brief and criteria.
	if len(task.Issues) != 2 || task.Issues[0].FindingID != "a" || task.Issues[1].Title != "Issue b" || task.Issues[1].Detail != "(line 140, in fnb)\nProblem: What b gets wrong.\nFix: Write the vault first." || !slices.Equal(task.Issues[1].Criteria, []string{"The vault is written before the key file is removed"}) {
		t.Fatalf("issues = %+v", task.Issues)
	}
	if _, err := newChangeIssues(task.Issues, task.FindingTargets); err != nil {
		t.Fatalf("the drafted issues are refused: %v", err)
	}
	// Shared criteria appear once.
	if !slices.Contains(task.AcceptanceCriteria, "The vault is written before the key file is removed") || strings.Count(strings.Join(task.AcceptanceCriteria, "\n"), "Add a regression test that fails before the fix") != 1 {
		t.Fatalf("criteria = %q", task.AcceptanceCriteria)
	}
	if err := validateChangeCreateRequest(ChangeCreateRequest{Title: task.Title, Kind: task.Kind, Paths: task.Paths, AcceptanceCriteria: task.AcceptanceCriteria}); err != nil {
		t.Fatalf("the drafted task is refused: %v", err)
	}
	if kind := issuesTask(root, []project.UnifiedFinding{groupFinding("a", "x.go", 1, project.FindingCategorySecurity), groupFinding("b", "x.go", 2, project.FindingCategoryBugs)}).Kind; kind != "fix" {
		t.Fatalf("mixed kinds = %q", kind)
	}
}

func TestIssuesFixedTogetherMustBeOpenAndInOneFile(t *testing.T) {
	inProgress := groupFinding("c", "cmd/master.go", 3, "")
	inProgress.Status = project.FindingStatusInProgress
	findings := []project.UnifiedFinding{groupFinding("a", "cmd/master.go", 1, ""), groupFinding("b", "cmd/get.go", 2, ""), inProgress, groupFinding("d", "", 0, "")}
	for _, ids := range [][]string{{"a", "b"}, {"a", "c"}, {"d", "a"}} {
		if _, err := chooseIssues(findings, ids); !errors.Is(err, ErrIssueSelection) {
			t.Fatalf("%v: error = %v", ids, err)
		}
	}
	if _, err := chooseIssues(findings, []string{"a", "gone"}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("missing issue error = %v", err)
	}
	if chosen, err := chooseIssues(findings, []string{"a"}); err != nil || len(chosen) != 1 {
		t.Fatalf("chosen = %+v, %v", chosen, err)
	}
	service, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	for _, ids := range [][]string{nil, {" ", ""}, strings.Split("1 2 3 4 5 6", " ")} {
		if _, err := service.DraftIssuesTask(ids); !errors.Is(err, ErrIssueSelection) {
			t.Fatalf("%v: error = %v", ids, err)
		}
	}
	if _, err := service.DraftIssuesTask(strings.Split("1 2 3 4 5", " ")); errors.Is(err, ErrIssueSelection) {
		t.Fatalf("five issues are refused: %v", err)
	}
}
