package app

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// fixScope is where a task that fixes linked issues may change code: the
// folders of the issues' files and of the files the task names (so a fix can
// update a caller or split a file beside it). Test files may change anywhere.
// A task not drafted from issues with a file has no scope (nil): it changes
// whatever its diff policy allows.
type fixScope []string

func issueFixScope(session *ChangeSession) fixScope {
	folders := map[string]bool{}
	for _, target := range session.FindingTargets {
		if target.Path != "" {
			folders[path.Dir(target.Path)] = true
		}
	}
	for _, issue := range session.Issues {
		folders[path.Dir(issue.Path)] = true // Unlinked ones too: the task still fixes that file.
	}
	if len(folders) == 0 {
		return nil
	}
	for _, target := range session.Targets {
		folders[path.Dir(target.Path)] = true
	}
	return fixScope(sortedKeys(folders))
}

// check refuses a change outside the scope and says what the agent should do
// instead: leave the file and report that the issue needs a task of its own.
func (scope fixScope) check(file string, isTest func(string) bool) error {
	if scope == nil || isTest(file) || slices.Contains(scope, path.Dir(file)) {
		return nil
	}
	return fmt.Errorf("%s is outside this fix's scope: a task that fixes issues changes code in %s and test files only. If the fix needs %s, leave it unchanged, call finish and say the issue needs a task of its own", file, scope.describe(), file)
}

// describe names the scope's folders for an agent or a person.
func (scope fixScope) describe() string {
	names := make([]string, 0, len(scope))
	for _, folder := range scope {
		if folder == "." {
			folder = "the project's top folder"
		}
		names = append(names, folder)
	}
	return strings.Join(names, ", ")
}
