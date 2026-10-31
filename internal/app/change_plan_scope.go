package app

import "slices"

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
	scope.plan.ReadTargets = slices.DeleteFunc(scope.plan.ReadTargets, func(read ChangeTarget) bool { return read.Path == path })
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
