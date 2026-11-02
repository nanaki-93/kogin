package app

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// fixScope is where a task that fixes linked issues may change code: the
// folders of the issues' files and of the files the task names (so a fix can
// update a caller or split a file beside it), and the files outside them an
// agent was granted because the fix needs them (widen_scope). Test files may
// change anywhere. A task not drafted from issues with a file has no scope
// (nil): it changes whatever its diff policy allows.
type fixScope struct {
	folders []string
	files   []string
}

// ScopeGrant is a file outside a fix's folders that an agent may change
// because the fix needs it: code the task's own code imports.
type ScopeGrant struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// Issue is the finding of the issue step that asked, if any: rolling
	// that step back takes the grant away.
	Issue string `json:"issue,omitempty"`
}

func issueFixScope(session *ChangeSession) *fixScope {
	folders := issueFolders(session)
	if len(folders) == 0 {
		return nil
	}
	for _, target := range session.Targets {
		folders[path.Dir(target.Path)] = true
	}
	scope := &fixScope{folders: sortedKeys(folders)}
	for _, grant := range session.ScopeGrants {
		if !scope.covers(grant.Path) {
			scope.files = append(scope.files, grant.Path)
		}
	}
	return scope
}

// issueFolders are the folders of the issues a task fixes, linked or not.
func issueFolders(session *ChangeSession) map[string]bool {
	folders := map[string]bool{}
	for _, target := range session.FindingTargets {
		if target.Path != "" {
			folders[path.Dir(target.Path)] = true
		}
	}
	for _, issue := range session.Issues {
		folders[path.Dir(issue.Path)] = true // Unlinked ones too: the task still fixes that file.
	}
	return folders
}

// covers reports whether the scope lets code in file change.
func (scope *fixScope) covers(file string) bool {
	return scope == nil || slices.Contains(scope.folders, path.Dir(file)) || slices.Contains(scope.files, file)
}

// check refuses a change outside the scope.
func (scope *fixScope) check(file string, isTest func(string) bool) error {
	if scope.covers(file) || isTest(file) {
		return nil
	}
	return fmt.Errorf("%s is outside this fix's scope: a task that fixes issues changes code in %s and test files only", file, scope.describe())
}

// describe names the scope's folders and granted files for an agent or a person.
func (scope *fixScope) describe() string {
	names := make([]string, 0, len(scope.folders)+len(scope.files))
	for _, folder := range scope.folders {
		if folder == "." {
			folder = "the project's top folder"
		}
		names = append(names, folder)
	}
	return strings.Join(append(names, scope.files...), ", ")
}
