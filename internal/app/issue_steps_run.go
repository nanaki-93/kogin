package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// issueStepError is a step that ends without a commit of its own while the
// run goes on with the next one: the agent changed nothing (the issue was
// already fixed by an earlier step, does not hold, or needs a task of its
// own), ran out of its time, or its checks still failed after one repair.
type issueStepError struct {
	reason string
	// outcome is what the agent said when it changed nothing: already_fixed
	// or does_not_hold. Any other leaves the issue not fixed.
	outcome string
}

func (e *issueStepError) Error() string { return e.reason }

// agentOutcomeError marks a tool-using agent's result that Kasuri refused
// (no change, a file out of scope): the agent ran, its outcome is not usable.
type agentOutcomeError struct{ err error }

func (e *agentOutcomeError) Error() string { return e.err.Error() }
func (e *agentOutcomeError) Unwrap() error { return e.err }

// issueStepsFailedError ends a run in which no issue could be fixed.
type issueStepsFailedError struct{ count int }

func (e *issueStepsFailedError) Error() string {
	return fmt.Sprintf("None of the %d issues could be fixed; each one's reason is listed under Creation, and they are open again in Issues.", e.count)
}

// nothingToFixError ends a run in which no issue needed a change it could
// make, and some do not hold: the task has nothing to merge, and closing it
// dismisses those.
type nothingToFixError struct{ count, doNotHold int }

func (e *nothingToFixError) Error() string {
	reason := fmt.Sprintf("Nothing to fix: %d of the %d issues do not hold", e.doNotHold, e.count)
	if other := e.count - e.doNotHold; other > 0 {
		reason += fmt.Sprintf(", and %d could not be fixed (open again in Issues)", other)
	}
	return reason + ". Each reason is listed under Creation; Close task dismisses the issues that do not hold."
}

func countIssues(session *ChangeSession, status string) int {
	count := 0
	for _, issue := range session.Issues {
		if issue.Status == status {
			count++
		}
	}
	return count
}

// runStageOrIssueSteps runs a model stage. For a task that fixes its issues
// one at a time, creation runs the steps of the issues not yet tried; once
// every issue had its step, it is one run that acts only on its feedback.
func (s *Service) runStageOrIssueSteps(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime, stage int, message string) (*ChangeSession, error) {
	if stage == 0 && stepsEnabled(session) {
		if nextIssue(session) >= 0 {
			return s.runIssueSteps(ctx, job, session, runtime)
		}
		message = issueRepairMessage(session, message)
	}
	return s.runWorkflowModelStage(ctx, job, session, runtime, stage, message)
}

// runIssueSteps is the creation stage of a task that fixes its issues one at
// a time: every issue not yet tried gets its step, in order.
func (s *Service) runIssueSteps(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime) (*ChangeSession, error) {
	for index := nextIssue(session); index >= 0; index = nextIssue(session) {
		next, err := s.runIssueStep(ctx, job, session, runtime, index)
		if err != nil {
			return nil, err
		}
		session = next
	}
	if !slices.ContainsFunc(session.Issues, func(issue ChangeIssue) bool { return issue.Status == IssueFixed }) {
		if hold := countIssues(session, IssueDoesNotHold); hold > 0 {
			return nil, &nothingToFixError{count: len(session.Issues), doNotHold: hold}
		}
		return nil, &issueStepsFailedError{count: len(session.Issues)}
	}
	return session, nil
}

// runIssueStep fixes one issue: a coding run with only that issue, the
// checks, one repair when they fail, then a commit. An issue it cannot fix
// is rolled back and unlinked. Any other error stops the run: the step is
// rolled back too, and the next run tries the issue again from the last
// fixed one.
func (s *Service) runIssueStep(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime, index int) (*ChangeSession, error) {
	session, err := s.updateIssueStep(ctx, job, session, func(current *ChangeSession) error {
		current.Issues[index].Status = IssueFixing
		return nil
	})
	if err != nil {
		return nil, err
	}
	before := slices.Clone(session.Changes)
	next, err := s.attemptIssueStep(ctx, job, session, runtime, index)
	var notFixed *issueStepError
	switch {
	case errors.As(err, &notFixed):
		return s.dropIssueStep(ctx, job, session, index, before, notFixed)
	case err != nil:
		return nil, s.resetIssueStep(ctx, job, index, before, err)
	}
	return s.updateIssueStep(ctx, job, next, func(current *ChangeSession) error {
		oid, err := commitIssueStep(ctx, job.root, current, current.Issues[index])
		if err != nil {
			return fmt.Errorf("commit the fix of %q: %w", current.Issues[index].Title, err)
		}
		current.Issues[index].Status, current.Issues[index].Commit = IssueFixed, oid
		return nil
	})
}

// attemptIssueStep runs the coding agent on one issue until its checks pass,
// with one repair. It returns an issueStepError when the issue stays unfixed.
func (s *Service) attemptIssueStep(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime, index int) (*ChangeSession, error) {
	message := issueStepMessage(session, index)
	for attempt := 0; ; attempt++ {
		next, err := s.runIssueAgent(ctx, job, session, runtime, index, message)
		if err != nil {
			return nil, err
		}
		checked, err := s.CheckChange(ctx, job.sessionID, ChangeCheckRequest{ChangeIdentity: identityForChange(next), CheckOptions: CheckOptions{RunTests: true, RunLint: true}})
		if err != nil {
			return nil, err
		}
		if requiredChecksPassed(checked.Checks) {
			return checked, nil
		}
		if existingTestsFail(checked.Checks) || !workflowChecksRepairable(checked.Checks) {
			// No fix of this issue can make these pass; the run stops and says why.
			return nil, errWorkflowChecksFailed
		}
		if attempt == 1 {
			return nil, &issueStepError{reason: "Checks still failed after one repair: " + summarizeFailedChecks(checked.Checks)}
		}
		session = checked
		message = issueStepMessage(checked, index) + "\n\nPrevious check failures to resolve:\n" + failedWorkflowCheckEvidence(checked.Checks)
	}
}

// runIssueAgent runs the coding agent once on one issue, within the time one
// creation run has, and fits what it wrote to the code budgets.
func (s *Service) runIssueAgent(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime, index int, message string) (*ChangeSession, error) {
	tools := toolStageAvailable(session, runtime)
	limit := duration(runtime.effective.Timeout)
	if tools {
		limit = s.toolStageLimit(runtime)
	}
	timed, cancel := context.WithTimeoutCause(ctx, limit, &workflowTimeoutError{stage: "Creation", limit: limit})
	defer cancel()
	var next *ChangeSession
	var run *ChangeStageTools
	var err error
	if tools {
		next, run, err = s.runToolAgent(timed, job.root, issueStepView(session, index), runtime, toolCoder, message)
	} else {
		next, err = s.generateChangeWithRuntime(timed, job.root, session, ChangeMessageRequest{ChangeIdentity: identityForChange(session), Message: message}, runtime)
	}
	var refused *agentOutcomeError
	switch {
	case ctx.Err() != nil:
		return nil, context.Cause(ctx)
	case timed.Err() != nil:
		return nil, &issueStepError{reason: fmt.Sprintf("The coding agent did not finish within %s.", limit)}
	case errors.Is(err, errCoderNoChange):
		return nil, unchangedIssueStep(run, finishReport(run))
	case errors.As(err, &refused):
		return nil, &issueStepError{reason: "The coding agent made no usable change: " + clip(refused.Error(), maxIssueReasonBytes-64)}
	case err != nil:
		return nil, err
	case next.Hash == session.Hash:
		return nil, unchangedIssueStep(run, agentReport(next))
	}
	return s.fitChangeToBudgets(ctx, job.root, next, runtime, false)
}

// unchangedIssueStep is a step whose agent changed nothing, as it said it
// ended: an issue already fixed or that does not hold is settled that way,
// any other is not fixed.
func unchangedIssueStep(run *ChangeStageTools, report string) error {
	if run != nil && run.finish != nil {
		if outcome := run.finish.Outcome; outcome == outcomeAlreadyFixed || outcome == outcomeDoesNotHold {
			return &issueStepError{reason: report, outcome: outcome}
		}
	}
	return &issueStepError{reason: "The coding agent changed nothing: " + report}
}

// finishReport is what an agent's finish call said, for a reason.
func finishReport(run *ChangeStageTools) string {
	if run == nil || run.finish == nil || strings.TrimSpace(run.finish.Summary) == "" {
		return "it gave no report."
	}
	return clip(strings.TrimSpace(run.finish.Summary), 600)
}

// agentReport is what the latest run said about its work, without the tool
// counts, for a reason.
func agentReport(session *ChangeSession) string {
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if message := session.Messages[i]; message.Role == "assistant" {
			report, _, _ := strings.Cut(strings.TrimSpace(message.Content), "\n\nTools:")
			return clip(strings.TrimSpace(report), 600)
		}
	}
	return "it gave no report."
}

// issueStepView is the task as the coding agent of one step reads it: that
// issue's title and criteria, without the brief that lists all the issues.
// It is only read; the run stores the task it loads.
func issueStepView(session *ChangeSession, index int) *ChangeSession {
	view := *session
	issue := session.Issues[index]
	view.Title, view.Plan = issue.Title, nil
	if len(issue.Criteria) > 0 {
		view.AcceptanceCriteria = issue.Criteria
	}
	return &view
}

// issueStepMessage asks for one issue's fix and its regression test.
func issueStepMessage(session *ChangeSession, index int) string {
	issue := session.Issues[index]
	var message strings.Builder
	fmt.Fprintf(&message, "Fix issue %d of %d in %s: %s\n%s\n\n", index+1, len(session.Issues), issue.Path, issue.Title, strings.TrimSpace(issue.Detail))
	message.WriteString("Fix only this issue; the task's other issues get steps of their own, and earlier ones are already in the worktree. ")
	message.WriteString("Check it against the code first. If it does not hold, change nothing and finish with outcome does_not_hold, saying why. If an earlier step of this task already fixed it, change nothing and finish with outcome already_fixed. ")
	message.WriteString("Otherwise make the smallest change that fixes it, add a regression test that fails without the fix, run the checks and finish with outcome fixed. ")
	message.WriteString("When the fix needs code outside this task's folders that its code imports, ask for those files with widen_scope and fix it there; when that is refused, change nothing and finish with outcome needs_own_task.")
	if len(issue.Criteria) > 0 {
		message.WriteString("\n\nDone when:")
		for _, criterion := range issue.Criteria {
			message.WriteString("\n- " + criterion)
		}
	}
	return message.String()
}

// issueRepairMessage is the request of a run after every issue had its step:
// only the review or check feedback it carries, so the agent does not start
// the issues again. A request without feedback (a new one) is kept.
func issueRepairMessage(session *ChangeSession, message string) string {
	feedback := -1
	for _, marker := range []string{"\n\nCritic review feedback:", "\n\nPrevious check failures to resolve:", "\n\nPrevious workflow failure to resolve:", "\n\nHarness² Propose-Probe-Compose:"} {
		if index := strings.Index(message, marker); index >= 0 && (feedback < 0 || index < feedback) {
			feedback = index
		}
	}
	if feedback < 0 {
		return message
	}
	var status strings.Builder
	for i, issue := range session.Issues {
		fmt.Fprintf(&status, "\n%d. %s: %s", i+1, issue.Title, strings.ReplaceAll(issue.Status, "_", " "))
	}
	return "Every issue of this task has had its own step:" + status.String() +
		"\n\nResolve only the feedback below in the worktree's current version. Do not start the issues again or change anything else." + message[feedback:]
}

// updateIssueStep changes the task's issue steps under its lock, keeping the
// conversation short enough for the steps still to come, and reports it.
func (s *Service) updateIssueStep(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, change func(*ChangeSession) error) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, err := s.loadChangeGraphSnapshot(ctx, job, changeGraphSnapshot(session))
	if err != nil {
		return nil, err
	}
	if err := change(current); err != nil {
		return nil, err
	}
	current.Messages = trimStepMessages(current.Messages)
	current.UpdatedAt = time.Now().UTC()
	if err := storeChangeSession(job.root, current); err != nil {
		return nil, err
	}
	s.notifyWorkflow(ctx, current, "create")
	return current, nil
}

// trimStepMessages keeps the conversation within what a step may add to it
// (two runs of two messages each): the task as first asked and the newest.
func trimStepMessages(messages []ChangeMessage) []ChangeMessage {
	limit := maxChangeMessages - 6
	if len(messages) <= limit {
		return messages
	}
	return append(messages[:2:2], messages[len(messages)-(limit-2):]...)
}

// dropIssueStep returns the task to the proposal it had before the step and
// settles the issue as the step ended: fixed by an earlier step's commit,
// set aside to be dismissed on merge when it does not hold, or not fixed
// with its reason and unlinked, so it is open again in Issues.
func (s *Service) dropIssueStep(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, index int, before []ChangeEdit, step *issueStepError) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, err := loadChangeSession(job.root, job.sessionID)
	if err != nil {
		return nil, err
	}
	if current.Workflow == nil || current.Workflow.ID != job.id || current.Issues[index].FindingID != session.Issues[index].FindingID {
		return nil, project.ErrRevisionConflict
	}
	if s.rollbackIssueStepLocked(current, before) {
		current.Checks = []CheckResult{}
	}
	if err := s.restoreStoredProposalLocked(context.WithoutCancel(ctx), job.root, current); err != nil {
		return nil, fmt.Errorf("roll back the step of %q: %w", current.Issues[index].Title, err)
	}
	settleIssueStep(current, index, step)
	current.Messages = trimStepMessages(current.Messages)
	current.UpdatedAt = time.Now().UTC()
	if err := storeChangeSession(job.root, current); err != nil {
		return nil, err
	}
	s.notifyWorkflow(ctx, current, "create")
	return current, nil
}

// settleIssueStep records how a step that made no commit of its own ended.
func settleIssueStep(current *ChangeSession, index int, step *issueStepError) {
	issue := &current.Issues[index]
	current.ScopeGrants = withoutStepGrants(current.ScopeGrants, issue.FindingID)
	reason := strings.TrimSpace(step.reason)
	linked := func(target project.FindingTarget) bool { return target.ID == issue.FindingID }
	earlier := lastIssueCommit(current)
	switch {
	case step.outcome == outcomeAlreadyFixed && earlier != "":
		issue.Status, issue.Commit, issue.Reason = IssueFixed, earlier, clip("Already fixed by an earlier step: "+reason, maxIssueReasonBytes-8)
		return
	case step.outcome == outcomeAlreadyFixed:
		issue.Status, issue.Reason = IssueDoesNotHold, clip("Already fixed before this task: "+reason, maxIssueReasonBytes-8)
	case step.outcome == outcomeDoesNotHold:
		issue.Status, issue.Reason = IssueDoesNotHold, clip(reason, maxIssueReasonBytes-8)
	default:
		issue.Status, issue.Reason = IssueNotFixed, clip(reason, maxIssueReasonBytes-8)
		current.FindingTargets = slices.DeleteFunc(current.FindingTargets, linked)
		return
	}
	for _, target := range current.FindingTargets {
		if linked(target) && !slices.Contains(current.DismissedTargets, target) {
			current.DismissedTargets = append(current.DismissedTargets, target)
		}
	}
	current.FindingTargets = slices.DeleteFunc(current.FindingTargets, linked)
}

// resetIssueStep rolls back the step that stopped the run with cause and
// marks its issue waiting, so the next run starts it again instead of
// finding its edits already made. The checks stay: they say why the run
// stopped. It returns cause, with a rollback that failed joined to it.
func (s *Service) resetIssueStep(ctx context.Context, job *changeWorkflowJob, index int, before []ChangeEdit, cause error) error {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, err := loadChangeSession(job.root, job.sessionID)
	if err != nil || current.Workflow == nil || current.Workflow.ID != job.id || index >= len(current.Issues) {
		return cause
	}
	s.rollbackIssueStepLocked(current, before)
	if err := s.restoreStoredProposalLocked(context.WithoutCancel(ctx), job.root, current); err != nil {
		return errors.Join(cause, fmt.Errorf("roll back the step of %q: %w", current.Issues[index].Title, err))
	}
	current.Issues[index].Status = IssuePending
	current.ScopeGrants = withoutStepGrants(current.ScopeGrants, current.Issues[index].FindingID)
	current.UpdatedAt = time.Now().UTC()
	if err := storeChangeSession(job.root, current); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// rollbackIssueStepLocked returns the stored proposal to the one before a
// step, revoking what was approved for the step's version. It reports
// whether the proposal changed.
func (s *Service) rollbackIssueStepLocked(current *ChangeSession, before []ChangeEdit) bool {
	hash := changeProposalHash(before)
	if hash == current.Hash {
		return false
	}
	current.Changes, current.Hash = before, hash
	current.Revision++
	current.PinnedTests = nil
	revokeApproval(current)
	delete(s.changeAuthority, current.ProjectID+"/"+current.ID)
	return true
}

// issueStepsBudget is the time a run needs beyond one creation run when it
// fixes issues one at a time: each issue may take two coding runs and two
// check passes.
func (s *Service) issueStepsBudget(ctx context.Context, models ChangeWorkflowModels, steps int) time.Duration {
	if steps == 0 {
		return 0
	}
	runtime := s.workflowStageRuntime(0, models.Create)
	run := duration(runtime.effective.Timeout)
	if runtime.client != nil && runtime.client.SupportsAgentRuns() {
		run = s.toolStageLimit(runtime)
	}
	checks := 5*s.conf().focusedCheckTimeout + 5*time.Minute
	return time.Duration(steps)*(2*run+2*checks) - run
}

// pendingIssueSteps counts the steps a run starting at creation will take.
func pendingIssueSteps(session *ChangeSession) int {
	if !stepsEnabled(session) {
		return 0
	}
	count := 0
	for _, issue := range session.Issues {
		if issue.Status == IssuePending || issue.Status == IssueFixing {
			count++
		}
	}
	return count
}
