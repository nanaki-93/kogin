package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// stepIssues makes the tool flow's task fix these issues of calc.go one at a time.
func stepIssues(titles ...string) func(*ChangeOpenRequest) {
	return func(request *ChangeOpenRequest) {
		request.Kind, request.Title = "fix", "Fix issues in calc.go"
		request.FindingTargets, request.Issues = nil, nil
		for i, title := range titles {
			id := "finding:" + string(rune('a'+i))
			request.FindingTargets = append(request.FindingTargets, project.FindingTarget{ProjectID: request.ProjectID, Kind: "semantic", ID: id, Path: "calc.go"})
			request.Issues = append(request.Issues, ChangeIssue{FindingID: id, Path: "calc.go", Title: title, Detail: "line 4\n" + title + ".", Criteria: []string{title + " is resolved."}})
		}
	}
}

const (
	stepAddSource = "func Add(a, b int) int { return a + b }\n"
	stepSubSource = "func Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"
	stepMulSource = stepSubSource + "\n// Mul multiplies.\nfunc Mul(a, b int) int { return a * b }\n"
	stepMulTest   = "package main\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n"
)

func TestIssuesAreFixedOneAtATimeWithACommitEach(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub and its test."}},
		{Text: "Done.", Next: true},
		{Tool: "read_file", Arguments: map[string]any{"path": "calc.go"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Add cannot overflow for int inputs here; the issue does not hold."}},
		{Text: "Nothing to change.", Next: true},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepSubSource, "new_text": stepMulSource}},
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_mul_test.go", "content": stepMulTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Mul and its test."}},
		{Text: "Done."},
	}
	tester := []fakePiStep{
		{Tool: "read_file", Arguments: map[string]any{"path": "calc_mul_test.go"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "The fixes' tests cover the criteria."}},
		{Text: "Done."},
	}
	flow := startToolFlowWith(t, coder, tester, nil, stepIssues("Fix: Sub is missing", "Add ignores overflow", "Mul is missing"))
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	issues := finished.Issues
	if len(issues) != 3 || issues[0].Status != IssueFixed || issues[1].Status != IssueNotFixed || issues[2].Status != IssueFixed {
		t.Fatalf("issues = %+v", issues)
	}
	if !strings.Contains(issues[1].Reason, "The coding agent changed nothing: Add cannot overflow") || issues[1].Commit != "" {
		t.Fatalf("not fixed issue = %+v", issues[1])
	}
	if len(finished.FindingTargets) != 2 || finished.FindingTargets[0].ID != "finding:a" || finished.FindingTargets[1].ID != "finding:c" {
		t.Fatalf("the issue that was not fixed is still linked: %+v", finished.FindingTargets)
	}
	base := finished.Worktree.Base
	if log := testGit(t, flow.worktree, "log", "--format=%s", base+".."+issueStepsRef(finished)); log != "fix: mul is missing\nfix: sub is missing" {
		t.Fatalf("issue commits:\n%s", log)
	}
	if first := testGit(t, flow.worktree, "show", "--name-only", "--format=", issues[0].Commit); first != "calc.go\ncalc_sub_test.go" {
		t.Fatalf("the first issue's commit holds:\n%s", first)
	}
	if head := testGit(t, flow.worktree, "rev-parse", "HEAD"); head != base {
		t.Fatalf("the worktree moved off its base while the task ran: %s", head)
	}
	last := fakePiLog(t, flow.dir, "coder")
	if !strings.Contains(last.Prompt, "Task (fix): Mul is missing") || !strings.Contains(last.Prompt, "Fix issue 3 of 3 in calc.go: Mul is missing") || strings.Contains(last.Prompt, "Sub is missing") || !strings.Contains(last.Prompt, "1. Mul is missing is resolved.") {
		t.Fatalf("the last step's prompt:\n%s", last.Prompt)
	}

	reviewed, err := flow.service.ReviewChange(context.Background(), finished.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(finished)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := flow.service.ApplyChange(context.Background(), reviewed.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(reviewed), Confirm: true, CommitMessage: "Fix issues in calc.go"})
	if err != nil || result.State != "applied" || result.Commit == nil || result.Commit.OID != issues[2].Commit {
		t.Fatalf("accept = %+v, %v", result, err)
	}
	if log := testGit(t, flow.worktree, "log", "--format=%s", base+"..HEAD"); log != "fix: mul is missing\nfix: sub is missing" {
		t.Fatalf("the task branch holds:\n%s", log)
	}
	if _, err := gitRead(context.Background(), flow.worktree, "rev-parse", "--verify", issueStepsRef(finished)); err == nil {
		t.Fatal("the task's issue commits ref was kept after accepting")
	}
}

func TestAnIssueWhoseChecksStillFailIsRolledBack(t *testing.T) {
	broken := func(newText string) fakePiStep {
		return fakePiStep{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "return a + b", "new_text": newText}}
	}
	coder := []fakePiStep{
		broken("return a - b"),
		{Tool: "finish", Arguments: map[string]any{"summary": "Changed Add."}},
		{Text: "Done.", Next: true},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "return a - b", "new_text": "return b - a"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Changed Add again."}},
		{Text: "Done.", Next: true},
		{Tool: "finish", Arguments: map[string]any{"summary": "This needs a change in another package; it needs a task of its own."}},
		{Text: "Nothing to change."},
	}
	flow := startToolFlowWith(t, coder, nil, nil, stepIssues("Add rounds wrong", "Add is slow"))
	finished := flow.finished(t)
	if finished.Workflow.Status != "failed" || !strings.Contains(finished.Workflow.Reason, "None of the 2 issues could be fixed") {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if issue := finished.Issues[0]; issue.Status != IssueNotFixed || !strings.Contains(issue.Reason, "Checks still failed after one repair") {
		t.Fatalf("first issue = %+v", issue)
	}
	if issue := finished.Issues[1]; issue.Status != IssueNotFixed || !strings.Contains(issue.Reason, "needs a task of its own") {
		t.Fatalf("second issue = %+v", issue)
	}
	if len(finished.Changes) != 0 || len(finished.FindingTargets) != 0 {
		t.Fatalf("changes %+v, links %+v", finished.Changes, finished.FindingTargets)
	}
	if data, err := os.ReadFile(filepath.Join(flow.worktree, "calc.go")); err != nil || string(data) != toolCalcSource {
		t.Fatalf("the failed fix was not rolled back: %q %v", data, err)
	}
}

func TestANewTaskNamesItsIssuesOnceAndWithoutProgress(t *testing.T) {
	targets := []project.FindingTarget{{ProjectID: "p", Kind: "semantic", ID: "a", Path: "x.go"}, {ProjectID: "p", Kind: "semantic", ID: "b", Path: "x.go"}}
	issue := func(id string) ChangeIssue { return ChangeIssue{FindingID: id, Path: "x.go", Title: "Issue " + id} }
	fresh, err := newChangeIssues([]ChangeIssue{issue("a"), issue("b")}, targets)
	if err != nil || fresh[0].Status != IssuePending || fresh[1].Status != IssuePending {
		t.Fatalf("issues = %+v, %v", fresh, err)
	}
	if none, err := newChangeIssues(nil, targets); err != nil || none != nil {
		t.Fatalf("no issues = %+v, %v", none, err)
	}
	fixed := issue("b")
	fixed.Status = IssueFixed
	unlinked := issue("c")
	untitled := issue("b")
	untitled.Title = " "
	for name, issues := range map[string][]ChangeIssue{
		"one issue":   {issue("a")},
		"repeated":    {issue("a"), issue("a")},
		"with status": {issue("a"), fixed},
		"not linked":  {issue("a"), unlinked},
		"blank title": {issue("a"), untitled},
		"too many":    {issue("a"), issue("b"), issue("c"), issue("d"), issue("e"), issue("f")},
	} {
		if _, err := newChangeIssues(issues, targets); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	notFixed := ChangeIssue{FindingID: "c", Path: "x.go", Title: "Issue c", Status: IssueNotFixed, Reason: "Checks still failed."}
	if err := validateChangeIssues([]ChangeIssue{notFixed}, targets); err != nil {
		t.Fatalf("an unlinked issue that was not fixed: %v", err)
	}
	notFixed.Reason = ""
	if err := validateChangeIssues([]ChangeIssue{notFixed}, targets); err == nil {
		t.Fatal("an issue not fixed without a reason was accepted")
	}
}

func TestAfterTheStepsARepairActsOnlyOnItsFeedback(t *testing.T) {
	session := &ChangeSession{Issues: []ChangeIssue{{Title: "Sub is missing", Status: IssueFixed}, {Title: "Add overflows", Status: IssueNotFixed}}}
	message := issueRepairMessage(session, "Fix issues\n\nSpecification:\n1. Sub is missing\n2. Add overflows\n\nCritic review feedback:\nTestSub misses a negative case.")
	if !strings.HasPrefix(message, "Every issue of this task has had its own step:\n1. Sub is missing: fixed\n2. Add overflows: not fixed") || strings.Contains(message, "Specification") || !strings.HasSuffix(message, "Critic review feedback:\nTestSub misses a negative case.") {
		t.Fatalf("repair message:\n%s", message)
	}
	if got := issueRepairMessage(session, "Also log the result."); got != "Also log the result." {
		t.Fatalf("a new request was changed: %q", got)
	}
	if got := issueCommitMessage(ChangeIssue{Title: "Fix: Save outcome not checked"}); got != "fix: save outcome not checked" {
		t.Fatalf("commit message = %q", got)
	}
	if status, reason := workflowFailure(fmt.Errorf("change node create: %w", &issueStepsFailedError{count: 2}), nil); status != "failed" || !strings.HasPrefix(reason, "None of the 2 issues could be fixed") {
		t.Fatalf("failure = %s: %s", status, reason)
	}
	kept := issuesForRequest([]ChangeIssue{{FindingID: "a", Status: IssueFixed, Commit: "x"}, {FindingID: "b", Status: IssueNotFixed, Reason: "r"}, {FindingID: "c"}}, []project.FindingTarget{{ID: "a"}, {ID: "c"}})
	if len(kept) != 2 || kept[0].FindingID != "a" || kept[0].Status != "" || kept[0].Commit != "" || kept[1].FindingID != "c" {
		t.Fatalf("recaptured issues = %+v", kept)
	}
}
