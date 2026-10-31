package insight

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/textutil"
)

const (
	BugTaskSpecSchemaVersion = "2"
	MaxBugTaskItems          = 8
	MaxBugTaskItemBytes      = 512
	MaxBugTaskCandidateBytes = 16 * 1024
)

var findingSecret = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|password|secret|credential|authorization)\b\s*[:=]\s*[^\s,;]+`)

// BugTaskSpec is a bounded model proposal for one file edit. Its
// target fields are always validated against the deterministic project index.
type BugTaskSpec struct {
	SchemaVersion      string             `json:"schema_version"`
	TargetPath         string             `json:"target_path"`
	AcceptanceCriteria []string           `json:"acceptance_criteria"`
	NonGoals           []string           `json:"non_goals"`
	TestCandidate      *TestCandidateSpec `json:"test_candidate,omitempty"`
	// GoTestCandidate is the Go-only candidate of reports saved before
	// test_candidate; it is read and never used.
	GoTestCandidate *GoTestCandidateSpec `json:"go_test_candidate,omitempty"`
}

// TestCandidateSpec is a regression test the model proposes as a new test
// file, in the target's language and the project's test conventions: it
// should fail on the code as it is and pass once the criteria hold. It is
// advisory: a task draft shows it, and only a task's agents write it.
type TestCandidateSpec struct {
	// Path is the new test file, project-relative.
	Path string `json:"path"`
	// Name is the test's name as content spells it.
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GoTestCandidateSpec is the legacy Go-only candidate.
type GoTestCandidateSpec struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// SanitizeFindingText redacts secret-bearing key-value tokens and bounds byte length.
func SanitizeFindingText(value string, limit int) string {
	value = strings.TrimSpace(findingSecret.ReplaceAllString(value, "[redacted]"))
	if len(value) <= limit {
		return value
	}
	return textutil.Truncate(value, limit-len("[truncated]")) + "[truncated]"
}

// SanitizeBugTaskSpec applies the same secret redaction and text normalization
// used by persisted findings while preserving no shared mutable slices.
func SanitizeBugTaskSpec(spec *BugTaskSpec) *BugTaskSpec {
	if spec == nil {
		return nil
	}
	result := &BugTaskSpec{
		SchemaVersion:      strings.TrimSpace(spec.SchemaVersion),
		TargetPath:         strings.TrimSpace(spec.TargetPath),
		AcceptanceCriteria: sanitizeBugTaskItems(spec.AcceptanceCriteria),
		NonGoals:           sanitizeBugTaskItems(spec.NonGoals),
	}
	if spec.GoTestCandidate != nil {
		result.GoTestCandidate = &GoTestCandidateSpec{
			Name:    SanitizeFindingText(spec.GoTestCandidate.Name, MaxBugTaskItemBytes),
			Content: SanitizeFindingText(spec.GoTestCandidate.Content, MaxBugTaskCandidateBytes),
		}
	}
	if spec.TestCandidate != nil {
		result.TestCandidate = &TestCandidateSpec{
			Path:    strings.TrimSpace(spec.TestCandidate.Path),
			Name:    SanitizeFindingText(spec.TestCandidate.Name, MaxBugTaskItemBytes),
			Content: SanitizeFindingText(spec.TestCandidate.Content, MaxBugTaskCandidateBytes),
		}
	}
	return result
}

func sanitizeBugTaskItems(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, SanitizeFindingText(item, MaxBugTaskItemBytes))
	}
	return result
}

// CloneBugTaskSpec deep-copies a BugTaskSpec and its nested slices/candidate.
func CloneBugTaskSpec(spec *BugTaskSpec) *BugTaskSpec {
	if spec == nil {
		return nil
	}
	copy := *spec
	copy.AcceptanceCriteria = append([]string(nil), spec.AcceptanceCriteria...)
	copy.NonGoals = append([]string(nil), spec.NonGoals...)
	if spec.GoTestCandidate != nil {
		candidate := *spec.GoTestCandidate
		copy.GoTestCandidate = &candidate
	}
	if spec.TestCandidate != nil {
		candidate := *spec.TestCandidate
		copy.TestCandidate = &candidate
	}
	return &copy
}

// NormalizeEmptyFindingsOutput normalizes an empty JSON array or blank model output
// into `{"findings":[]}` while leaving non-empty or malformed payloads untouched.
func NormalizeEmptyFindingsOutput(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return `{"findings":[]}`
	}
	if strings.HasPrefix(trimmed, "[") {
		var findings []json.RawMessage
		if json.Unmarshal([]byte(trimmed), &findings) == nil && len(findings) == 0 {
			return `{"findings":[]}`
		}
	}
	return output
}
