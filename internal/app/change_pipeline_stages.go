package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

const workflowTestPrompt = "Write meaningful regression and boundary tests for this proposal and its acceptance criteria. Return complete test file contents only for captured test file paths (such as _test.go, .test.ts, test_*.py, or tests/ files). Preserve existing tests and all pinned regression tests. Do not change implementation files or claim tests ran."

// toolTestPrompt asks a testing agent with tools for tests; it starts like
// workflowTestPrompt so the conversation cleanup recognizes it.
const toolTestPrompt = "Write meaningful regression and boundary tests for this proposal and its acceptance criteria. Create or edit test files only; the implementation is fixed. Keep existing and pinned tests, run the checks, and finish with the tests you wrote and the criteria they cover."

func stripWorkflowTestMessages(messages []ChangeMessage) []ChangeMessage {
	if len(messages) == 0 {
		return messages
	}
	cleaned := make([]ChangeMessage, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		if messages[i].Role == "user" && strings.HasPrefix(messages[i].Content, "Write meaningful regression and boundary tests for this proposal") {
			if i+1 < len(messages) && messages[i+1].Role == "assistant" {
				i++
			}
			continue
		}
		cleaned = append(cleaned, messages[i])
	}
	if len(cleaned) > maxChangeMessages-4 {
		keepTail := maxChangeMessages - 6
		cleaned = append(cleaned[:2:2], cleaned[len(cleaned)-keepTail:]...)
	}
	return cleaned
}

func workflowRetryMessage(message string, workflow *ChangeWorkflow, checks []CheckResult) string {
	extra := criticReviewRetryFeedback(message, workflow) + workflowFailureRetryFeedback(message, workflow, checks) + harnessProbeRetryDirective(message, workflow, checks)
	if extra == "" {
		return message
	}
	if len(message)+len(extra) <= maxWorkflowRetryMessage {
		return message + extra
	}
	// The feedback is what the repair acts on, so the request gives way first;
	// each keeps at least half of the budget.
	message = textutil.Truncate(message, max(maxWorkflowRetryMessage-len(extra), maxWorkflowRetryMessage/2))
	return message + textutil.Truncate(extra, maxWorkflowRetryMessage-len(message))
}

const maxWorkflowRetryMessage = 8192

func harnessProbeRetryDirective(message string, workflow *ChangeWorkflow, checks []CheckResult) string {
	if strings.Contains(message, "Harness² Propose-Probe-Compose:") {
		return ""
	}
	probe := probeWorkflowFriction(workflow, checks)
	if probe == "" {
		return ""
	}
	return "\n\nHarness² Propose-Probe-Compose:\n" + probe
}

// probeWorkflowFriction turns failed checks and a rejecting critic review
// into short directives for the next attempt.
func probeWorkflowFriction(workflow *ChangeWorkflow, checks []CheckResult) string {
	var directives []string
	for _, check := range checks {
		if !check.Required || check.State == CheckPassed || check.State == CheckSkipped {
			continue
		}
		if directive := failedCheckDirective(check); directive != "" && !slices.Contains(directives, directive) {
			directives = append(directives, directive)
		}
	}
	if workflow != nil && workflow.Review != nil && workflow.Review.Verdict == "changes_requested" {
		directives = append(directives, "Resolve every Critic review finding while keeping existing tests and architecture boundaries intact.")
	}
	return strings.Join(directives, " ")
}

func failedCheckDirective(check CheckResult) string {
	name := strings.ToLower(check.Name)
	switch {
	case name == "architecture boundaries":
		return "Remove forbidden cross-layer imports and call the owning domain/service interface instead."
	case name == "complexity gate":
		return "Bring every file and function the complexity gate names within its budget, using the numbers it reports: extract helpers from long or complex functions, and move cohesive parts of an over-long file into the sibling files the task already holds. What was already over may stay over but must not grow."
	case name == "secret & hygiene gate":
		return "Remove hardcoded tokens/keys and unresolved conflict markers; inject secrets from configuration or environment."
	case strings.HasPrefix(name, "parse:") || strings.HasPrefix(name, "syntax:") || strings.HasPrefix(name, "format:") || name == "lint" || strings.HasPrefix(name, "lint:"):
		return "Fix syntax, formatting, and static analysis diagnostics using sibling file declarations."
	case name == "tests" || strings.HasPrefix(name, "tests:") || strings.HasPrefix(name, "regression baseline"):
		return "Align implementation logic with failing test assertions without weakening pinned regression tests."
	default:
		return ""
	}
}

func criticReviewRetryFeedback(message string, workflow *ChangeWorkflow) string {
	if workflow == nil || workflow.Review == nil || workflow.Review.Verdict != "changes_requested" || strings.Contains(message, "Critic review feedback:") {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n\nCritic review feedback:\n" + strings.TrimSpace(workflow.Review.Summary))
	for _, finding := range workflow.Review.Findings {
		out.WriteString("\n- " + finding.String())
	}
	return out.String()
}

func workflowFailureRetryFeedback(message string, workflow *ChangeWorkflow, checks []CheckResult) string {
	if strings.Contains(message, "Previous check failures to resolve:") || strings.Contains(message, "Previous workflow failure to resolve:") {
		return ""
	}
	if evidence := failedWorkflowCheckEvidence(checks); evidence != "" {
		return "\n\nPrevious check failures to resolve:\n" + evidence
	}
	if workflow != nil && workflow.Status == "failed" && strings.TrimSpace(workflow.Reason) != "" && !strings.Contains(message, workflow.Reason) {
		return "\n\nPrevious workflow failure to resolve:\n" + strings.TrimSpace(workflow.Reason)
	}
	return ""
}

// failedWorkflowCheckEvidence is what a repair reads of the checks: the
// required ones that did not pass, each with what says why. Optional checks,
// such as the regression baseline of tests the old code cannot build, are
// not the repair's to fix.
func failedWorkflowCheckEvidence(checks []CheckResult) string {
	var text strings.Builder
	for _, check := range checks {
		if !check.Required || check.State == CheckPassed || check.State == CheckSkipped {
			continue
		}
		if text.Len() >= 2400 {
			break
		}
		output := strings.TrimSpace(sanitizeCheckOutput(check.Output, false, "", ""))
		if output == "" {
			output = check.State
		}
		text.WriteString(check.Name + " (" + check.State + "):\n" + checkFailureExcerpt(output, 1500) + "\n")
	}
	return strings.TrimSpace(text.String())
}

func workflowTestStageMessage(stageMessage string) string {
	for _, marker := range []string{
		"\n\nCritic review feedback:",
		"\n\nPrevious check failures to resolve:",
		"\n\nPrevious workflow failure to resolve:",
		"\n\nHarness² Propose-Probe-Compose:",
	} {
		if idx := strings.Index(stageMessage, marker); idx >= 0 {
			combined := workflowTestPrompt + stageMessage[idx:]
			return textutil.Truncate(combined, 8192)
		}
	}
	return workflowTestPrompt
}

func (s *Service) runWorkflowModelStage(ctx context.Context, job *changeWorkflowJob, session *ChangeSession, runtime modelRuntime, stage int, message string) (*ChangeSession, error) {
	tools := stage < 2 && toolStageAvailable(session, runtime)
	limit := duration(runtime.effective.Timeout)
	switch {
	case tools:
		limit = s.toolStageLimit(runtime)
	case stage == 2:
		limit *= time.Duration(s.workflowReviewWaves(ctx, len(job.specialists)))
	}
	timed, cancel := context.WithTimeoutCause(ctx, limit, &workflowTimeoutError{stage: []string{"Creation", "Testing", "Review"}[stage], limit: limit})
	defer cancel()
	// After a task's issue steps, the agents read only what it fixed. The
	// one-shot stages store the task they are given, so they keep all its
	// criteria and are told in the request which issues were left out.
	view := fixedIssuesView(session)
	var err error
	switch {
	case tools && stage == 0:
		session, err = s.runToolStage(timed, job.root, view, runtime, toolCoder, message)
	case tools:
		session, err = s.runToolStage(timed, job.root, view, runtime, toolTester, withTestDispute(session, strings.Replace(workflowTestStageMessage(message), workflowTestPrompt, toolTestPrompt, 1)))
	case stage == 0:
		isRepair := session.Workflow.AutoRepairAttempts > 0 || strings.Contains(message, "Previous check failures to resolve:")
		session, err = s.generateChangeWithRuntime(timed, job.root, session, ChangeMessageRequest{ChangeIdentity: identityForChange(session), Message: message + leftOutBrief(session), Repair: isRepair}, runtime)
	case stage == 1:
		session, err = s.generateWorkflowTests(timed, job.root, session, runtime, workflowTestStageMessage(message)+leftOutBrief(session))
	default:
		err = s.reviewWorkflowProposal(timed, job, view, runtime)
	}
	if timed.Err() != nil {
		return nil, context.Cause(timed)
	}
	if err == nil && stage < 2 {
		// The fix-up step makes what this stage wrote fit the project's
		// budgets; each of its model calls has its own time limit.
		session, err = s.fitChangeToBudgets(ctx, job.root, session, runtime, stage == 1)
	}
	return session, err
}

func (s *Service) generateWorkflowTests(ctx context.Context, root string, session *ChangeSession, runtime modelRuntime, message string) (*ChangeSession, error) {
	text, _, err := composeChangePromptContext(root, session)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(message) == "" {
		message = workflowTestPrompt
	}
	messages, err := changeMessages(session, message, text)
	if err != nil {
		return nil, err
	}
	messages[0].Content += " You are the testing agent. Only captured test files may appear in changes; implementation is fixed."
	schema := changeResponseSchema()
	result, err := s.requestWorkflowModel(ctx, root, session, runtime, messages, schema)
	if err != nil {
		return nil, err
	}
	explanation, edits, err := parseChangeResponse(root, result.Content, session)
	if err != nil {
		return nil, err
	}
	testEdits := make([]ChangeEdit, 0, len(edits))
	for _, edit := range edits {
		if isTestEditPath(session, edit.Path) {
			testEdits = append(testEdits, edit)
		}
	}
	if len(testEdits) == 0 {
		return nil, fmt.Errorf("testing agent attempted to change implementation")
	}
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, _, err := s.loadChangeForAction(ctx, session.ID, identityForChange(session))
	if err != nil {
		return nil, err
	}
	merged, err := setProposal(ctx, root, current, mergeWorkflowTests(append([]ChangeEdit(nil), current.Changes...), testEdits))
	if err != nil {
		return nil, err
	}
	current.Changes = merged
	current.Hash, current.Revision = changeProposalHash(current.Changes), current.Revision+1
	current.Checks, current.PinnedTests = []CheckResult{}, nil
	revokeApproval(current)
	now := time.Now().UTC()
	current.Messages = append(current.Messages, ChangeMessage{Role: "user", Content: message, CreatedAt: now}, ChangeMessage{Role: "assistant", Content: explanation, CreatedAt: now})
	current.UpdatedAt = now
	return current, storeChangeSession(root, current)
}

func mergeWorkflowTests(changes, tests []ChangeEdit) []ChangeEdit {
	for _, test := range tests {
		found := false
		for i := range changes {
			if changes[i].Path == test.Path {
				changes[i], found = test, true
				break
			}
		}
		if !found {
			changes = append(changes, test)
		}
	}
	return changes
}

func (s *Service) requestWorkflowModel(ctx context.Context, root string, session *ChangeSession, runtime modelRuntime, messages []llm.ChatMessage, schema llm.JSONSchema) (modelOutput, error) {
	if limit := changeContextByteLimit(runtime); limit > 0 && len(messages[0].Content)+len(messages[1].Content) > limit {
		return modelOutput{}, fmt.Errorf("selected context exceeds configured %s token limit", runtime.effective.Profile)
	}
	timed, cancel := context.WithTimeout(ctx, duration(runtime.effective.Timeout))
	defer cancel()
	authorize := func(ctx context.Context) error {
		s.changesMu.Lock()
		defer s.changesMu.Unlock()
		current, currentRoot, err := s.loadChangeForAction(ctx, session.ID, identityForChange(session))
		if err != nil {
			return err
		}
		if currentRoot != root || current.Workflow == nil || session.Workflow == nil || current.Workflow.ID != session.Workflow.ID {
			return fmt.Errorf("workflow identity changed before model dispatch")
		}
		return s.requireChangeExecutionTrust(ctx, current)
	}
	result, err := s.retryRequestAuthorized(timed, runtime, messages, &schema, authorize)
	if err == nil {
		err = timed.Err()
	}
	return result, err
}

// parseWorkflowReview reads a reviewer's reply about the proposal of session,
// placing its findings and notes in the task's files (session may be nil).
func parseWorkflowReview(output string, session *ChangeSession) (*ChangeWorkflowReview, error) {
	if len(output) > 32*1024 {
		return nil, fmt.Errorf("workflow review exceeds its limit")
	}
	output = llm.UnwrapMarkdownJSON(output)
	// The provider may not choose the proposal identity attached by the daemon.
	var wire struct {
		Verdict  string            `json:"verdict"`
		Summary  string            `json:"summary"`
		Findings []json.RawMessage `json:"findings"`
		Notes    []json.RawMessage `json:"notes"`
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || wire.Findings == nil || len(wire.Findings) > maxSpecialistFindings || len(wire.Notes) > maxSpecialistNotes {
		return nil, fmt.Errorf("invalid workflow review response")
	}
	files := reviewFiles(session)
	findings, err := decodeReviewItems(wire.Findings, files)
	if err != nil {
		return nil, err
	}
	notes, err := decodeReviewItems(wire.Notes, files)
	if err != nil {
		return nil, err
	}
	if len(notes) == 0 {
		notes = nil
	}
	review := &ChangeWorkflowReview{Verdict: wire.Verdict, Summary: wire.Summary, Findings: findings, Notes: notes}
	return review, validateWorkflowReview(review)
}

func validateWorkflowReview(review *ChangeWorkflowReview) error {
	if review.Verdict != "approve" && review.Verdict != "changes_requested" {
		return fmt.Errorf("invalid workflow review verdict")
	}
	limit := len(workflowReviewRoles)
	if !boundedWorkflowText(review.Summary, 4096) || len(review.Findings) > limit*maxSpecialistFindings || len(review.Notes) > limit*maxSpecialistNotes {
		return fmt.Errorf("invalid workflow review summary")
	}
	// Only blocking findings decide the verdict; advisory notes never do.
	if (review.Verdict == "approve") != (len(review.Findings) == 0) {
		return fmt.Errorf("workflow verdict and findings disagree")
	}
	for _, item := range slices.Concat(review.Findings, review.Notes) {
		if !validReviewItem(item) {
			return fmt.Errorf("invalid workflow review finding")
		}
	}
	return nil
}

const (
	maxSpecialistFindings = 16
	maxSpecialistNotes    = 8
	maxOverrideReason     = 1024
	maxApprovalNotes      = 2048
)

func boundedWorkflowText(text string, limit int) bool {
	return strings.TrimSpace(text) != "" && len(text) <= limit && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}
