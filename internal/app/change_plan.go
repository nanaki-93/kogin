package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
}

type ChangePlan struct {
	ChangePlanInput
	ReadTargets  []ChangeTarget `json:"read_targets,omitempty"`
	Hash         string         `json:"hash"`
	ApprovedHash string         `json:"approved_hash,omitempty"`
}

func cloneChangePlanInput(input ChangePlanInput) ChangePlanInput {
	input.NonGoals = slices.Clone(input.NonGoals)
	input.ReadPaths = slices.Clone(input.ReadPaths)
	input.Citations = slices.Clone(input.Citations)
	input.Assumptions = slices.Clone(input.Assumptions)
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
	for _, value := range append([]string{input.Problem, input.Evidence, input.Approach}, input.NonGoals...) {
		if len(value) > 8192 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("plan text must be UTF-8 without NUL and at most 8192 bytes per field")
		}
	}
	return nil
}

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
	if err := validateChangePlanInput(p.ChangePlanInput); err != nil {
		return err
	}
	if p.Hash != changePlanHash(session) || p.ApprovedHash != "" && p.ApprovedHash != p.Hash {
		return fmt.Errorf("plan or captured source identity changed; review the approach again")
	}
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
	return validateCapturedPlanEvidence(session)
}

func requireApprovedChangePlan(session *ChangeSession) error {
	if err := validateStoredPlan(session); err != nil {
		return err
	}
	if session.Plan != nil && session.Plan.ApprovedHash != session.Plan.Hash {
		return fmt.Errorf("approve the current plan before generating code")
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
