package archgate

import (
	"path/filepath"

	"github.com/nanaki-93/kasuri/v2/internal/project/syntax"
)

// SyntaxCheck parses a proposed non-Go file. It advises rather than blocks:
// a grammar that does not know a newer syntax must not fail a valid change,
// and the project's own build and tests remain the authority. A file that
// did not parse before the task is reported as such.
func SyntaxCheck(targets []ChangeTarget, edit ChangeEdit) CheckResult {
	check := CheckResult{Name: "syntax: " + filepath.ToSlash(edit.Path), Required: false, State: CheckPassed}
	proposed := syntax.Analyze(edit.Path, []byte(edit.Content))
	switch {
	case proposed.Grammar == "":
		check.State, check.Output = CheckSkipped, "No parser for this file type."
	case proposed.SyntaxError == "":
		check.Output = "Parses as " + proposed.Grammar + "."
	default:
		base, hasBase := targetContent(targets, edit.Path)
		if hasBase && syntax.Analyze(edit.Path, []byte(base)).SyntaxError != "" {
			check.Output = "Did not parse before this task either: " + proposed.SyntaxError
			return check
		}
		check.State = CheckFailed
		check.Output = proposed.SyntaxError + ". Fix the syntax; if the code is valid, Kasuri's " + proposed.Grammar + " parser may not know this syntax yet."
	}
	return check
}
