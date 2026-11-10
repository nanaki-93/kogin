package app

import (
	"fmt"
	"slices"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// planScope keeps a task's plan bound to the task while its scope grows: in
// the task's own worktree, the agents (or the user) may change files outside
// the captured scope, and each such file becomes a target. The plan's text
// does not change, so a plan that matched its task stays valid, and approved
// if it was; otherwise every later save of the task would be refused.
type planScope struct {
	plan     *ChangePlan
	bound    bool // the plan matched the task before its scope grew
	approved bool
	grown    bool
}

// bindPlanScope gives session its own copy of the plan, so a scratch copy of
// a task never changes the plan of the task it was copied from.
func bindPlanScope(session *ChangeSession) *planScope {
	scope := &planScope{}
	if session.Plan == nil {
		return scope
	}
	plan := *session.Plan
	plan.ChangePlanInput = cloneChangePlanInput(plan.ChangePlanInput)
	plan.ReadTargets = slices.Clone(plan.ReadTargets)
	session.Plan, scope.plan = &plan, &plan
	scope.bound = plan.Hash == changePlanHash(session)
	scope.approved = scope.bound && plan.ApprovedHash == plan.Hash
	return scope
}

// add notes that path became a target. A file the plan only read leaves its
// read context: a target is never read context too.
func (scope *planScope) add(path string) {
	scope.grown = true
	if scope.plan == nil {
		return
	}
	scope.plan.ReadPaths = slices.DeleteFunc(scope.plan.ReadPaths, func(read string) bool { return read == path })
	if len(scope.plan.ReadPaths) == 0 {
		scope.plan.ReadPaths = nil
	}
	scope.plan.ReadTargets = slices.DeleteFunc(scope.plan.ReadTargets, func(read ChangeTarget) bool { return read.Path == path })
	if len(scope.plan.ReadTargets) == 0 {
		scope.plan.ReadTargets = nil
	}
}

// rebind binds the plan to the grown scope.
func (scope *planScope) rebind(session *ChangeSession) {
	if scope.plan == nil || !scope.bound || !scope.grown {
		return
	}
	scope.plan.Hash = changePlanHash(session)
	if scope.approved {
		scope.plan.ApprovedHash = scope.plan.Hash
	}
}

// approvedScopePaths returns the target paths to capture when an investigated
// plan is approved: the request's explicit approved_paths, or, when the task
// started with no files yet, the plan's touched files within maxTaskPaths.
func approvedScopePaths(session *ChangeSession, requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	if len(session.Targets) > 0 || session.Plan == nil || session.Plan.Source != planSourceInvestigator {
		return nil
	}
	var paths []string
	for _, touch := range session.Plan.Touches {
		if !slices.Contains(paths, touch.Path) {
			paths = append(paths, touch.Path)
			if len(paths) == maxTaskPaths {
				break
			}
		}
	}
	return paths
}

// applyApprovedPlanScope sets the task's targets to the files confirmed with an
// approved investigator plan, keeping cited files verifiable and the plan's
// hash bound to the updated scope.
func (s *Service) applyApprovedPlanScope(root string, session *ChangeSession, requested []string) error {
	desired := approvedScopePaths(session, requested)
	if len(desired) == 0 {
		return nil
	}
	if !ownsWorktree(session) || len(session.Changes) > 0 || session.Plan == nil || session.Plan.Source != planSourceInvestigator {
		return fmt.Errorf("approved paths apply only when approving an investigated plan before code is written")
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return err
	}
	scope := bindPlanScope(session)
	if err := retainDroppedCitations(session, scope.plan, desired); err != nil {
		return err
	}
	targets, instructions, err := captureApprovedTargets(root, policy, session, scope, desired)
	if err != nil {
		return err
	}
	session.Targets, session.Instructions = targets, instructions
	if err := validateChangeFindingTargets(session); err != nil {
		return err
	}
	scope.grown = true
	scope.rebind(session)
	return s.refreshApprovedPromptScope(root, session)
}

func retainDroppedCitations(session *ChangeSession, plan *ChangePlan, desired []string) error {
	for _, target := range session.Targets {
		if slices.Contains(desired, target.Path) || slices.Contains(plan.ReadPaths, target.Path) {
			continue
		}
		if !slices.ContainsFunc(plan.Citations, func(c PlanEvidence) bool { return c.Path == target.Path }) {
			continue
		}
		if !target.Exists {
			return fmt.Errorf("cited file %s must stay in the approved scope", target.Path)
		}
		if len(plan.ReadPaths) >= 8 {
			return fmt.Errorf("plan allows at most 8 read context paths")
		}
		plan.ReadPaths = append(plan.ReadPaths, target.Path)
		plan.ReadTargets = append(plan.ReadTargets, target)
	}
	return nil
}

func captureApprovedTargets(root string, policy *project.ContextPolicy, session *ChangeSession, scope *planScope, desired []string) ([]ChangeTarget, map[string]string, error) {
	targets := make([]ChangeTarget, 0, len(desired))
	instructions := map[string]string{}
	for _, file := range desired {
		if err := worktreeWritable(policy, session, file); err != nil {
			return nil, nil, err
		}
		target, err := captureChangeTarget(root, file)
		if err != nil {
			return nil, nil, err
		}
		inst, err := project.ResolveInstructions(root, file)
		if err != nil {
			return nil, nil, err
		}
		scope.add(file)
		targets = append(targets, target)
		instructions[file] = inst.Fingerprint
	}
	for _, read := range scope.plan.ReadTargets {
		inst, err := project.ResolveInstructions(root, read.Path)
		if err != nil {
			return nil, nil, err
		}
		instructions[read.Path] = inst.Fingerprint
	}
	return targets, instructions, nil
}

func (s *Service) refreshApprovedPromptScope(root string, session *ChangeSession) error {
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
