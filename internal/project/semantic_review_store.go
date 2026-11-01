package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project/insight"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

const (
	fileAnalysisSchemaVersion = "1"
	fileAnalysisRelativeDir   = ".kasuri/file-analysis"

	AnalysisStatusFresh   = "fresh"
	AnalysisStatusMissing = "missing"
	AnalysisStatusStale   = "stale"
	AnalysisStatusFailed  = "failed"
	AnalysisStatusRunning = "running"
)

// FindingCategory routes semantic advice independently of its severity or producer.
// The empty value is retained for historical reports; it is never a fresh category.
type FindingCategory string

const (
	FindingCategoryUnclassified FindingCategory = ""
	FindingCategoryBugs         FindingCategory = "bugs"
	FindingCategoryPerformance  FindingCategory = "performance"
	FindingCategorySecurity     FindingCategory = "security"
)

func (category FindingCategory) Valid() bool {
	return category == FindingCategoryBugs || category == FindingCategoryPerformance || category == FindingCategorySecurity
}

// Finding is model-derived advice, not a deterministic project fact.
type Finding struct {
	Category FindingCategory `json:"category,omitempty"`
	Severity string          `json:"severity"`
	Title    string          `json:"title,omitempty"`
	Summary  string          `json:"summary"`
	// StartLine and EndLine are where the risk is, found from the line the
	// model copied; zero when it copied none that is in the file.
	StartLine          int                 `json:"start_line,omitempty"`
	EndLine            int                 `json:"end_line,omitempty"`
	TaskSpec           *BugTaskSpec        `json:"task_spec,omitempty"`
	EngineeringInsight *EngineeringInsight `json:"engineering_insight,omitempty"`
}

// Suggestion is an optional, user-reviewed atomic improvement proposal.
type Suggestion struct {
	Title              string              `json:"title"`
	Summary            string              `json:"summary"`
	Action             string              `json:"action,omitempty"`
	EngineeringInsight *EngineeringInsight `json:"engineering_insight,omitempty"`
}

// FileAnalysis is a source-free, versioned semantic summary for one file.
// Deterministic imports remain in ProjectIndex; this record only
// stores model interpretation and its validity inputs.
type FileAnalysis struct {
	SchemaVersion           string              `json:"schema_version"`
	ProjectID               string              `json:"project_id"`
	ProjectRevision         string              `json:"project_revision"`
	Path                    string              `json:"path"`
	ContentHash             string              `json:"content_hash"`
	Language                string              `json:"language"`
	Purpose                 string              `json:"purpose,omitempty"`
	Responsibilities        []string            `json:"responsibilities,omitempty"`
	Imports                 []string            `json:"imports,omitempty"`
	Dependencies            []string            `json:"dependencies,omitempty"`
	SideEffects             []string            `json:"side_effects,omitempty"`
	EngineeringInsight      *EngineeringInsight `json:"engineering_insight,omitempty"`
	Risks                   []Finding           `json:"risks,omitempty"`
	Suggestions             []Suggestion        `json:"suggestions,omitempty"`
	Status                  string              `json:"status"`
	Failure                 string              `json:"failure,omitempty"`
	Model                   string              `json:"model,omitempty"`
	ConfiguredModel         string              `json:"configured_model,omitempty"`
	Profile                 string              `json:"profile,omitempty"`
	Scope                   string              `json:"scope,omitempty"`
	ProviderOrigin          string              `json:"provider_origin,omitempty"`
	ReasoningEffort         string              `json:"reasoning_effort,omitempty"`
	PromptVersion           string              `json:"prompt_version"`
	ContextPolicyVersion    string              `json:"context_policy_version"`
	InstructionsFingerprint string              `json:"instructions_fingerprint,omitempty"`
	GeneratedAt             time.Time           `json:"generated_at"`
}

// FileAnalysisInput identifies the exact inputs that make an analysis valid.
type FileAnalysisInput struct {
	ProjectID            string
	ProjectRevision      string
	Path                 string
	ContentHash          string
	Language             string
	Model                string
	Profile              string
	Scope                string
	ProviderOrigin       string
	ReasoningEffort      string
	PromptVersion        string
	ContextPolicyVersion string
}

var fileAnalysisLocks sync.Map

// FileAnalysisCache persists semantic summaries under one project root.
type FileAnalysisCache struct {
	root   string
	mu     *sync.Mutex
	policy *ContextPolicy
}

func NewFileAnalysisCache(root string) (*FileAnalysisCache, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	lock, _ := fileAnalysisLocks.LoadOrStore(canonical, &sync.Mutex{})
	return &FileAnalysisCache{root: canonical, mu: lock.(*sync.Mutex)}, nil
}

func (c *FileAnalysisCache) WithPolicy(policy *ContextPolicy) *FileAnalysisCache {
	return &FileAnalysisCache{root: c.root, mu: c.mu, policy: policy}
}

func (c *FileAnalysisCache) contextPolicy() (*ContextPolicy, error) {
	if c.policy != nil {
		return c.policy, nil
	}
	return NewContextPolicy(c.root)
}

// Load returns a cache entry whose status is fresh, stale, missing, failed, or
// running. It checks applicable guidance and recovers corrupt cache files.
func (c *FileAnalysisCache) Load(input FileAnalysisInput) (*FileAnalysis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	policy, err := c.contextPolicy()
	if err != nil {
		return nil, err
	}
	if err := c.validateInputWithPolicy(input, policy); err != nil {
		return nil, err
	}
	path, err := storage.MetadataPath(c.root, fileAnalysisRelativeDir, analysisPathKey(normalizedAnalysisPath(input.Path))+".json")
	if err != nil {
		return nil, fmt.Errorf("read file analysis cache: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newMissingAnalysis(input), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read file analysis cache: %w", err)
	}
	var analysis FileAnalysis
	if err := json.Unmarshal(data, &analysis); err != nil || !validStoredAnalysis(analysis) {
		if err := storage.RecoverCorrupt(path); err != nil {
			return nil, err
		}
		return newMissingAnalysis(input), nil
	}
	if !analysisMatches(analysis, input) {
		analysis.Status = AnalysisStatusStale
	}
	if err := validateInstructionsWithPolicy(c.root, input.Path, analysis.InstructionsFingerprint, policy); err != nil {
		if !errors.Is(err, ErrRevisionConflict) {
			return nil, err
		}
		analysis.Status = AnalysisStatusStale
	}
	return cloneFileAnalysis(&analysis), nil
}

// normalizeStoredRisk checks a risk about to be stored and sanitizes its
// task specification.
func normalizeStoredRisk(risk *Finding) error {
	if risk.Category != FindingCategoryUnclassified && !risk.Category.Valid() {
		return fmt.Errorf("file analysis risk category is invalid")
	}
	if risk.StartLine < 0 || risk.EndLine < risk.StartLine || risk.StartLine == 0 && risk.EndLine != 0 {
		return fmt.Errorf("file analysis risk lines are invalid")
	}
	risk.TaskSpec = SanitizeBugTaskSpec(risk.TaskSpec)
	risk.EngineeringInsight = CloneEngineeringInsight(risk.EngineeringInsight)
	if !validPersistedBugTaskSpec(risk.TaskSpec) {
		return fmt.Errorf("file analysis task specification is invalid")
	}
	return nil
}

// Store atomically persists a source-free analysis whose cache key is derived
// from its normalized project-relative path.
func (c *FileAnalysisCache) Store(analysis FileAnalysis) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	analysis = *cloneFileAnalysis(&analysis)
	for index := range analysis.Risks {
		if err := normalizeStoredRisk(&analysis.Risks[index]); err != nil {
			return err
		}
	}
	input := inputFromAnalysis(analysis)
	policy, err := c.contextPolicy()
	if err != nil {
		return err
	}
	if err := c.validateInputWithPolicy(input, policy); err != nil {
		return err
	}
	if !validAnalysisStatus(analysis.Status) || analysis.Status == AnalysisStatusMissing || analysis.Status == AnalysisStatusStale {
		return fmt.Errorf("cannot persist file analysis status %q", analysis.Status)
	}
	analysis.SchemaVersion = fileAnalysisSchemaVersion
	analysis.Path = normalizedAnalysisPath(analysis.Path)
	analysis.GeneratedAt = analysis.GeneratedAt.UTC()
	if analysis.GeneratedAt.IsZero() {
		analysis.GeneratedAt = time.Now().UTC()
	}
	path, err := storage.MetadataPath(c.root, fileAnalysisRelativeDir, analysisPathKey(analysis.Path)+".json")
	if err != nil {
		return fmt.Errorf("store file analysis cache: %w", err)
	}
	data, err := json.MarshalIndent(analysis, "", "  ")
	if err != nil {
		return fmt.Errorf("encode file analysis cache: %w", err)
	}
	if err := storage.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("store file analysis cache: %w", err)
	}
	return nil
}

// Summary returns a file's stored summary when it was written for the file's
// current content, whatever model wrote it; nil when there is none.
func (c *FileAnalysisCache) Summary(file, contentHash string) (*FileAnalysis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path, err := storage.MetadataPath(c.root, fileAnalysisRelativeDir, analysisPathKey(normalizedAnalysisPath(file))+".json")
	if err != nil {
		return nil, fmt.Errorf("read file analysis cache: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read file analysis cache: %w", err)
	}
	var analysis FileAnalysis
	if json.Unmarshal(data, &analysis) != nil || !validStoredAnalysis(analysis) || analysis.Status != AnalysisStatusFresh ||
		analysis.ContentHash != contentHash || strings.TrimSpace(analysis.Purpose) == "" {
		return nil, nil
	}
	return cloneFileAnalysis(&analysis), nil
}

// Clear removes all cached file analysis reports for the project root.
func (c *FileAnalysisCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	directory, err := storage.MetadataPath(c.root, fileAnalysisRelativeDir)
	if err != nil {
		return fmt.Errorf("clear file analysis cache: %w", err)
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("clear file analysis cache: %w", err)
	}
	return nil
}

func (c *FileAnalysisCache) validateInputWithPolicy(input FileAnalysisInput, policy *ContextPolicy) error {
	decision := policy.Decide(input.Path)
	if decision.Reason == "unsafe path" || !decision.Include {
		return fmt.Errorf("file analysis path is not eligible: %s", decision.Reason)
	}
	if _, err := ResolveFile(c.root, input.Path); err != nil {
		return err
	}
	for _, value := range []string{input.ProjectID, input.ProjectRevision, input.ContentHash, input.Language, input.PromptVersion, input.ContextPolicyVersion} {
		if value == "" {
			return fmt.Errorf("file analysis validity inputs are required")
		}
	}
	return nil
}

func newMissingAnalysis(input FileAnalysisInput) *FileAnalysis {
	return &FileAnalysis{SchemaVersion: fileAnalysisSchemaVersion, ProjectID: input.ProjectID, ProjectRevision: input.ProjectRevision, Path: normalizedAnalysisPath(input.Path), ContentHash: input.ContentHash, Language: input.Language, Status: AnalysisStatusMissing, Model: input.Model, ConfiguredModel: input.Model, Profile: input.Profile, Scope: input.Scope, ProviderOrigin: input.ProviderOrigin, ReasoningEffort: input.ReasoningEffort, PromptVersion: input.PromptVersion, ContextPolicyVersion: input.ContextPolicyVersion}
}

func inputFromAnalysis(analysis FileAnalysis) FileAnalysisInput {
	model := analysis.ConfiguredModel
	if model == "" {
		model = analysis.Model
	}
	return FileAnalysisInput{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Path: analysis.Path, ContentHash: analysis.ContentHash, Language: analysis.Language, Model: model, Profile: analysis.Profile, Scope: analysis.Scope, ProviderOrigin: analysis.ProviderOrigin, ReasoningEffort: analysis.ReasoningEffort, PromptVersion: analysis.PromptVersion, ContextPolicyVersion: analysis.ContextPolicyVersion}
}

func analysisMatches(analysis FileAnalysis, input FileAnalysisInput) bool {
	// A project revision changes for every eligible source edit. Content hashes
	// keep invalidation local to the edited file while the stored revision still
	// records the project state that informed the original summary.
	return analysis.SchemaVersion == fileAnalysisSchemaVersion && analysis.ProjectID == input.ProjectID && analysis.Path == normalizedAnalysisPath(input.Path) && analysis.ContentHash == input.ContentHash && analysis.Language == input.Language && modelsMatch(analysis.ConfiguredModel, input.Model) && analysis.Profile == input.Profile && analysis.Scope == input.Scope && analysis.ProviderOrigin == input.ProviderOrigin && analysis.ReasoningEffort == input.ReasoningEffort && analysis.PromptVersion == input.PromptVersion && analysis.ContextPolicyVersion == input.ContextPolicyVersion
}

func modelsMatch(stored, requested string) bool {
	return requested == "" || stored == "" || stored == requested
}

func validStoredAnalysis(analysis FileAnalysis) bool {
	return analysis.SchemaVersion == fileAnalysisSchemaVersion && analysis.Path != "" && analysis.ProjectID != "" && analysis.ProjectRevision != "" && analysis.ContentHash != "" && analysis.Language != "" && analysis.PromptVersion != "" && analysis.ContextPolicyVersion != "" && validAnalysisStatus(analysis.Status)
}

func validAnalysisStatus(status string) bool {
	switch status {
	case AnalysisStatusFresh, AnalysisStatusMissing, AnalysisStatusStale, AnalysisStatusFailed, AnalysisStatusRunning:
		return true
	default:
		return false
	}
}

func normalizedAnalysisPath(path string) string { return filepath.ToSlash(filepath.Clean(path)) }

func analysisPathKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func cloneFileAnalysis(source *FileAnalysis) *FileAnalysis {
	copy := *source
	copy.Responsibilities = append([]string(nil), source.Responsibilities...)
	copy.Imports = append([]string(nil), source.Imports...)
	copy.Dependencies = append([]string(nil), source.Dependencies...)
	copy.SideEffects = append([]string(nil), source.SideEffects...)
	copy.Risks = append([]Finding(nil), source.Risks...)
	for index := range copy.Risks {
		copy.Risks[index].TaskSpec = insight.CloneBugTaskSpec(source.Risks[index].TaskSpec)
		copy.Risks[index].EngineeringInsight = CloneEngineeringInsight(source.Risks[index].EngineeringInsight)
	}
	copy.Suggestions = append([]Suggestion(nil), source.Suggestions...)
	for index := range copy.Suggestions {
		copy.Suggestions[index].EngineeringInsight = CloneEngineeringInsight(source.Suggestions[index].EngineeringInsight)
	}
	copy.EngineeringInsight = CloneEngineeringInsight(source.EngineeringInsight)
	return &copy
}

const MaxEngineeringInsightRunes = insight.MaxEngineeringInsightRunes

type OptionalEngineeringInsightReason = insight.OptionalEngineeringInsightReason

const (
	OptionalEngineeringInsightAbsent             = insight.OptionalEngineeringInsightAbsent
	OptionalEngineeringInsightNull               = insight.OptionalEngineeringInsightNull
	OptionalEngineeringInsightInvalidShape       = insight.OptionalEngineeringInsightInvalidShape
	OptionalEngineeringInsightEmptyRequiredField = insight.OptionalEngineeringInsightEmptyRequiredField
	OptionalEngineeringInsightOverLimit          = insight.OptionalEngineeringInsightOverLimit
	OptionalEngineeringInsightAccepted           = insight.OptionalEngineeringInsightAccepted
)

type OptionalEngineeringInsightPresence = insight.OptionalEngineeringInsightPresence

const (
	OptionalEngineeringInsightAbsentPresence = insight.OptionalEngineeringInsightAbsentPresence
	OptionalEngineeringInsightNullPresence   = insight.OptionalEngineeringInsightNullPresence
	OptionalEngineeringInsightValuePresence  = insight.OptionalEngineeringInsightValuePresence
)

type OptionalEngineeringInsightFieldDiagnostic = insight.OptionalEngineeringInsightFieldDiagnostic
type OptionalEngineeringInsightDiagnostic = insight.OptionalEngineeringInsightDiagnostic

const EngineeringInsightPromptInstructions = insight.EngineeringInsightPromptInstructions

type EngineeringInsight = insight.EngineeringInsight

// ParseOptionalEngineeringInsight isolates optional model prose from strict
// parent contracts. Invalid prose is omitted without rejecting valid parent data.
func ParseOptionalEngineeringInsight(raw json.RawMessage) (*EngineeringInsight, string) {
	return insight.ParseOptionalEngineeringInsight(raw)
}

// ParseOptionalEngineeringInsightDiagnostic shares the production parser's
// validation path while returning source-free facts needed by private
// evaluation evidence.
func ParseOptionalEngineeringInsightDiagnostic(raw json.RawMessage) (*EngineeringInsight, OptionalEngineeringInsightDiagnostic) {
	return insight.ParseOptionalEngineeringInsightDiagnostic(raw)
}

// ValidEngineeringInsight normalizes trusted presentation text after decoding.
func ValidEngineeringInsight(value *EngineeringInsight) bool {
	return insight.ValidEngineeringInsight(value)
}

func CloneEngineeringInsight(source *EngineeringInsight) *EngineeringInsight {
	return insight.CloneEngineeringInsight(source)
}

const (
	BugTaskSpecSchemaVersion = insight.BugTaskSpecSchemaVersion
	MaxBugTaskItems          = insight.MaxBugTaskItems
	MaxBugTaskItemBytes      = insight.MaxBugTaskItemBytes
	MaxBugTaskCandidateBytes = insight.MaxBugTaskCandidateBytes
)

type BugTaskSpec = insight.BugTaskSpec
type GoTestCandidateSpec = insight.GoTestCandidateSpec
type TestCandidateSpec = insight.TestCandidateSpec

// SanitizeBugTaskSpec applies the same secret redaction and text normalization
// used by persisted findings while preserving no shared mutable slices.
func SanitizeBugTaskSpec(spec *BugTaskSpec) *BugTaskSpec {
	return insight.SanitizeBugTaskSpec(spec)
}
