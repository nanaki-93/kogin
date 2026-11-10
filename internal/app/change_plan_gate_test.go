package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// investigatorScript is a scripted investigation that reads calc.go and
// proposes a plan citing Add's line.
func investigatorScript() []fakePiStep {
	return []fakePiStep{
		{Tool: "read_file", Arguments: map[string]any{"path": "calc.go"}},
		{Tool: "plan", Arguments: map[string]any{
			"problem":      "calc.go has Add but no Sub, so callers cannot subtract.",
			"root_cause":   "The package only defines Add; nothing defines Sub.",
			"approach":     "Add Sub(a, b) beside Add, returning a - b, with a table test.",
			"citations":    []map[string]any{{"path": "calc.go", "start_line": 4, "end_line": 4, "quote": "func Add(a, b int) int { return a + b }", "observation": "Add exists; Sub does not."}},
			"alternatives": []map[string]any{{"approach": "Negate b and reuse Add.", "why_not": "An indirection with no gain over a - b."}},
			"risks":        []any{"None: the change only adds a function."},
			"touches":      []map[string]any{{"path": "calc.go", "why": "Define Sub beside Add."}},
		}},
		{Text: "Planned."},
	}
}

func coderScript() []fakePiStep {
	return []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": "func Add(a, b int) int { return a + b }\n", "new_text": "func Add(a, b int) int { return a + b }\n\n// Sub subtracts.\nfunc Sub(a, b int) int { return a - b }\n"}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub next to Add."}},
		{Text: "Done."},
	}
}

func testerScript() []fakePiStep {
	return []fakePiStep{
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Tested Sub."}},
		{Text: "Done."},
	}
}

func TestThePlanGateInvestigatesAndWaitsForTheHumansApproval(t *testing.T) {
	scripts := map[string][]fakePiStep{"investigator": investigatorScript(), "coder": coderScript(), "tester": testerScript()}
	flow := startToolFlowScripts(t, scripts, nil, nil, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	w := waiting.Workflow
	if w.Status != "awaiting_plan_review" || !strings.Contains(w.Reason, "approve it to start the agents") {
		t.Fatalf("workflow = %s (%s)", w.Status, w.Reason)
	}
	if w.PlanStage == nil || w.PlanStage.Status != "completed" || w.PlanStage.Tools == nil || w.PlanStage.Tools.Calls != 2 {
		t.Fatalf("plan stage = %+v", w.PlanStage)
	}
	for _, stage := range w.Stages[:3] {
		if stage.Status != "pending" {
			t.Fatalf("stage %s = %s before approval", stage.Name, stage.Status)
		}
	}
	if len(waiting.Changes) != 0 {
		t.Fatalf("the investigation changed files: %+v", waiting.Changes)
	}
	plan := waiting.Plan
	if plan == nil || plan.Source != "investigator" || plan.ApprovedHash != "" || plan.RootCause == "" || len(plan.Citations) != 1 || plan.Citations[0].StartLine != 4 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.Alternatives) != 1 || plan.Alternatives[0].WhyNot == "" || len(plan.Touches) != 1 {
		t.Fatalf("plan detail = %+v", plan)
	}
	record := fakePiLog(t, flow.dir, "investigator")
	if !strings.Contains(record.Prompt, "Task (feature): Add Sub") || !strings.Contains(record.Prompt, "1. Sub returns a minus b.") {
		t.Fatalf("investigator prompt = %q", record.Prompt)
	}
	var reported bool
	for _, message := range waiting.Messages {
		reported = reported || strings.Contains(message.Content, "Proposed a plan: calc.go has Add but no Sub")
	}
	if !reported {
		t.Fatalf("messages = %+v", waiting.Messages)
	}

	// A run without the plan's hash is refused until the human approves.
	blocked := flow.request
	blocked.ChangeIdentity, blocked.Plan = changeIdentity(waiting), "on"
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, blocked); err == nil || !strings.Contains(err.Error(), "approve the displayed plan") {
		t.Fatalf("start without approval = %v", err)
	}

	// Approving the plan runs the agents; the investigation stays on the timeline.
	approved := flow.request
	approved.ChangeIdentity, approved.Plan, approved.PlanHash = changeIdentity(waiting), "on", plan.Hash
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, approved); err != nil {
		t.Fatal(err)
	}
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("approved run = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if finished.Plan == nil || finished.Plan.ApprovedHash != finished.Plan.Hash {
		t.Fatalf("approved plan = %+v", finished.Plan)
	}
	if finished.Workflow.PlanStage == nil || finished.Workflow.PlanStage.Status != "completed" {
		t.Fatalf("the approved run lost the investigation: %+v", finished.Workflow.PlanStage)
	}
	coder := fakePiLog(t, flow.dir, "coder")
	if !strings.Contains(coder.Prompt, "The human approved this plan; follow it") || !strings.Contains(coder.Prompt, "Approach: Add Sub(a, b) beside Add") || !strings.Contains(coder.Prompt, "Root cause: The package only defines Add") {
		t.Fatalf("coder prompt = %q", coder.Prompt)
	}
}

func TestARedirectRunsTheInvestigationAgainWithTheNote(t *testing.T) {
	scripts := map[string][]fakePiStep{"investigator": investigatorScript()}
	flow := startToolFlowScripts(t, scripts, nil, nil, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	if waiting.Workflow.Status != "awaiting_plan_review" {
		t.Fatalf("workflow = %s (%s)", waiting.Workflow.Status, waiting.Workflow.Reason)
	}
	redirect := flow.request
	redirect.ChangeIdentity, redirect.Plan, redirect.PlanRedirect = changeIdentity(waiting), "on", "Keep Add untouched; explain why a - b is enough."
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, redirect); err != nil {
		t.Fatal(err)
	}
	again := flow.finished(t)
	if again.Workflow.Status != "awaiting_plan_review" || again.Plan == nil || again.Plan.ApprovedHash != "" {
		t.Fatalf("redirected run = %s, plan %+v", again.Workflow.Status, again.Plan)
	}
	record := fakePiLog(t, flow.dir, "investigator")
	if !strings.Contains(record.Prompt, "Redirect from the reviewer: Keep Add untouched") || !strings.Contains(record.Prompt, "The previous plan:") {
		t.Fatalf("redirected prompt = %q", record.Prompt)
	}
}

func TestThePlanGateSkipsARunWhosePlanWasApproved(t *testing.T) {
	// With the gate off nothing investigates; the run goes straight to code.
	flow := startToolFlowScripts(t, map[string][]fakePiStep{"coder": coderScript(), "tester": testerScript()}, nil, nil, nil)
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" || finished.Workflow.PlanStage != nil {
		t.Fatalf("gate-off run = %s, plan stage %+v", finished.Workflow.Status, finished.Workflow.PlanStage)
	}
}

func TestACitationThatDoesNotCheckIsReturnedToTheInvestigator(t *testing.T) {
	steps := investigatorScript()
	// A wrong quote first: the plan call fails and the agent fixes it.
	bad := map[string]any{}
	for key, value := range steps[1].Arguments {
		bad[key] = value
	}
	bad["citations"] = []map[string]any{{"path": "calc.go", "start_line": 1, "end_line": 1, "quote": "func Add(a, b int) int { return a + b }", "observation": "Wrong lines."}}
	scripts := map[string][]fakePiStep{"investigator": append([]fakePiStep{steps[0], {Tool: "plan", Arguments: bad}}, steps[1:]...)}
	flow := startToolFlowScripts(t, scripts, nil, nil, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	if waiting.Workflow.Status != "awaiting_plan_review" {
		t.Fatalf("workflow = %s (%s)", waiting.Workflow.Status, waiting.Workflow.Reason)
	}
	record := fakePiLog(t, flow.dir, "investigator")
	if len(record.Results) < 3 || !strings.Contains(record.Results[1], "do not contain the quote") {
		t.Fatalf("investigator results = %+v", record.Results)
	}
}

// groupedInvestigatorScript proposes a plan for a three-issue task: the first
// holds, the second does not, the third is already fixed.
func groupedInvestigatorScript() []fakePiStep {
	steps := investigatorScript()
	steps[1].Arguments["issues"] = []map[string]any{
		{"issue": 1, "verdict": "holds", "note": "Sub is still missing from calc.go."},
		{"issue": 2, "verdict": "does_not_hold", "note": "Add cannot overflow for the int inputs this project uses."},
		{"issue": 3, "verdict": "already_fixed", "note": "The current code already clamps the result."},
	}
	return steps
}

func TestApprovedPlanVerdictsSettleIssuesBeforeAnyStepRuns(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "edit_file", Arguments: map[string]any{"path": "calc.go", "old_text": stepAddSource, "new_text": stepSubSource}},
		{Tool: "create_file", Arguments: map[string]any{"path": "calc_sub_test.go", "content": toolSubTest}},
		{Tool: "finish", Arguments: map[string]any{"summary": "Added Sub and its test."}},
		{Text: "Done."},
	}
	tester := []fakePiStep{
		{Tool: "finish", Arguments: map[string]any{"summary": "The fix's test covers the criterion."}},
		{Text: "Done."},
	}
	scripts := map[string][]fakePiStep{"investigator": groupedInvestigatorScript(), "coder": coder, "tester": tester}
	flow := startToolFlowScripts(t, scripts, nil, stepIssues("Sub is missing", "Add ignores overflow", "Add result is unclamped"), func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	if waiting.Workflow.Status != "awaiting_plan_review" || len(waiting.Plan.IssueVerdicts) != 3 {
		t.Fatalf("workflow = %s (%s), plan %+v", waiting.Workflow.Status, waiting.Workflow.Reason, waiting.Plan)
	}
	approved := flow.request
	approved.ChangeIdentity, approved.Plan, approved.PlanHash = changeIdentity(waiting), "on", waiting.Plan.Hash
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, approved); err != nil {
		t.Fatal(err)
	}
	finished := flow.finished(t)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("approved run = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	// Only the issue that holds got a coding step and a commit.
	if first := finished.Issues[0]; first.Status != IssueFixed || first.Commit == "" {
		t.Fatalf("issue 1 = %+v", first)
	}
	if second := finished.Issues[1]; second.Status != IssueDoesNotHold || !strings.Contains(second.Reason, "Add cannot overflow") {
		t.Fatalf("issue 2 = %+v", second)
	}
	if third := finished.Issues[2]; third.Status != IssueDoesNotHold || !strings.Contains(third.Reason, "Already fixed before this task") {
		t.Fatalf("issue 3 = %+v", third)
	}
	if len(finished.DismissedTargets) != 2 {
		t.Fatalf("dismissed = %+v", finished.DismissedTargets)
	}
	// The step's prompt carried the plan's note for the issue that holds.
	coderRun := fakePiLog(t, flow.dir, "coder")
	if !strings.Contains(coderRun.Prompt, "The approved plan checked this issue against the code and it holds: Sub is still missing") {
		t.Fatalf("coder prompt = %q", coderRun.Prompt)
	}
}

func TestAPlanIsClippedToItsWordBudgets(t *testing.T) {
	steps := investigatorScript()
	steps[1].Arguments["problem"] = strings.Repeat("The handler reads nested paths and fails. ", 12)
	steps[1].Arguments["approach"] = "1. Reject names with a slash.\n\n2. Return 404 for them.\n3. Add a table test.\n4. Refactor the handler.\n5. Document it."
	steps[1].Arguments["risks"] = []any{strings.Repeat("A very long risk sentence that goes on and on. ", 6)}
	flow := startToolFlowScripts(t, map[string][]fakePiStep{"investigator": steps}, nil, nil, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	plan := waiting.Plan
	if plan == nil || waiting.Workflow.Status != "awaiting_plan_review" {
		t.Fatalf("workflow = %s (%s)", waiting.Workflow.Status, waiting.Workflow.Reason)
	}
	if n := len([]rune(plan.Problem)); n > maxPlanProblemRunes || !strings.HasSuffix(plan.Problem, "…") {
		t.Fatalf("problem not clipped: %d runes %q", n, plan.Problem)
	}
	// The approach keeps at most three steps, each on its own line.
	if lines := strings.Split(plan.Approach, "\n"); len(lines) != 3 || lines[0] != "1. Reject names with a slash." || lines[2] != "3. Add a table test." {
		t.Fatalf("approach = %q", plan.Approach)
	}
	if n := len([]rune(plan.Risks[0])); n > maxPlanShortRunes || !strings.HasSuffix(plan.Risks[0], "…") {
		t.Fatalf("risk not clipped: %q", plan.Risks[0])
	}
}

// oneIssue makes the flow's task the fix of a single reported issue.
func oneIssue(request *ChangeOpenRequest) {
	request.Kind, request.Title = "fix", "Fix: Add rounds wrong"
	request.FindingTargets = []project.FindingTarget{{ProjectID: request.ProjectID, Kind: "semantic", ID: "finding:a", Path: "calc.go"}}
	request.Issues = nil
}

// noBugInvestigatorScript proposes a plan whose verdict is that the task's
// one issue does not hold: nothing is touched.
func noBugInvestigatorScript(verdict string) []fakePiStep {
	steps := investigatorScript()
	steps[1].Arguments["problem"] = "Reported: Add rounds wrong. It does not: Add returns a + b for ints."
	steps[1].Arguments["root_cause"] = "No bug. Add is integer addition; there is nothing to round."
	steps[1].Arguments["approach"] = "Nothing to change."
	delete(steps[1].Arguments, "touches")
	steps[1].Arguments["issues"] = []map[string]any{{"issue": 1, "verdict": verdict, "note": "Add is integer addition; nothing is rounded."}}
	return steps
}

func TestAPlanThatFindsNothingToFixClosesTheTaskAndDismissesTheIssue(t *testing.T) {
	flow := startToolFlowScripts(t, map[string][]fakePiStep{"investigator": noBugInvestigatorScript("does_not_hold")}, nil, oneIssue, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	if waiting.Workflow.Status != "awaiting_plan_review" || len(waiting.Plan.IssueVerdicts) != 1 || waiting.Plan.IssueVerdicts[0].FindingID != "finding:a" || len(waiting.Plan.Touches) != 0 {
		t.Fatalf("workflow = %s (%s), plan %+v", waiting.Workflow.Status, waiting.Workflow.Reason, waiting.Plan)
	}
	record := fakePiLog(t, flow.dir, "investigator")
	if !strings.Contains(record.Prompt, "This task fixes one reported issue. Check it against the code and give it a verdict as issue 1") {
		t.Fatalf("investigator prompt = %q", record.Prompt)
	}
	// Closing from the plan step needs no approval and no agents.
	closed, err := flow.service.CloseChange(context.Background(), waiting.ID, changeIdentity(waiting))
	if err != nil || closed.Deleted != waiting.ID || closed.DismissedIssues != 1 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if _, err := os.Stat(filepath.Join(flow.dir, "coder.log.json")); !os.IsNotExist(err) {
		t.Fatalf("a coding agent ran: %v", err)
	}
}

func TestAPlanWhoseIssueHoldsCannotBeClosedAndNeedsItsTouches(t *testing.T) {
	steps := investigatorScript()
	withIssue := map[string]any{}
	for key, value := range steps[1].Arguments {
		withIssue[key] = value
	}
	withIssue["issues"] = []map[string]any{{"issue": 1, "verdict": "holds"}}
	// First without touches: a plan that changes code must name its files.
	bare := map[string]any{}
	for key, value := range withIssue {
		bare[key] = value
	}
	delete(bare, "touches")
	scripts := map[string][]fakePiStep{"investigator": {steps[0], {Tool: "plan", Arguments: bare}, {Tool: "plan", Arguments: withIssue}, {Text: "Planned."}}}
	flow := startToolFlowScripts(t, scripts, nil, oneIssue, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	if waiting.Workflow.Status != "awaiting_plan_review" || waiting.Plan.IssueVerdicts[0].Verdict != "holds" {
		t.Fatalf("workflow = %s (%s), plan %+v", waiting.Workflow.Status, waiting.Workflow.Reason, waiting.Plan)
	}
	record := fakePiLog(t, flow.dir, "investigator")
	if len(record.Results) < 2 || !strings.Contains(record.Results[1], "touches: name each file the change edits or creates") {
		t.Fatalf("investigator results = %+v", record.Results)
	}
	if _, err := flow.service.CloseChange(context.Background(), waiting.ID, changeIdentity(waiting)); !errors.Is(err, errNotNothingToFix) {
		t.Fatalf("closed a task whose issue holds: %v", err)
	}
}

func TestApprovingAPlanWithNothingToFixSpendsNoCodingRun(t *testing.T) {
	flow := startToolFlowScripts(t, map[string][]fakePiStep{"investigator": noBugInvestigatorScript("already_fixed")}, nil, oneIssue, func(request *ChangeWorkflowRequest) { request.Plan = "on" })
	waiting := flow.finished(t)
	approved := flow.request
	approved.ChangeIdentity, approved.Plan, approved.PlanHash = changeIdentity(waiting), "on", waiting.Plan.Hash
	if _, err := flow.service.StartChangeWorkflow(context.Background(), flow.id, approved); err != nil {
		t.Fatal(err)
	}
	finished := flow.finished(t)
	if finished.Workflow.Status != "nothing_to_fix" || !strings.HasPrefix(finished.Workflow.Reason, "Nothing to fix: the code already fixes the issue. Add is integer addition") {
		t.Fatalf("approved run = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	if len(finished.DismissedTargets) != 1 || finished.DismissedTargets[0].ID != "finding:a" {
		t.Fatalf("dismissed = %+v", finished.DismissedTargets)
	}
	if closed, err := flow.service.CloseChange(context.Background(), finished.ID, changeIdentity(finished)); err != nil || closed.DismissedIssues != 1 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
}

func TestACoderThatFindsNothingToFixEndsTheRunWithItsReason(t *testing.T) {
	coder := []fakePiStep{
		{Tool: "finish", Arguments: map[string]any{"summary": "No change. Add is integer addition, nothing rounds."}},
		{Tool: "finish", Arguments: map[string]any{"summary": "No change. Add is integer addition, nothing rounds.", "outcome": "does_not_hold"}},
		{Text: "Nothing to change."},
	}
	flow := startToolFlowScripts(t, map[string][]fakePiStep{"coder": coder}, nil, oneIssue, nil)
	finished := flow.finished(t)
	if finished.Workflow.Status != "nothing_to_fix" || !strings.Contains(finished.Workflow.Reason, "the coding agent found the issue does not hold. No change. Add is integer addition") {
		t.Fatalf("run = %s (%s)", finished.Workflow.Status, finished.Workflow.Reason)
	}
	record := fakePiLog(t, flow.dir, "coder")
	if len(record.Results) != 2 || !strings.Contains(record.Results[0], "you changed nothing, so say how this issue ended") || !strings.HasPrefix(record.Results[1], "Recorded.") {
		t.Fatalf("the finish that changed nothing was not asked how it ended: %q", record.Results)
	}
	if closed, err := flow.service.CloseChange(context.Background(), finished.ID, changeIdentity(finished)); err != nil || closed.DismissedIssues != 1 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
}
