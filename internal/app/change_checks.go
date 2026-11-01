package app

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app/archgate"
	"github.com/nanaki-93/mini-orca/v2/internal/app/sandbox"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/project/rules"
	"github.com/nanaki-93/mini-orca/v2/internal/textutil"
)

func (s *Service) CheckChange(ctx context.Context, id string, request ChangeCheckRequest) (*ChangeSession, error) {
	s.changesMu.Lock()
	session, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	s.changesMu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(session.Changes) == 0 {
		return nil, fmt.Errorf("prepare a proposal before checking")
	}
	request.RunTests = request.RunTests || session.CheckOptions.RunTests
	request.RunLint = request.RunLint || session.CheckOptions.RunLint
	if request.RunTests || request.RunLint {
		if err := s.requireChangeExecutionTrust(ctx, session); err != nil {
			return nil, err
		}
	}
	checks, budgets := changeSourceChecks(root, session.Targets, session.Changes)
	s.recordBudgetDebt(budgets)
	var checked string // The whole-tree fingerprint the checks describe.
	if requiredChecksPassed(checks) && (request.RunTests || request.RunLint) {
		executed, fingerprint, err := s.checkChangeWorkspace(ctx, root, session, request.CheckOptions)
		if err != nil {
			return nil, err
		}
		checks, checked = append(checks, executed...), fingerprint
	} else if checked, err = workspaceFingerprint(ctx, root); err != nil {
		return nil, err
	}
	return s.publishChangeChecks(ctx, id, request, checks, checked)
}

func (s *Service) publishChangeChecks(ctx context.Context, id string, request ChangeCheckRequest, checks []CheckResult, checked string) (*ChangeSession, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	current, root, err := s.loadChangeForAction(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	current.Checks, current.ChecksWorkspaceHash, current.UpdatedAt = checks, checked, time.Now().UTC()
	revokeApproval(current)
	current.CheckOptions = request.CheckOptions
	if current.Workflow != nil {
		reconcileWorkflowAfterChecks(current, checks, ctx.Value(changeWorkflowContextKey{}) != nil)
	}
	pinChangeRegression(current, checks)
	if err := storeChangeSession(root, current); err != nil {
		return nil, err
	}
	s.changeAuthority[current.ProjectID+"/"+id] = "checks:" + current.Hash
	return current, nil
}

func reconcileWorkflowAfterChecks(current *ChangeSession, checks []CheckResult, inWorkflowJob bool) {
	wf := current.Workflow
	if !inWorkflowJob && !reconcileManualCheckStages(wf, current.Hash, checks) {
		return
	}
	if canHumanReviewProposal(current) {
		markLeadingWorkflowStagesCompleted(wf)
		if len(wf.Stages) > 3 {
			wf.Stages[3].Status = "waiting"
		}
		return
	}
	if len(wf.Stages) > 3 {
		wf.Stages[3].Status = "blocked"
	}
}

func reconcileManualCheckStages(wf *ChangeWorkflow, hash string, checks []CheckResult) bool {
	if !requiredChecksPassed(checks) {
		wf.Status, wf.Reason = workflowFailure(errWorkflowChecksFailed, checks)
		if len(wf.Stages) > 0 && (wf.Stages[0].Status == "failed" || wf.Stages[0].Status == "interrupted") {
			wf.Stages[0].Status, wf.Stages[0].Reason = "completed", ""
		}
		if len(wf.Stages) > 1 {
			wf.Stages[1].Status, wf.Stages[1].Reason = "failed", wf.Reason
		}
		if len(wf.Stages) > 3 {
			wf.Stages[3].Status = "blocked"
		}
		return false
	}
	if len(wf.Stages) > 0 {
		wf.Stages[0].Status, wf.Stages[0].Reason = "completed", ""
	}
	if len(wf.Stages) > 1 {
		wf.Stages[1].Status, wf.Stages[1].Reason = "completed", ""
	}
	restoreReviewOutcome(wf, hash)
	return true
}

// restoreReviewOutcome returns a workflow whose checks pass again to the state
// its review at hash established: human review after approval or an override,
// otherwise the reviewers' requested changes that a failed recheck hid.
func restoreReviewOutcome(wf *ChangeWorkflow, hash string) {
	if !inputAllowsReview(wf) || wf.Review == nil || wf.Review.ProposalHash != hash {
		return
	}
	switch {
	case wf.Review.Verdict == "approve" || reviewOverridden(wf, hash):
		wf.Status, wf.Reason = "awaiting_human_review", ""
	case wf.Status == "failed" && wf.Input == nil:
		wf.Status, wf.Reason = "changes_requested", ""
	}
}

func markLeadingWorkflowStagesCompleted(wf *ChangeWorkflow) {
	for i := 0; i < 3 && i < len(wf.Stages); i++ {
		wf.Stages[i].Status, wf.Stages[i].Reason = "completed", ""
	}
}

func pinChangeRegression(session *ChangeSession, checks []CheckResult) {
	if !requiredChecksPassed(checks) {
		return
	}
	for _, check := range checks {
		if strings.HasPrefix(check.Name, "regression baseline") && (check.State == CheckPassed || check.Outcome != "" && check.State != CheckCanceled) {
			session.PinnedTests = changeTestEdits(session)
		}
	}
}

// changeSourceChecks runs the checks that read the proposed files without
// executing anything. It also returns how each file stands against its
// budgets, so already over-budget files can be tracked as refactor issues.
func changeSourceChecks(root string, targets []ChangeTarget, edits []ChangeEdit) ([]CheckResult, []archgate.BudgetReport) {
	checks := []CheckResult{}
	for _, edit := range edits {
		if edit.Deleted() {
			continue
		}
		if filepath.Ext(edit.Path) != ".go" {
			checks = append(checks, archgate.SyntaxCheck(targets, edit))
			continue
		}
		parsed := parseGoSource(edit.Path, edit.Content)
		parsed.Name += ": " + edit.Path
		checks = append(checks, parsed)
		formatted, err := format.Source([]byte(edit.Content))
		check := CheckResult{Name: "format: " + edit.Path, Required: true, State: CheckPassed}
		if err != nil || !bytes.Equal(formatted, []byte(edit.Content)) {
			check.State, check.Output = CheckFailed, "Candidate must parse and be gofmt-formatted."
		}
		checks = append(checks, check)
	}
	gates, budgets := archgate.ChangeArchitectureAndComplexityChecks(root, targets, edits)
	checks = append(checks, gates...)
	if len(edits) > 0 {
		checks = append(checks, archgate.CheckChangeSecretAndHygiene(edits))
	}
	return checks, budgets
}

// checkChangeWorkspace runs checks in an isolated copy and returns the
// whole-tree fingerprint of the project the copy reproduces.
func (s *Service) checkChangeWorkspace(ctx context.Context, root string, session *ChangeSession, options CheckOptions) ([]CheckResult, string, error) {
	workspace, err := os.MkdirTemp("", "kasuri-change-check-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(workspace)
	fingerprint, err := s.prepareCheckCopy(ctx, root, workspace, session)
	if err != nil {
		return nil, "", err
	}
	revision, err := s.changeExecutionRevision(session)
	if err != nil {
		return nil, "", err
	}
	run := &checkRun{s: s, ctx: ctx, root: root, revision: revision, fingerprint: fingerprint, session: session}
	if ownsWorktree(session) {
		// The worktree holds the proposal; its base is the tree without it.
		run.revert = func(dir string) error { return revertProposalInTree(dir, session) }
		run.baseKey = "worktree base " + session.Worktree.Base
	}
	defer run.cleanup()
	checks, err := s.runProfileChecks(run, workspace, options)
	return checks, fingerprint, err
}

// prepareCheckCopy copies the project and validates the copy: it must equal
// the tree it was copied from and, for a path-scoped session, hold the
// captured sources and freshness scope. Legacy sessions require the tree they
// were captured from.
func (s *Service) prepareCheckCopy(ctx context.Context, root, workspace string, session *ChangeSession) (string, error) {
	expected := session.WorkspaceHash
	if session.ScopeVersion != 0 {
		live, err := workspaceFingerprint(ctx, root)
		if err != nil {
			return "", err
		}
		expected = live
	}
	if err := copyCheckWorkspace(ctx, root, workspace); err != nil {
		return "", err
	}
	fingerprint, err := fingerprintCheckWorkspace(ctx, workspace)
	if err != nil {
		return "", err
	}
	if fingerprint != expected {
		return "", fmt.Errorf("%w: the project changed while checks were prepared", project.ErrRevisionConflict)
	}
	if session.ScopeVersion == 0 {
		return fingerprint, nil
	}
	if ownsWorktree(session) {
		return fingerprint, verifyProposalInTree(workspace, session)
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return "", err
	}
	return fingerprint, verifyCapturedSources(workspace, policy, session)
}

func stageChangeEdits(workspace string, edits []ChangeEdit) error {
	for _, edit := range edits {
		if edit.OldPath != "" {
			if err := removeWorkspaceFile(workspace, edit.OldPath); err != nil {
				return err
			}
		}
		if edit.Deleted() {
			if err := removeWorkspaceFile(workspace, edit.Path); err != nil {
				return err
			}
			continue
		}
		path, err := project.ResolveWritePath(workspace, edit.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(edit.Content), 0600); err != nil {
			return err
		}
	}
	return nil
}

func removeWorkspaceFile(workspace, file string) error {
	path, err := project.ResolveWritePath(workspace, file)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// isTestTarget reports a test file in the task scope, by convention or by a
// test_files pattern of the project's AGENTS.md.
func isTestTarget(target ChangeTarget) bool {
	return target.Test || rules.IsConventionalTestFile(target.Path)
}

// isTestEditPath reports whether a proposed file is a test file.
func isTestEditPath(session *ChangeSession, path string) bool {
	if target := changeTarget(session, path); target != nil {
		return isTestTarget(*target)
	}
	return rules.IsConventionalTestFile(path)
}

func changeTestEdits(session *ChangeSession) []ChangeEdit {
	result := []ChangeEdit{}
	for _, edit := range session.Changes {
		if isTestEditPath(session, edit.Path) {
			result = append(result, edit)
		}
	}
	return result
}

func newChangeTestEdits(session *ChangeSession) []ChangeEdit {
	result := []ChangeEdit{}
	for _, edit := range changeTestEdits(session) {
		if target := changeTarget(session, edit.Path); target != nil && !target.Exists {
			result = append(result, edit)
		}
	}
	return result
}

func (s *Service) RepairChangeMessage(ctx context.Context, id string, request ChangeMessageRequest) (*ChangeSession, error) {
	userGuidance := strings.TrimSpace(request.Message)
	runtime, err := s.changeMessageRuntime(ctx, request)
	if err != nil {
		return nil, err
	}
	session, root, err := s.reserveChangeRepair(ctx, id, request.ChangeIdentity)
	if err != nil {
		return nil, err
	}
	prompt := "Repair the current proposal (including any test compile or assertion errors) without changing scope or acceptance criteria. Check evidence:\n" + changeCheckEvidence(session.Checks)
	if userGuidance != "" && userGuidance != "Repair the current proposal to satisfy failed checks." {
		prompt += "\n\nUser repair guidance:\n" + userGuidance
	}
	request.Message = prompt
	return s.generateChangeWithRuntime(ctx, root, session, request, runtime)
}

func (s *Service) reserveChangeRepair(ctx context.Context, id string, identity ChangeIdentity) (*ChangeSession, string, error) {
	s.changesMu.Lock()
	defer s.changesMu.Unlock()
	session, root, err := s.loadChangeForAction(ctx, id, identity)
	if err != nil {
		return nil, root, err
	}
	if err := s.requireRepairableChange(session); err != nil {
		return nil, root, err
	}
	session.RepairAttempts++
	if err := storeChangeSession(root, session); err != nil {
		return nil, root, err
	}
	return session, root, nil
}

func (s *Service) requireRepairableChange(session *ChangeSession) error {
	hasFailedChecks := len(session.Checks) > 0 && !requiredChecksPassed(session.Checks) && session.ReviewedHash == ""
	if session.RepairAttempts >= 3 || len(session.Checks) == 0 || requiredChecksPassed(session.Checks) || (s.changeAuthority[session.ProjectID+"/"+session.ID] != "checks:"+session.Hash && !hasFailedChecks) {
		return fmt.Errorf("repair requires current failed checks and fewer than three attempts")
	}
	return nil
}

func changeCheckEvidence(checks []CheckResult) string {
	var text strings.Builder
	for _, check := range checks {
		if text.Len() >= 4096 {
			break
		}
		text.WriteString(check.Name + ": " + check.State + "\n" + sanitizeCheckOutput(check.Output, false, "", "") + "\n")
	}
	result := text.String()
	return textutil.Truncate(result, 4096)
}

type workspaceIdentity struct {
	fingerprint string
}

func workspaceFingerprint(ctx context.Context, root string) (string, error) {
	return sandbox.WorkspaceFingerprint(ctx, root)
}

func fingerprintCheckWorkspace(ctx context.Context, root string) (string, error) {
	return sandbox.FingerprintWorkspace(ctx, root)
}

func (fixture workspaceIdentity) verifyCurrent(ctx context.Context, root string) error {
	return sandbox.VerifyWorkspaceFingerprint(ctx, root, fixture.fingerprint)
}
