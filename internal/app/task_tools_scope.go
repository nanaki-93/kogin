package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// A fix that needs code outside its folders may widen its scope instead of
// stopping: an agent asks for the files with widen_scope, and Kasuri grants
// those the task's own code imports, within limits. The grants stay on the
// task, so the reviewers and you see what was added and why.
const (
	maxScopeGrantFolders = 2
	maxScopeFiles        = 8
	maxScopeGrantReason  = 500
)

func (run *taskToolRun) scopeTools() []toolSpec {
	if run.role != toolCoder || issueFixScope(run.session) == nil {
		return nil
	}
	return []toolSpec{{name: "widen_scope", call: run.widenScope,
		description: fmt.Sprintf("Ask to change code outside this fix's folders when the fix needs it. Kasuri allows files in the project's own folders that this task's code imports, at most %d more folders and %d code files in the task in total. Say why the fix needs them; reviewers see it. If it is refused, leave those files and finish with outcome needs_own_task.", maxScopeGrantFolders, maxScopeFiles),
		schema:      fmt.Sprintf(`{"type":"object","properties":{"paths":{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"string","minLength":1}},"reason":{"type":"string","minLength":1,"maxLength":%d}},"required":["paths","reason"],"additionalProperties":false}`, maxScopeFiles, maxScopeGrantReason)}}
}

// scope is the run's fix scope with what it was granted so far.
func (run *taskToolRun) scope() *fixScope {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.scopeLocked()
}

func (run *taskToolRun) scopeLocked() *fixScope {
	view := *run.session
	view.ScopeGrants = append(slices.Clone(run.session.ScopeGrants), run.grants...)
	return issueFixScope(&view)
}

func (run *taskToolRun) widenScope(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Paths  []string `json:"paths"`
		Reason string   `json:"reason"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	reason := strings.TrimSpace(input.Reason)
	if len(input.Paths) == 0 || len(input.Paths) > maxScopeFiles || reason == "" || len(reason) > maxScopeGrantReason {
		return "", fmt.Errorf("name one to %d files and why the fix needs them, in at most %d bytes", maxScopeFiles, maxScopeGrantReason)
	}
	files, err := run.scopeRequest(input.Paths)
	if err != nil {
		return "", err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	wanted, err := run.grantable(files)
	if err != nil {
		return "", err
	}
	if len(wanted) == 0 {
		return "These files are already in this fix's scope: change them as the fix needs.", nil
	}
	wait, err := run.s.blockingTask(ctx, run.session.ID, wanted)
	if err != nil {
		return "", err
	}
	if wait != nil {
		run.wait = wait
		return "", fmt.Errorf("%s: another task, “%s”, is changing it and is not merged yet. Stop here and call finish; Kasuri continues this issue by itself on the updated code once that task is merged", strings.Join(wait.Files, ", "), wait.Title)
	}
	issue := run.fixingIssue()
	for _, file := range wanted {
		run.grants = append(run.grants, ScopeGrant{Path: file, Reason: reason, Issue: issue})
	}
	return fmt.Sprintf("Granted: you may now change %s. They are part of this task, and the reviewers see why: %s", strings.Join(wanted, ", "), reason), nil
}

// grantable is what of files the scope does not cover yet, when all of it
// may be granted: code the task's code imports, within the limits. Caller
// holds run.mu.
func (run *taskToolRun) grantable(files []string) ([]string, error) {
	scope := run.scopeLocked()
	var wanted []string
	for _, file := range files {
		if !scope.covers(file) && !slices.Contains(wanted, file) {
			wanted = append(wanted, file)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	imported, err := run.importedFolders(scope.folders)
	if err != nil {
		return nil, err
	}
	for _, file := range wanted {
		if !imported[path.Dir(file)] {
			return nil, fmt.Errorf("%s is not in code this task's code imports (%s), so this fix may not change it. Leave it and finish with outcome needs_own_task, saying what it needs", file, describeImported(imported))
		}
	}
	return wanted, run.withinScopeLimits(wanted)
}

// scopeRequest cleans the requested files: code the run may write (the
// diff policy), not tests, which may change anywhere anyway.
func (run *taskToolRun) scopeRequest(paths []string) ([]string, error) {
	files := make([]string, 0, len(paths))
	for _, file := range paths {
		clean, _, err := run.readable(file)
		if err != nil {
			return nil, err
		}
		if err := worktreeWritable(run.policy, run.session, clean); err != nil {
			return nil, errors.New(strings.Replace(err.Error(), "proposal wrote", "you may not write", 1))
		}
		if !run.resolver.IsTestFile(clean) {
			files = append(files, clean)
		}
	}
	return files, nil
}

// importedFolders are the project's own folders that code in folders imports,
// from the project map of the task's checkout.
func (run *taskToolRun) importedFolders(folders []string) (map[string]bool, error) {
	index, err := run.s.manager.Index()
	if err != nil {
		return nil, fmt.Errorf("the project's imports are unknown, so the scope cannot widen: %w", err)
	}
	imported := map[string]bool{}
	for _, edge := range run.s.projectMap(index).Edges {
		if slices.Contains(folders, edge.From) && !slices.Contains(folders, edge.To) {
			imported[edge.To] = true
		}
	}
	return imported, nil
}

func describeImported(imported map[string]bool) string {
	if len(imported) == 0 {
		return "it imports none of the project's other folders"
	}
	return "it imports " + strings.Join(sortedKeys(imported), ", ")
}

// withinScopeLimits keeps a widened task small: at most two folders beyond
// its own, and at most eight code files with those it already changes.
func (run *taskToolRun) withinScopeLimits(wanted []string) error {
	grants := append(slices.Clone(run.session.ScopeGrants), run.grants...)
	folders, files := map[string]bool{}, map[string]bool{}
	for _, grant := range grants {
		folders[path.Dir(grant.Path)], files[grant.Path] = true, true
	}
	for _, target := range run.session.Targets {
		if !target.Test {
			files[target.Path] = true
		}
	}
	for _, file := range wanted {
		folders[path.Dir(file)], files[file] = true, true
	}
	switch {
	case len(folders) > maxScopeGrantFolders:
		return fmt.Errorf("a fix may reach at most %d folders beyond its own, and this would make %d (%s). Finish with outcome needs_own_task and say what the fix needs", maxScopeGrantFolders, len(folders), strings.Join(sortedKeys(folders), ", "))
	case len(files) > maxScopeFiles:
		return fmt.Errorf("a fix may change at most %d code files, and this would make %d. Finish with outcome needs_own_task and say what the fix needs", maxScopeFiles, len(files))
	}
	return nil
}

// fixingIssue is the finding of the issue step this run works on, if any.
func (run *taskToolRun) fixingIssue() string {
	for _, issue := range run.session.Issues {
		if issue.Status == IssueFixing {
			return issue.FindingID
		}
	}
	return ""
}

// withGrants adds a run's grants to the stored task, once each.
func withGrants(grants, added []ScopeGrant) []ScopeGrant {
	for _, grant := range added {
		if !slices.ContainsFunc(grants, func(existing ScopeGrant) bool { return existing.Path == grant.Path }) {
			grants = append(grants, grant)
		}
	}
	return grants
}

// withoutStepGrants takes away what a rolled-back issue step was granted.
func withoutStepGrants(grants []ScopeGrant, issue string) []ScopeGrant {
	return slices.DeleteFunc(slices.Clone(grants), func(grant ScopeGrant) bool { return issue != "" && grant.Issue == issue })
}

// grantsMade is what this run was granted.
func (run *taskToolRun) grantsMade() []ScopeGrant {
	run.mu.Lock()
	defer run.mu.Unlock()
	return slices.Clone(run.grants)
}

// waiting is the task this run found it must wait for, if any.
func (run *taskToolRun) waiting() *ChangeWorkflowWait {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.wait
}
