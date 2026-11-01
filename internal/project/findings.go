package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
)

const (
	findingStorePath   = ".kasuri/findings.json"
	findingStoreSchema = "1"

	FindingSourceAI     = "ai"
	FindingSourceParser = "parser"
	// FindingSourceVet marks go vet failures of the retired Go scan; lint
	// reports them now.
	FindingSourceVet = "vet"
	// FindingSourceTest marks failing tests of a project's test commands.
	FindingSourceTest = "test"
	// FindingSourceLint marks diagnostics of a project's lint commands.
	FindingSourceLint = "lint"
	// FindingSourceSemgrep marks matches of the project's own semgrep rules.
	FindingSourceSemgrep = "semgrep"
	// FindingSourceRules marks code over the budgets AGENTS.md declares,
	// found by task checks and by the checks of each analysis run.
	FindingSourceRules = "rules"
	// FindingSourceSecurityRules marks matches of the deterministic security
	// rules; they are reported, not suggested.
	FindingSourceSecurityRules = "security_rules"

	// FindingCategoryMaintainability is code to refactor, such as a file over
	// its line budget or a lint diagnostic. Only rules and lint use it.
	FindingCategoryMaintainability FindingCategory = "maintainability"

	FindingConfidenceSuggested    = "suggested"
	FindingConfidenceToolReported = "tool_reported"

	FindingStatusOpen       = "open"
	FindingStatusInProgress = "in_progress"
	FindingStatusDismissed  = "dismissed"
	FindingStatusFixed      = "fixed"
	FindingStatusRetired    = "retired"

	FindingFreshnessFresh = "fresh"
	FindingFreshnessStale = "stale"
)

// FindingLocation identifies an optional project-relative location: a file,
// its lines and the function that holds them.
type FindingLocation struct {
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	// Symbol is the innermost function around StartLine, as the syntax layer
	// names it ("Store.add"); not part of the finding's identity.
	Symbol string `json:"symbol,omitempty"`
}

// UnifiedFinding keeps model suggestions structurally distinct from verified
// tool output while sharing one deterministic persistence and triage contract.
type UnifiedFinding struct {
	ID                  string              `json:"id"`
	Category            FindingCategory     `json:"category,omitempty"`
	Source              string              `json:"source"`
	Confidence          string              `json:"confidence"`
	Severity            string              `json:"severity"`
	Title               string              `json:"title"`
	Message             string              `json:"message"`
	Rule                string              `json:"rule,omitempty"`
	ProjectID           string              `json:"project_id"`
	ProjectRevision     string              `json:"project_revision"`
	FileHash            string              `json:"file_hash,omitempty"`
	Location            FindingLocation     `json:"location"`
	Evidence            string              `json:"evidence,omitempty"`
	Status              string              `json:"status"`
	Freshness           string              `json:"freshness"`
	DetectedAt          time.Time           `json:"detected_at"`
	OriginatingAnalysis string              `json:"originating_analysis,omitempty"`
	TaskSpec            *BugTaskSpec        `json:"task_spec,omitempty"`
	EngineeringInsight  *EngineeringInsight `json:"engineering_insight,omitempty"`
}

// FindingInput is the active deterministic state used to assess freshness.
type FindingInput struct {
	ProjectID       string
	ProjectRevision string
	FileHashes      map[string]string
}

func normalizeFinding(finding UnifiedFinding, input FindingInput) UnifiedFinding {
	finding.ProjectID = input.ProjectID
	finding.ProjectRevision = input.ProjectRevision
	finding.Title = sanitizeFindingText(finding.Title, maxProjectAnalysisItemBytes)
	finding.Message = sanitizeFindingText(finding.Message, maxProjectAnalysisItemBytes)
	finding.Evidence = sanitizeFindingText(finding.Evidence, maxProjectAnalysisBytes)
	finding.TaskSpec = SanitizeBugTaskSpec(finding.TaskSpec)
	finding.Location.Path = filepath.ToSlash(strings.TrimSpace(finding.Location.Path))
	finding.Location.Symbol = sanitizeFindingText(finding.Location.Symbol, maxFindingSymbolBytes)
	if finding.Status == "" {
		finding.Status = FindingStatusOpen
	}
	finding.Freshness = FindingFreshnessFresh
	if finding.DetectedAt.IsZero() {
		finding.DetectedAt = time.Now().UTC()
	} else {
		finding.DetectedAt = finding.DetectedAt.UTC()
	}
	finding.ID = FindingID(finding)
	return finding
}

// FindingID excludes revision, hash and category. Adding or correcting a category
// preserves triage for the same evidence; freshness is checked independently.
func FindingID(finding UnifiedFinding) string {
	values := []string{finding.Source, finding.Rule, finding.Location.Path, fmt.Sprint(finding.Location.StartLine), fmt.Sprint(finding.Location.EndLine), finding.Message}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return "finding:" + hex.EncodeToString(sum[:])
}

func findingMatchesInput(finding UnifiedFinding, input FindingInput) bool {
	if finding.ProjectID != input.ProjectID || finding.ProjectRevision != input.ProjectRevision {
		return false
	}
	if finding.FileHash == "" || finding.Location.Path == "" || input.FileHashes == nil {
		return true
	}
	return input.FileHashes[finding.Location.Path] == finding.FileHash
}

func validateFinding(finding UnifiedFinding) error {
	maintainability := (finding.Source == FindingSourceRules || finding.Source == FindingSourceLint) && finding.Category == FindingCategoryMaintainability
	if finding.Category != FindingCategoryUnclassified && !finding.Category.Valid() && !maintainability {
		return fmt.Errorf("finding category is invalid")
	}
	if !validFindingIdentity(finding) {
		return fmt.Errorf("finding is invalid")
	}
	if !validFindingContent(finding) {
		return fmt.Errorf("finding exceeds limits")
	}
	if !validPersistedBugTaskSpec(finding.TaskSpec) {
		return fmt.Errorf("finding task specification is invalid")
	}
	return nil
}

func validFindingIdentity(finding UnifiedFinding) bool {
	return finding.ID != "" && validFindingSource(finding.Source) && validFindingConfidence(finding.Source, finding.Confidence) &&
		validProjectAnalysisSeverity(finding.Severity) && finding.Title != "" && finding.Message != "" &&
		finding.ProjectID != "" && finding.ProjectRevision != "" && validFindingStatus(finding.Status) &&
		finding.Freshness == FindingFreshnessFresh && !finding.DetectedAt.IsZero()
}

func validFindingContent(finding UnifiedFinding) bool {
	return len(finding.Title) <= maxProjectAnalysisItemBytes && len(finding.Message) <= maxProjectAnalysisItemBytes &&
		len(finding.Evidence) <= maxProjectAnalysisBytes && finding.Location.StartLine >= 0 &&
		finding.Location.EndLine >= finding.Location.StartLine && len(finding.Location.Symbol) <= maxFindingSymbolBytes
}

// maxFindingSymbolBytes bounds a location's function name.
const maxFindingSymbolBytes = 256

func validPersistedBugTaskSpec(spec *BugTaskSpec) bool {
	if spec == nil {
		return true
	}
	if !validPersistedBugTaskTarget(spec) || !validPersistedBugTaskItems(spec) {
		return false
	}
	return validPersistedGoTestCandidate(spec.GoTestCandidate) && validPersistedTestCandidate(spec.TestCandidate)
}

func validPersistedBugTaskTarget(spec *BugTaskSpec) bool {
	return spec.SchemaVersion == BugTaskSpecSchemaVersion && spec.TargetPath != "" &&
		len(spec.AcceptanceCriteria) > 0 &&
		len(spec.AcceptanceCriteria) <= MaxBugTaskItems && len(spec.NonGoals) <= MaxBugTaskItems
}

func validPersistedBugTaskItems(spec *BugTaskSpec) bool {
	for _, item := range append(append([]string(nil), spec.AcceptanceCriteria...), spec.NonGoals...) {
		if item == "" || len(item) > MaxBugTaskItemBytes {
			return false
		}
	}
	return true
}

func validPersistedTestCandidate(candidate *TestCandidateSpec) bool {
	return candidate == nil || (candidate.Path != "" && len(candidate.Path) <= 1024 && candidate.Name != "" && candidate.Content != "" &&
		len(candidate.Name) <= MaxBugTaskItemBytes && len(candidate.Content) <= MaxBugTaskCandidateBytes)
}

func validPersistedGoTestCandidate(candidate *GoTestCandidateSpec) bool {
	return candidate == nil || (candidate.Name != "" && candidate.Content != "" &&
		len(candidate.Name) <= MaxBugTaskItemBytes && len(candidate.Content) <= MaxBugTaskCandidateBytes)
}

func validFindingSource(value string) bool {
	switch value {
	case FindingSourceAI, FindingSourceParser, FindingSourceVet, FindingSourceTest, FindingSourceLint, FindingSourceSemgrep, FindingSourceRules, FindingSourceSecurityRules:
		return true
	}
	return false
}
func validFindingConfidence(source, confidence string) bool {
	if source == FindingSourceAI {
		return confidence == FindingConfidenceSuggested
	}
	return confidence == FindingConfidenceToolReported
}
func validFindingStatus(value string) bool {
	return value == FindingStatusOpen || value == FindingStatusInProgress || value == FindingStatusDismissed || value == FindingStatusFixed || value == FindingStatusRetired
}
func sanitizeFindingText(value string, limit int) string {
	return insight.SanitizeFindingText(value, limit)
}
func cloneFindings(source []UnifiedFinding) []UnifiedFinding {
	result := append([]UnifiedFinding(nil), source...)
	for index := range result {
		result[index].TaskSpec = insight.CloneBugTaskSpec(source[index].TaskSpec)
		result[index].EngineeringInsight = CloneEngineeringInsight(source[index].EngineeringInsight)
	}
	return result
}
