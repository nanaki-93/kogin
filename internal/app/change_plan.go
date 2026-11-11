package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// ChangePlanInput is user-editable task intent, separate from proposed source.
type ChangePlanInput struct {
	Problem     string         `json:"problem"`
	Evidence    string         `json:"evidence,omitempty"`
	Approach    string         `json:"approach"`
	NonGoals    []string       `json:"non_goals,omitempty"`
	ReadPaths   []string       `json:"read_paths,omitempty"`
	Citations   []PlanEvidence `json:"citations,omitempty"`
	Assumptions []string       `json:"assumptions,omitempty"`
	// Claim is the problem in one plain sentence for someone who has not
	// read the code: no identifiers, files or line numbers.
	Claim string `json:"claim,omitempty"`
	// RootCause is why the problem happens, naming the code; the citations
	// are the lines that show it.
	RootCause string `json:"root_cause,omitempty"`
	// Alternatives are approaches considered and rejected.
	Alternatives []PlanAlternative `json:"alternatives,omitempty"`
	// Risks are what the change could break.
	Risks []string `json:"risks,omitempty"`
	// Touches are the files the change will edit or create, and why each.
	Touches []PlanTouch `json:"touches,omitempty"`
	// IssueVerdicts are what the investigation found for each issue a task
	// fixes, before any code is written.
	IssueVerdicts []PlanIssueVerdict `json:"issue_verdicts,omitempty"`
}

// PlanAlternative is an approach the plan rejected, and why.
type PlanAlternative struct {
	Approach string `json:"approach"`
	WhyNot   string `json:"why_not"`
}

// PlanTouch is a file the plan expects the change to edit or create.
type PlanTouch struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// PlanIssueVerdict is what the investigation found for one linked issue:
// holds (the default work), already_fixed, does_not_hold or needs_own_task.
type PlanIssueVerdict struct {
	FindingID string `json:"finding_id"`
	Verdict   string `json:"verdict"`
	Note      string `json:"note,omitempty"`
}

// How a plan verdict reads; already_fixed, does_not_hold and needs_own_task
// reuse the issue step outcomes.
const planVerdictHolds = "holds"

// planSourceInvestigator marks a plan the investigation agent proposed, which
// a human approves or redirects before the agents write code.
const planSourceInvestigator = "investigator"

type ChangePlan struct {
	ChangePlanInput
	ReadTargets  []ChangeTarget `json:"read_targets,omitempty"`
	Hash         string         `json:"hash"`
	ApprovedHash string         `json:"approved_hash,omitempty"`
	// Source is who wrote the plan: empty for the user at task creation, or
	// investigator for the investigation agent. Model and InvestigatedAt say
	// which model proposed it and when.
	Source         string    `json:"source,omitempty"`
	Model          string    `json:"model,omitempty"`
	InvestigatedAt time.Time `json:"investigated_at,omitempty"`
}

func cloneChangePlanInput(input ChangePlanInput) ChangePlanInput {
	input.NonGoals = slices.Clone(input.NonGoals)
	input.ReadPaths = slices.Clone(input.ReadPaths)
	input.Citations = slices.Clone(input.Citations)
	input.Assumptions = slices.Clone(input.Assumptions)
	input.Alternatives = slices.Clone(input.Alternatives)
	input.Risks = slices.Clone(input.Risks)
	input.Touches = slices.Clone(input.Touches)
	input.IssueVerdicts = slices.Clone(input.IssueVerdicts)
	return input
}

func captureChangePlan(root string, session *ChangeSession, input *ChangePlanInput) error {
	if input == nil {
		return nil // The existing direct proposal API and historical conversations remain supported.
	}
	if err := validateChangePlanInput(*input); err != nil {
		return err
	}
	session.Plan = &ChangePlan{ChangePlanInput: cloneChangePlanInput(*input)}
	seen := map[string]bool{}
	for _, path := range input.ReadPaths {
		if seen[path] || changeTarget(session, path) != nil {
			return fmt.Errorf("read context paths must be distinct from each other and write targets")
		}
		seen[path] = true
		target, err := captureChangeTarget(root, path)
		if err != nil {
			return err
		}
		if !target.Exists {
			return fmt.Errorf("read context path %q does not exist", path)
		}
		instructions, err := project.ResolveInstructions(root, path)
		if err != nil {
			return err
		}
		session.Instructions[path] = instructions.Fingerprint
		session.Plan.ReadTargets = append(session.Plan.ReadTargets, target)
	}
	if err := validateCapturedPlanEvidence(session); err != nil {
		return err
	}
	session.Plan.Hash = changePlanHash(session)
	return nil
}

func validateChangePlanInput(input ChangePlanInput) error {
	if strings.TrimSpace(input.Problem) == "" || strings.TrimSpace(input.Approach) == "" {
		return fmt.Errorf("provide the problem and proposed approach before approving the plan")
	}
	if len(input.NonGoals) > 16 || len(input.ReadPaths) > 8 {
		return fmt.Errorf("plan allows at most 16 boundaries and 8 read context paths")
	}
	if len(input.Citations) > 8 || len(input.Assumptions) > 8 {
		return fmt.Errorf("plan allows at most eight citations and assumptions")
	}
	for _, assumption := range input.Assumptions {
		if !validInvestigationText(assumption, 512) {
			return fmt.Errorf("invalid plan assumption")
		}
	}
	for _, value := range append([]string{input.Problem, input.Evidence, input.Approach, input.RootCause, input.Claim}, input.NonGoals...) {
		if len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("plan text must be UTF-8 without NUL and at most 8192 bytes per field")
		}
	}
	return validatePlanInvestigation(input)
}

// validatePlanInvestigation bounds the fields an investigation adds to a plan.
func validatePlanInvestigation(input ChangePlanInput) error {
	if len(input.Alternatives) > maxPlanAlternatives || len(input.Risks) > maxPlanRisks || len(input.Touches) > maxStoredTaskPaths || len(input.IssueVerdicts) > maxIssuesPerTask {
		return fmt.Errorf("plan allows at most %d alternatives, %d risks, %d touched files and %d issue verdicts", maxPlanAlternatives, maxPlanRisks, maxStoredTaskPaths, maxIssuesPerTask)
	}
	for _, alternative := range input.Alternatives {
		if !validInvestigationText(alternative.Approach, 512) || !validInvestigationText(alternative.WhyNot, 512) {
			return fmt.Errorf("invalid plan alternative")
		}
	}
	for _, risk := range input.Risks {
		if !validInvestigationText(risk, 512) {
			return fmt.Errorf("invalid plan risk")
		}
	}
	for _, touch := range input.Touches {
		if clean, err := cleanToolPath(touch.Path); err != nil || clean != touch.Path || !validInvestigationText(touch.Why, 512) {
			return fmt.Errorf("a plan's touched file names one project file and why")
		}
	}
	return validatePlanVerdicts(input.IssueVerdicts)
}

// validatePlanVerdicts checks a plan's issue verdicts: one per issue, each a
// known verdict with bounded text.
func validatePlanVerdicts(verdicts []PlanIssueVerdict) error {
	seen := map[string]bool{}
	for _, verdict := range verdicts {
		if verdict.FindingID == "" || seen[verdict.FindingID] {
			return fmt.Errorf("each issue gets one plan verdict")
		}
		seen[verdict.FindingID] = true
		if !slices.Contains([]string{planVerdictHolds, outcomeAlreadyFixed, outcomeDoesNotHold, outcomeNeedsOwnTask}, verdict.Verdict) {
			return fmt.Errorf("a plan verdict is holds, already_fixed, does_not_hold or needs_own_task")
		}
		if verdict.Note != "" && !validInvestigationText(verdict.Note, 512) {
			return fmt.Errorf("invalid plan verdict note")
		}
	}
	return nil
}

const (
	maxPlanAlternatives = 4
	maxPlanRisks        = 6
)

func validateCapturedPlanEvidence(session *ChangeSession) error {
	sources := map[string]string{}
	for _, target := range session.Targets {
		sources[target.Path] = target.Content
	}
	for _, target := range session.Plan.ReadTargets {
		sources[target.Path] = target.Content
	}
	for _, citation := range session.Plan.Citations {
		if err := validatePlanCitation(citation, sources[citation.Path]); err != nil {
			return err
		}
	}
	return nil
}

func changePlanHash(session *ChangeSession) string {
	input := struct {
		Kind, Title  string
		Plan         ChangePlanInput
		ReadTargets  []ChangeTarget
		Targets      []ChangeTarget
		Criteria     []string
		Instructions map[string]string
		Project      string
		Revision     string
		Workspace    string
		Policy       string
	}{session.Kind, session.Title, session.Plan.ChangePlanInput, session.Plan.ReadTargets, session.Targets, session.AcceptanceCriteria,
		session.Instructions, session.ProjectID, session.ProjectRevision, session.WorkspaceHash, session.PolicyFingerprint}
	data, _ := json.Marshal(input) // Only strings, slices and maps; no unsupported JSON values.
	return contentHash(data)
}

func validateStoredPlan(session *ChangeSession) error {
	if session.Plan == nil {
		return nil
	}
	p := session.Plan
	if len(session.AcceptanceCriteria) == 0 {
		return fmt.Errorf("provide acceptance criteria for the plan")
	}
	if p.Source != "" && p.Source != planSourceInvestigator {
		return fmt.Errorf("corrupt plan source")
	}
	if err := validateChangePlanInput(p.ChangePlanInput); err != nil {
		return err
	}
	if p.Hash != changePlanHash(session) || p.ApprovedHash != "" && p.ApprovedHash != p.Hash {
		return fmt.Errorf("plan or captured source identity changed; review the approach again")
	}
	if err := validatePlanReadTargets(session, p); err != nil {
		return err
	}
	return validateCapturedPlanEvidence(session)
}

// validatePlanReadTargets checks a plan's read context: each read path once,
// captured with its content and never a write target too.
func validatePlanReadTargets(session *ChangeSession, p *ChangePlan) error {
	if len(p.ReadPaths) != len(p.ReadTargets) {
		return fmt.Errorf("corrupt plan context")
	}
	seen := map[string]bool{}
	for i, target := range p.ReadTargets {
		if seen[target.Path] || changeTarget(session, target.Path) != nil {
			return fmt.Errorf("corrupt plan context scope")
		}
		seen[target.Path] = true
		if target.Path != p.ReadPaths[i] || !target.Exists || target.Hash != contentHash([]byte(target.Content)) {
			return fmt.Errorf("corrupt plan context identity")
		}
	}
	return nil
}

func requireApprovedChangePlan(session *ChangeSession) error {
	if err := validateStoredPlan(session); err != nil {
		return err
	}
	if session.Plan != nil && session.Plan.ApprovedHash != session.Plan.Hash {
		if session.Plan.Source == planSourceInvestigator {
			return fmt.Errorf("approve the proposed plan, or redirect it with a note, before code is generated")
		}
		return fmt.Errorf("approve the current plan before generating code")
	}
	return nil
}

func (s *Service) approveRunPlan(root string, session *ChangeSession, request ChangeWorkflowRequest) error {
	alreadyApproved := session.Plan != nil && session.Plan.ApprovedHash != "" && session.Plan.ApprovedHash == session.Plan.Hash
	if err := approveWorkflowPlan(session, request.PlanHash); err != nil {
		return err
	}
	if (!alreadyApproved && request.PlanHash != "") || len(request.ApprovedPaths) > 0 {
		return s.applyApprovedPlanScope(root, session, request.ApprovedPaths)
	}
	return nil
}

func approveWorkflowPlan(session *ChangeSession, hash string) error {
	if session.Plan == nil {
		if hash != "" {
			return fmt.Errorf("this conversation has no captured plan")
		}
		return nil
	}
	if hash == "" && session.Plan.ApprovedHash != "" && session.Plan.ApprovedHash == session.Plan.Hash {
		return nil
	}
	if hash == "" || hash != session.Plan.Hash {
		return fmt.Errorf("approve the displayed plan and captured source before generation")
	}
	session.Plan.ApprovedHash = hash
	return nil
}

func (s *Service) captureChangePromptSnapshot(ctx context.Context, root string, session *ChangeSession, plan *ChangePlanInput) error {
	var err error
	session.WorkspaceHash, err = workspaceFingerprint(ctx, root)
	if err != nil {
		return err
	}
	if err := captureChangePlan(root, session, plan); err != nil {
		return err
	}
	if err := captureFreshnessScope(root, session); err != nil {
		return err
	}
	_, manifest, err := composeChangePromptContext(root, session)
	if err != nil {
		return err
	}
	session.ContextManifest = s.contextManifestForRuntime(manifest, s.conf().runtimes.function)
	return nil
}
