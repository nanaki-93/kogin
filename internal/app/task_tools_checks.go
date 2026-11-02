package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

const (
	maxFinishSummary  = 4000
	maxCheckOutputCut = 1500
	maxChecksReport   = 8000
)

func (run *taskToolRun) checkTools() []toolSpec {
	return []toolSpec{
		{name: "run_checks", call: run.runChecks,
			description: "Run the task's checks on the change as it stands: formatting and parsing, architecture and code budgets from AGENTS.md, and the lint and tests of every subproject the change touches. Failures that also happened before the change are marked as such. Run it before finish when you changed code.",
			schema:      `{"type":"object","properties":{},"additionalProperties":false}`},
		{name: "finish", call: run.finishRun,
			description: "End the run with a summary of what you changed and, for each acceptance criterion, how the change meets it. When you fix one issue, also say how it ended: fixed (you changed code for it), already_fixed (an earlier step of this task already fixed it; change nothing), does_not_hold (the code does not have the problem; change nothing and say why), or needs_own_task (the fix needs code this task may not change). A coding agent sure that a test the testing agent wrote is wrong uses outcome test_disputed, names it in disputed_tests and says why in the summary. A testing agent lists in follow_ups the gaps this change cannot cover (behavior in code the task does not change) instead of writing failing tests for them. Call it once, last.",
			schema:      `{"type":"object","properties":{"summary":{"type":"string","minLength":1,"maxLength":4000},"criteria":{"type":"array","maxItems":16,"items":{"type":"object","properties":{"criterion":{"type":"integer","minimum":1},"how":{"type":"string","minLength":1,"maxLength":1000}},"required":["criterion","how"],"additionalProperties":false}},"outcome":{"type":"string","enum":["fixed","already_fixed","does_not_hold","needs_own_task","test_disputed"]},"disputed_tests":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1}},"follow_ups":{"type":"array","maxItems":5,"items":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":120},"detail":{"type":"string","minLength":1,"maxLength":1000},"path":{"type":"string"}},"required":["title","detail"],"additionalProperties":false}}},"required":["summary"],"additionalProperties":false}`},
	}
}

// proposal reads the change as the worktree holds it now, with a base
// target for every file it touches, on a copy of the run's session.
func (run *taskToolRun) proposal(ctx context.Context) (*ChangeSession, error) {
	snapshot := *run.session
	snapshot.Targets = slices.Clone(run.session.Targets)
	snapshot.Instructions = maps.Clone(run.session.Instructions)
	if snapshot.Instructions == nil {
		snapshot.Instructions = map[string]string{}
	}
	edits, err := readWorktreeProposal(ctx, run.root, &snapshot, run.session.Changes)
	if err != nil {
		return nil, err
	}
	if err := ensureWorktreeTargets(ctx, run.root, &snapshot, edits); err != nil {
		return nil, err
	}
	snapshot.Changes = edits
	return &snapshot, nil
}

func (run *taskToolRun) runChecks(ctx context.Context, arguments json.RawMessage) (string, error) {
	if err := decodeToolArguments(arguments, &struct{}{}); err != nil {
		return "", err
	}
	run.mu.Lock()
	if run.checks >= run.limits.checks {
		run.mu.Unlock()
		return "", fmt.Errorf("this run has run the checks %d times; finish with what you have", run.limits.checks)
	}
	run.checks++
	run.mu.Unlock()
	// Writes wait while the checks copy and compare the worktree.
	run.writeMu.Lock()
	defer run.writeMu.Unlock()
	snapshot, err := run.proposal(ctx)
	if err != nil {
		return "", err
	}
	if len(snapshot.Changes) == 0 {
		return "Nothing has changed yet; there is nothing to check.", nil
	}
	checks, err := run.s.checkToolProposal(ctx, run.root, snapshot)
	if err != nil {
		return "", err
	}
	return describeToolChecks(len(snapshot.Changes), checks), nil
}

// checkToolProposal runs the source checks and the affected subprojects'
// lint and tests on a copy of the worktree; failures are compared with the
// task's base commit.
func (s *Service) checkToolProposal(ctx context.Context, root string, snapshot *ChangeSession) ([]CheckResult, error) {
	checks, _ := changeSourceChecks(root, snapshot.Targets, snapshot.Changes)
	workspace, err := os.MkdirTemp("", "kasuri-tool-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workspace)
	if err := copyCheckWorkspace(ctx, root, workspace); err != nil {
		return nil, err
	}
	fingerprint, err := fingerprintCheckWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	revision, err := s.changeExecutionRevision(snapshot)
	if err != nil {
		return nil, err
	}
	run := &checkRun{s: s, ctx: ctx, root: root, revision: revision, fingerprint: fingerprint, session: snapshot,
		revert: func(dir string) error { return revertProposalInTree(dir, snapshot) }, baseKey: "worktree base " + snapshot.Worktree.Base}
	defer run.cleanup()
	commands, err := s.runProfileChecks(run, workspace, CheckOptions{RunTests: true, RunLint: true})
	return append(checks, commands...), err
}

func describeToolChecks(files int, checks []CheckResult) string {
	var out strings.Builder
	verdict := "All required checks pass."
	if !requiredChecksPassed(checks) {
		verdict = "Some required checks fail; fix them before you finish."
	}
	fmt.Fprintf(&out, "Checks on the change as it stands (%d files changed). %s\n", files, verdict)
	for _, check := range checks {
		label := strings.ToUpper(check.State)
		if check.Outcome != "" {
			label += " (" + strings.ReplaceAll(check.Outcome, "_", " ") + ")"
		}
		if !check.Required {
			label += ", optional"
		}
		fmt.Fprintf(&out, "- %s: %s\n", check.Name, label)
		if check.State != CheckPassed && check.State != CheckSkipped && strings.TrimSpace(check.Output) != "" && out.Len() < maxChecksReport {
			out.WriteString(textutil.TruncateTail(strings.TrimSpace(check.Output), maxCheckOutputCut) + "\n")
		}
	}
	return out.String()
}

func (run *taskToolRun) finishRun(_ context.Context, arguments json.RawMessage) (string, error) {
	var input toolFinish
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	if strings.TrimSpace(input.Summary) == "" || len(input.Summary) > maxFinishSummary {
		return "", errors.New("give a summary of at most 4000 bytes")
	}
	for _, note := range input.Criteria {
		if note.Criterion < 1 || note.Criterion > len(run.session.AcceptanceCriteria) || strings.TrimSpace(note.How) == "" || len(note.How) > 1000 {
			return "", fmt.Errorf("criteria must number the task's %d acceptance criteria, each with how it is met", len(run.session.AcceptanceCriteria))
		}
	}
	if err := run.validateFinish(&input); err != nil {
		return "", err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.finish = &input
	return "Recorded. End your turn now; do not call more tools.", nil
}

// validateFinish checks what the finish call says beyond its summary: an
// outcome, a coder's dispute and a tester's follow-ups.
func (run *taskToolRun) validateFinish(input *toolFinish) error {
	switch {
	case !slices.Contains([]string{"", outcomeFixed, outcomeAlreadyFixed, outcomeDoesNotHold, outcomeNeedsOwnTask, outcomeTestDisputed}, input.Outcome):
		return errors.New("outcome is fixed, already_fixed, does_not_hold, needs_own_task or test_disputed")
	case run.role == toolTester && (input.Outcome == outcomeTestDisputed || len(input.DisputedTests) > 0):
		return errors.New("only the coding agent disputes tests")
	case run.role == toolCoder && len(input.FollowUps) > 0:
		return errors.New("follow_ups are the testing agent's; say what the change leaves out in the summary")
	}
	if err := validateDispute(run.session, input); err != nil {
		return err
	}
	return validateFollowUps(input.FollowUps)
}
