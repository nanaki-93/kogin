package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
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
		{Text: "Add cannot overflow for int inputs here; nothing to change.", Next: true},
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
	if !strings.HasPrefix(issues[1].Reason, "The coding agent changed nothing: The agent stopped without calling finish") || !strings.Contains(issues[1].Reason, "Add cannot overflow") || issues[1].Commit != "" {
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
	// Testing reads only what the steps fixed, and which issue was left out.
	tested := fakePiLog(t, flow.dir, "tester").Prompt
	if !strings.Contains(tested, "Acceptance criteria:\n1. Fix: Sub is missing is resolved.\n2. Mul is missing is resolved.\n\nIssues this task left out.") || strings.Contains(tested, "Add ignores overflow is resolved.") || !strings.Contains(tested, "- Add ignores overflow (calc.go): not fixed here; it is open again in Issues") {
		t.Fatalf("the testing agent's prompt:\n%s", tested)
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
		{Tool: "finish", Arguments: map[string]any{"summary": "This needs a change in another package; it needs a task of its own.", "outcome": "needs_own_task"}},
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
	if issue := finished.Issues[1]; issue.Status != IssueNotFixed || !strings.HasPrefix(issue.Reason, "The fix needs code this task may not change: This needs a change in another package") {
		t.Fatalf("second issue = %+v", issue)
	}
	if len(finished.Changes) != 0 || len(finished.FindingTargets) != 0 {
		t.Fatalf("changes %+v, links %+v", finished.Changes, finished.FindingTargets)
	}
	if data, err := os.ReadFile(filepath.Join(flow.worktree, "calc.go")); err != nil || string(data) != toolCalcSource {
		t.Fatalf("the failed fix was not rolled back: %q %v", data, err)
	}
}

func TestAStepThatStopsTheRunIsRolledBackAndStartsAgain(t *testing.T) {
	slowTest := "package main\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSub(t *testing.T) {\n\ttime.Sleep(5 * time.Second)\n\tif Sub(5, 3) != 2 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n"
	addSub := func(test string) []fakePiStep {
		return []fakePiStep{
			{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
			{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": test}},
			{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub and its test."}},
			{Text: "Done.", Next: true},
		}
	}
	coder := append(addSub(slowTest), addSub(toolSubTest)...)
	coder = append(coder,
		fakePiStep{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepSubSource, "new_text": stepMulSource}},
		fakePiStep{Tool: "create_file", Arguments: map[string]any{"path": "calc_mul_test.go", "content": stepMulTest}},
		fakePiStep{Tool: "finish", Arguments: map[string]any{"summary": "Added Mul and its test."}},
		fakePiStep{Text: "Done."},
	)
	tester := []fakePiStep{
		{Tool: "finish", Arguments: map[string]any{"summary": "The fixes' tests cover the criteria."}},
		{Text: "Done."},
	}
	limit := func(cfg *config.Config) { cfg.Timeouts.FocusedCheckSeconds = 1 }
	flow := startToolFlowWith(t, coder, tester, limit, stepIssues("Sub is missing", "Mul is missing"))
	stopped := flow.finished(t)
	if stopped.Workflow.Status != "failed" || !strings.Contains(stopped.Workflow.Reason, "Required checks did not complete") {
		t.Fatalf("workflow = %s (%s)", stopped.Workflow.Status, stopped.Workflow.Reason)
	}
	if stopped.Issues[0].Status != IssuePending || stopped.Issues[1].Status != IssuePending || len(stopped.FindingTargets) != 2 {
		t.Fatalf("issues %+v, links %+v", stopped.Issues, stopped.FindingTargets)
	}
	if len(stopped.Changes) != 0 || len(stopped.Checks) == 0 {
		t.Fatalf("changes %+v, checks %+v", stopped.Changes, stopped.Checks)
	}
	if data, err := os.ReadFile(filepath.Join(flow.worktree, "calc.go")); err != nil || string(data) != toolCalcSource {
		t.Fatalf("the stopped step was not rolled back: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(flow.worktree, "calc_sub_test.go")); !os.IsNotExist(err) {
		t.Fatalf("the stopped step's test was kept: %v", err)
	}

	flow.service.conf().focusedCheckTimeout = time.Minute
	request := flow.request
	request.ChangeIdentity = changeIdentity(stopped)
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, request); err != nil {
		t.Fatal(err)
	}
	finished := flow.finished(t)
	if finished.Issues[0].Status != IssueFixed || finished.Issues[1].Status != IssueFixed {
		t.Fatalf("workflow = %s (%s), issues = %+v", finished.Workflow.Status, finished.Workflow.Reason, finished.Issues)
	}
	if log := testGit(t, flow.worktree, "log", "--format=%s", finished.Worktree.Base+".."+issueStepsRef(finished)); log != "fix: mul is missing\nfix: sub is missing" {
		t.Fatalf("issue commits:\n%s", log)
	}
}

func TestAStepSaysHowItEndedWhenItChangesNothing(t *testing.T) {
	extraTest := "package main\n\nimport \"testing\"\n\nfunc TestSubAgain(t *testing.T) {\n\tif Sub(1, 1) != 0 {\n\t\tt.Fatal(\"Sub\")\n\t}\n}\n"
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub and its test.", "outcome": "fixed"}},
		{Text: "Done.", Next: true},
		// Already fixed: what the step wrote anyway is dropped with it.
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_again_test.go", "content": extraTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "The first step added Sub with its test.", "outcome": "already_fixed"}},
		{Text: "Nothing to change.", Next: true},
		// A finish that changed nothing must say why; the agent says it.
		{Tool: "read_file", Arguments: map[string]any{"path": "calc.go"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Checked Add."}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Checked Add.", "outcome": "fixed"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Add cannot overflow for int inputs here.", "outcome": "does_not_hold"}},
		{Text: "Nothing to change."},
	}
	tester := []fakePiStep{
		{Tool: "finish", Arguments: map[string]any{"summary": "The fixes' tests cover the criteria."}},
		{Text: "Done."},
	}
	flow := startToolFlowWith(t, coder, tester, nil, stepIssues("Sub is missing", "Sub has no test", "Add ignores overflow"))
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	issues := finished.Issues
	if issues[0].Status != IssueFixed || issues[0].Commit == "" {
		t.Fatalf("first issue = %+v", issues[0])
	}
	if issue := issues[1]; issue.Status != IssueFixed || issue.Commit != issues[0].Commit || !strings.HasPrefix(issue.Reason, "Already fixed by an earlier step: The first step added Sub") {
		t.Fatalf("an issue already fixed = %+v", issue)
	}
	if issue := issues[2]; issue.Status != IssueDoesNotHold || issue.Commit != "" || issue.Reason != "Add cannot overflow for int inputs here." {
		t.Fatalf("an issue that does not hold = %+v", issue)
	}
	for _, edit := range finished.Changes {
		if edit.Path == "calc_again_test.go" {
			t.Fatal("the already fixed step's test was kept")
		}
	}
	if _, err := os.Stat(filepath.Join(flow.worktree, "calc_again_test.go")); !os.IsNotExist(err) {
		t.Fatalf("the already fixed step's test is still in the worktree: %v", err)
	}
	// Merging closes the fixed ones and dismisses the one that does not hold.
	if len(finished.FindingTargets) != 2 || finished.FindingTargets[0].ID != "finding:a" || finished.FindingTargets[1].ID != "finding:b" {
		t.Fatalf("linked issues: %+v", finished.FindingTargets)
	}
	if len(finished.DismissedTargets) != 1 || finished.DismissedTargets[0].ID != "finding:c" {
		t.Fatalf("dismissed issues: %+v", finished.DismissedTargets)
	}
	if log := testGit(t, flow.worktree, "log", "--format=%s", finished.Worktree.Base+".."+issueStepsRef(finished)); log != "fix: sub is missing" {
		t.Fatalf("issue commits:\n%s", log)
	}
	last := fakePiLog(t, flow.dir, "coder")
	if !strings.Contains(last.Prompt, "finish with outcome already_fixed") {
		t.Fatalf("the step's prompt does not ask how it ended:\n%s", last.Prompt)
	}
	if len(last.Results) != 4 || !strings.Contains(last.Results[1], "you changed nothing, so say how this issue ended") || last.Results[2] != last.Results[1] || !strings.HasPrefix(last.Results[3], "Recorded.") {
		t.Fatalf("the finishes that changed nothing were not refused: %q", last.Results)
	}
}

func TestARunWithNothingToFixEndsSoAndClosingDismissesTheIssues(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "finish", Arguments: map[string]any{"summary": "Add already rounds correctly.", "outcome": "does_not_hold"}},
		{Text: "Nothing to change.", Next: true},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "return a + b", "new_text": "return a - b"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Changed Add."}},
		{Text: "Done.", Next: true},
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "return a - b", "new_text": "return b - a"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Changed Add again."}},
		{Text: "Done."},
	}
	flow := startToolFlowWith(t, coder, nil, nil, stepIssues("Add rounds wrong", "Add is slow"))
	finished := flow.finished(t)
	if finished.Workflow.Status != "nothing_to_fix" || !strings.HasPrefix(finished.Workflow.Reason, "Nothing to fix: 1 of the 2 issues do not hold, and 1 could not be fixed") {
		t.Fatalf("workflow = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if finished.Issues[0].Status != IssueDoesNotHold || finished.Issues[1].Status != IssueNotFixed || len(finished.Changes) != 0 {
		t.Fatalf("issues %+v, changes %+v", finished.Issues, finished.Changes)
	}
	if _, err := flow.service.CloseChange(context.Background(), finished.ID, ChangeIdentity{ProjectID: finished.ProjectID, ProjectRevision: finished.ProjectRevision, Revision: finished.Revision + 1, Hash: finished.Hash}); err == nil {
		t.Fatal("closed an older version of the task")
	}
	closed, err := flow.service.CloseChange(context.Background(), finished.ID, changeIdentity(finished))
	if err != nil || closed.Deleted != finished.ID || closed.DismissedIssues != 1 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if _, err := loadChangeSession(flow.worktree, finished.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the task was kept: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(flow.service.owner.manager.Root(), ".kasuri", "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Dismissed []project.FindingTarget `json:"dismissed"`
	}
	if err := json.Unmarshal(data, &document); err != nil || len(document.Dismissed) != 1 || document.Dismissed[0].ID != "finding:a" {
		t.Fatalf("dismissed = %+v, %v", document.Dismissed, err)
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

func TestTheStagesAfterTheStepsReadOnlyTheFixedIssues(t *testing.T) {
	session := &ChangeSession{Worktree: &ChangeWorktree{Base: "base", Branch: "task"}, AcceptanceCriteria: []string{"A is resolved.", "B is resolved.", "Add a test.", "C is resolved."},
		Issues: []ChangeIssue{
			{Title: "A", Path: "a.go", Status: IssueFixed, Criteria: []string{"A is resolved.", "Add a test."}},
			{Title: "B", Path: "a.go", Status: IssueDoesNotHold, Criteria: []string{"B is resolved.", "Add a test."}},
			{Title: "C", Path: "c.go", Status: IssueNotFixed, Criteria: []string{"C is resolved."}},
		}}
	view := fixedIssuesView(session)
	if view == session || !slices.Equal(view.AcceptanceCriteria, []string{"A is resolved.", "Add a test."}) || len(session.AcceptanceCriteria) != 4 {
		t.Fatalf("view criteria %q, task criteria %q", view.AcceptanceCriteria, session.AcceptanceCriteria)
	}
	if brief := leftOutBrief(session); brief != "\nIssues this task left out. They are not part of this change: do not write tests for them, fix them, or ask for them in review.\n- B (a.go): it does not hold\n- C (c.go): not fixed here; it is open again in Issues for a task of its own\n" {
		t.Fatalf("brief:\n%s", brief)
	}
	// While a step is still to run, or when every issue was fixed, the task
	// is read as it is.
	session.Issues[2].Status = IssuePending
	if fixedIssuesView(session) != session || leftOutBrief(session) != "" {
		t.Fatal("a task with a step to run was narrowed")
	}
	session.Issues[1].Status, session.Issues[2].Status = IssueFixed, IssueFixed
	if fixedIssuesView(session) != session || leftOutBrief(session) != "" {
		t.Fatal("a task that fixed every issue was narrowed")
	}
	// Fixed issues without criteria of their own keep the task's.
	session.Issues[0].Criteria, session.Issues[1].Criteria, session.Issues[2] = nil, nil, ChangeIssue{Title: "C", Path: "c.go", Status: IssueNotFixed}
	if view := fixedIssuesView(session); view != session || leftOutBrief(session) == "" {
		t.Fatal("a task whose fixed issues have no criteria lost its criteria")
	}
}
