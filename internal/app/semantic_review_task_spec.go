package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func parseOptionalBugTaskSpec(raw json.RawMessage, target project.IndexFile, source string) *project.BugTaskSpec {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var spec project.BugTaskSpec
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil
	}
	validated, err := validateBugTaskSpec(&spec, target, source)
	if err != nil {
		return nil
	}
	return validated
}

func validateBugTaskSpec(spec *project.BugTaskSpec, target project.IndexFile, source string) (*project.BugTaskSpec, error) {
	if spec == nil {
		return nil, nil
	}
	if !validBugTaskTarget(spec, target.Path) {
		return nil, fmt.Errorf("semantic analysis contains an invalid bug task target")
	}
	if err := validateBugTaskItems(spec.AcceptanceCriteria, true); err != nil {
		return nil, err
	}
	if err := validateBugTaskItems(spec.NonGoals, false); err != nil {
		return nil, err
	}
	validated := project.SanitizeBugTaskSpec(spec)
	validated.TargetPath = target.Path
	// The legacy Go-only candidate is not asked for any more.
	validated.GoTestCandidate = nil
	validated.TestCandidate = validateTestCandidate(validated.TestCandidate, target, source)
	return validated, nil
}

func validBugTaskTarget(spec *project.BugTaskSpec, targetPath string) bool {
	return spec.SchemaVersion == project.BugTaskSpecSchemaVersion && len(spec.TargetPath) <= 1024 &&
		spec.TargetPath == targetPath
}

func validateBugTaskItems(items []string, required bool) error {
	if len(items) > project.MaxBugTaskItems || required && len(items) == 0 {
		return fmt.Errorf("semantic analysis bug task items exceed limits")
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" || len(item) > project.MaxBugTaskItemBytes {
			return fmt.Errorf("semantic analysis bug task item is invalid")
		}
	}
	return nil
}
