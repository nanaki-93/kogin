package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project/insight"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

const (
	PerformancePromptVersion  = "performance-file-v8"
	PerformanceMaxSourceBytes = 64 * 1024
	maxPerformanceOutputBytes = 64 * 1024
	maxPerformanceFindings    = 5
)

// PerformanceFinding is a source-based opportunity, never a measured result.
type PerformanceFinding struct {
	ID                 string              `json:"id"`
	Category           string              `json:"category"`
	PotentialImpact    string              `json:"potential_impact"`
	Confidence         string              `json:"confidence"`
	Title              string              `json:"title"`
	ObservedPattern    string              `json:"observed_pattern"`
	WorkloadConditions string              `json:"workload_conditions"`
	Recommendation     string              `json:"recommendation"`
	Tradeoff           string              `json:"tradeoff"`
	VerificationPlan   string              `json:"verification_plan"`
	StartLine          int                 `json:"start_line"`
	EndLine            int                 `json:"end_line"`
	EngineeringInsight *EngineeringInsight `json:"engineering_insight,omitempty"`
}

type PerformanceFileReport struct {
	SchemaVersion           string               `json:"schema_version"`
	ProjectID               string               `json:"project_id"`
	ProjectRevision         string               `json:"project_revision"`
	Path                    string               `json:"path"`
	ContentHash             string               `json:"content_hash"`
	Status                  string               `json:"status"`
	Findings                []PerformanceFinding `json:"findings"`
	Warning                 string               `json:"warning,omitempty"`
	Model                   string               `json:"model"`
	ConfiguredModel         string               `json:"configured_model,omitempty"`
	Profile                 string               `json:"profile"`
	Scope                   string               `json:"scope"`
	ProviderOrigin          string               `json:"provider_origin,omitempty"`
	ReasoningEffort         string               `json:"reasoning_effort,omitempty"`
	PromptVersion           string               `json:"prompt_version"`
	ContextPolicyVersion    string               `json:"context_policy_version"`
	InstructionsFingerprint string               `json:"instructions_fingerprint,omitempty"`
	GeneratedAt             time.Time            `json:"generated_at"`
}

type performanceWire struct {
	Findings []performanceFindingWire `json:"findings"`
}
type performanceFindingWire struct {
	Category           string          `json:"category"`
	PotentialImpact    string          `json:"potential_impact"`
	Confidence         string          `json:"confidence"`
	Title              string          `json:"title"`
	ObservedPattern    string          `json:"observed_pattern"`
	WorkloadConditions string          `json:"workload_conditions"`
	Recommendation     string          `json:"recommendation"`
	Tradeoff           string          `json:"tradeoff"`
	VerificationPlan   string          `json:"verification_plan"`
	StartLine          int             `json:"start_line"`
	EndLine            int             `json:"end_line"`
	Insight            json.RawMessage `json:"engineering_insight"`
}

// ParsePerformanceFindings rejects malformed parent output but omits malformed optional insights.
func ParsePerformanceFindings(output, path, source string) ([]PerformanceFinding, string, error) {
	if len(output) > maxPerformanceOutputBytes {
		return nil, "", PerformanceOutputTooLarge
	}
	var wire performanceWire
	decoder := json.NewDecoder(strings.NewReader(insight.NormalizeEmptyFindingsOutput(output)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, "", PerformanceInvalidJSON
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, "", PerformanceInvalidJSON
	}
	if wire.Findings == nil || len(wire.Findings) > maxPerformanceFindings {
		return nil, "", PerformanceInvalidShape
	}
	lines := countLines([]byte(source))
	valid := make([]PerformanceFinding, 0, len(wire.Findings))
	dropped := performanceFindingValidationFailures{}
	for _, item := range wire.Findings {
		finding := PerformanceFinding{Category: strings.ToLower(strings.TrimSpace(item.Category)), PotentialImpact: strings.ToLower(strings.TrimSpace(item.PotentialImpact)), Confidence: strings.ToLower(strings.TrimSpace(item.Confidence)), Title: strings.TrimSpace(item.Title), ObservedPattern: strings.TrimSpace(item.ObservedPattern), WorkloadConditions: strings.TrimSpace(item.WorkloadConditions), Recommendation: strings.TrimSpace(item.Recommendation), Tradeoff: strings.TrimSpace(item.Tradeoff), VerificationPlan: strings.TrimSpace(item.VerificationPlan), StartLine: item.StartLine, EndLine: item.EndLine}
		if failure := invalidPerformanceFinding(finding, lines); failure != performanceFindingValid {
			dropped.add(failure)
			continue
		}
		finding.EngineeringInsight, _ = ParseOptionalEngineeringInsight(item.Insight)
		sum := sha256.Sum256([]byte(path + fmt.Sprintf(":%d:%d:%s:%s", finding.StartLine, finding.EndLine, finding.Category, finding.Title)))
		finding.ID = "performance:" + hex.EncodeToString(sum[:8])
		valid = append(valid, finding)
	}
	if len(wire.Findings) > 0 && len(valid) == 0 {
		return nil, "", dropped.failure()
	}
	warning := ""
	if dropped.any() {
		warning = dropped.warning()
	}
	return valid, warning, nil
}

type performanceFindingValidationFailure uint8

const (
	performanceFindingValid performanceFindingValidationFailure = iota
	performanceFindingInvalidFields
	performanceFindingInvalidEnums
	performanceFindingInvalidAnchor
)

type performanceFindingValidationFailures struct {
	fields bool
	enums  bool
	anchor bool
}

func invalidPerformanceFinding(f PerformanceFinding, lines int) performanceFindingValidationFailure {
	if !validPerformanceFindingFields(f) {
		return performanceFindingInvalidFields
	}
	if !validPerformanceFindingEnums(f) {
		return performanceFindingInvalidEnums
	}
	if !validPerformanceFindingAnchor(f, lines) {
		return performanceFindingInvalidAnchor
	}
	return performanceFindingValid
}

func (f *performanceFindingValidationFailures) add(failure performanceFindingValidationFailure) {
	switch failure {
	case performanceFindingInvalidFields:
		f.fields = true
	case performanceFindingInvalidEnums:
		f.enums = true
	case performanceFindingInvalidAnchor:
		f.anchor = true
	}
}

func (f performanceFindingValidationFailures) any() bool {
	return f.fields || f.enums || f.anchor
}

func (f performanceFindingValidationFailures) warning() string {
	reasons := make([]string, 0, 3)
	if f.fields {
		reasons = append(reasons, "required fields")
	}
	if f.enums {
		reasons = append(reasons, "category, potential impact, or confidence values")
	}
	if f.anchor {
		reasons = append(reasons, "source anchors")
	}
	if len(reasons) == 1 {
		if f.anchor {
			return "Some model opportunities were omitted because their source anchors were invalid."
		}
		return "Some model opportunities were omitted because " + reasons[0] + " were invalid."
	}
	if len(reasons) == 2 {
		return "Some model opportunities were omitted because " + reasons[0] + " or " + reasons[1] + " were invalid."
	}
	return "Some model opportunities were omitted because " + strings.Join(reasons[:len(reasons)-1], ", ") + ", or " + reasons[len(reasons)-1] + " were invalid."
}

func validPerformanceFindingFields(f PerformanceFinding) bool {
	return f.Title != "" && f.ObservedPattern != "" && f.WorkloadConditions != "" && f.Recommendation != "" && f.Tradeoff != "" && f.VerificationPlan != ""
}

func validPerformanceFindingEnums(f PerformanceFinding) bool {
	return oneOf(f.Category, "cpu", "memory", "io", "concurrency", "caching", "ui") && oneOf(f.PotentialImpact, "high", "medium", "low", "unknown") && oneOf(f.Confidence, "high", "medium", "low")
}

func validPerformanceFindingAnchor(f PerformanceFinding, lines int) bool {
	return f.StartLine >= 1 && f.EndLine >= f.StartLine && f.EndLine <= lines
}
func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

var performanceReportLocks sync.Map

func performanceReportLock(root string) (*sync.Mutex, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	lock, _ := performanceReportLocks.LoadOrStore(canonical, &sync.Mutex{})
	return lock.(*sync.Mutex), nil
}

func StorePerformanceFileReport(root string, report PerformanceFileReport) error {
	if report.ProjectID == "" || report.Path == "" || report.ContentHash == "" || report.PromptVersion == "" || report.GeneratedAt.IsZero() {
		return fmt.Errorf("performance report is incomplete")
	}
	mu, err := performanceReportLock(root)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	cachePath, err := resolvePerformanceCachePath(root, report.Path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFile(cachePath, data, 0600)
}

func ClearPerformanceFileReports(root string) error {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return err
	}
	mu, err := performanceReportLock(canonical)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	directory, err := storage.MetadataPath(canonical, ".kogin", "performance", "files")
	if err != nil {
		return fmt.Errorf("clear performance cache: %w", err)
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("clear performance cache: %w", err)
	}
	return nil
}

// LoadPerformanceFileReport reads only sanitized metadata. Changed identity,
// prompt version, context policy, or an unavailable source file is reported as
// stale instead of being presented as a current review.
func LoadPerformanceFileReport(root, path, contentHash string, policy *ContextPolicy) (*PerformanceFileReport, error) {
	if policy == nil {
		return nil, fmt.Errorf("performance report policy is required")
	}
	mu, err := performanceReportLock(root)
	if err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	cachePath, err := resolvePerformanceCachePath(root, path)
	if err != nil {
		return nil, fmt.Errorf("read performance cache: %w", err)
	}
	data, err := os.ReadFile(cachePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read performance cache: %w", err)
	}
	report, valid := parsePerformanceFileReport(data, path)
	if !valid {
		if recoverErr := storage.RecoverCorrupt(cachePath); recoverErr != nil {
			return nil, recoverErr
		}
		return nil, nil
	}
	if performanceReportIsStale(report, root, path, contentHash, policy) {
		report.Status = "stale"
	}
	if err := validateInstructionsWithPolicy(root, path, report.InstructionsFingerprint, policy); err != nil {
		if !errors.Is(err, ErrRevisionConflict) {
			return nil, err
		}
		report.Status = "stale"
	}
	return clonePerformanceFileReport(&report), nil
}

func resolvePerformanceCachePath(root, path string) (string, error) {
	key := sha256.Sum256([]byte(path))
	return storage.MetadataPath(root, ".kogin", "performance", "files", hex.EncodeToString(key[:])+".json")
}

func parsePerformanceFileReport(data []byte, path string) (PerformanceFileReport, bool) {
	var report PerformanceFileReport
	if err := json.Unmarshal(data, &report); err != nil || report.SchemaVersion != "1" || report.Path != path || report.ContentHash == "" {
		return PerformanceFileReport{}, false
	}
	return report, true
}

func performanceReportIsStale(report PerformanceFileReport, root, path, contentHash string, policy *ContextPolicy) bool {
	if report.ContentHash != contentHash || report.PromptVersion != PerformancePromptVersion || report.ContextPolicyVersion != policy.Version() || !policy.Decide(path).Include {
		return true
	}
	current, err := GetFileInfo(root, path)
	return err != nil || current.SizeBytes > PerformanceMaxSourceBytes || current.ContentHash != contentHash
}

func clonePerformanceFileReport(source *PerformanceFileReport) *PerformanceFileReport {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Findings = append([]PerformanceFinding(nil), source.Findings...)
	for index := range copy.Findings {
		copy.Findings[index].EngineeringInsight = CloneEngineeringInsight(source.Findings[index].EngineeringInsight)
	}
	return &copy
}

// PerformanceReviewFailure is safe to retain in source-free run metadata. It
// never contains provider output, prose, paths, or decoder-supplied field names.
type PerformanceReviewFailure string

const (
	PerformanceOutputTooLarge  PerformanceReviewFailure = "The Performance response exceeded the output size limit."
	PerformanceInvalidJSON     PerformanceReviewFailure = "The Performance response was not valid findings JSON."
	PerformanceInvalidShape    PerformanceReviewFailure = "The Performance response must contain a findings array with at most five entries."
	PerformanceInvalidAnchors  PerformanceReviewFailure = "Performance findings referenced invalid lines."
	PerformanceInvalidFields   PerformanceReviewFailure = "Performance findings were missing required explanations."
	PerformanceInvalidValues   PerformanceReviewFailure = "Performance findings used unsupported category, impact or confidence values."
	PerformanceInvalidFindings PerformanceReviewFailure = "No Performance findings passed the required field and source anchor checks."
)

func (failure PerformanceReviewFailure) Error() string { return string(failure) }

func (failure PerformanceReviewFailure) Valid() bool {
	switch failure {
	case PerformanceOutputTooLarge, PerformanceInvalidJSON, PerformanceInvalidShape, PerformanceInvalidAnchors, PerformanceInvalidFields, PerformanceInvalidValues, PerformanceInvalidFindings:
		return true
	}
	return false
}

func (f performanceFindingValidationFailures) failure() PerformanceReviewFailure {
	switch {
	case f.anchor && !f.fields && !f.enums:
		return PerformanceInvalidAnchors
	case f.fields && !f.anchor && !f.enums:
		return PerformanceInvalidFields
	case f.enums && !f.anchor && !f.fields:
		return PerformanceInvalidValues
	default:
		return PerformanceInvalidFindings
	}
}
