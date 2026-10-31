package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

const (
	findingStorePath   = ".mini-orca/findings.json"
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

type findingDocument struct {
	SchemaVersion string              `json:"schema_version"`
	Findings      []UnifiedFinding    `json:"findings"`
	Completed     []FindingCompletion `json:"completed,omitempty"`
	Dismissed     []FindingTarget     `json:"dismissed,omitempty"`
}

// Store instances for the same checkout share read-modify-write ownership.
var findingStoreLocks sync.Map

// FindingStore persists only sanitized finding metadata under .mini-orca.
type FindingStore struct {
	root string
	mu   *sync.Mutex
}

func NewFindingStore(root string) (*FindingStore, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	lock, _ := findingStoreLocks.LoadOrStore(canonical, &sync.Mutex{})
	return &FindingStore{root: canonical, mu: lock.(*sync.Mutex)}, nil
}

func (s *FindingStore) Load(input FindingInput) ([]UnifiedFinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked(true)
	if err != nil {
		return nil, err
	}
	applyFindingCompletions(document)
	findings := document.Findings
	for index := range findings {
		if !findingMatchesInput(findings[index], input) {
			findings[index].Freshness = FindingFreshnessStale
		}
	}
	return cloneFindings(findings), nil
}

// ReconcileSource updates findings from one producer without retiring findings
// from the other producers. This keeps explicit scan results and cached AI
// suggestions independently refreshable.
func (s *FindingStore) ReconcileSource(input FindingInput, source string, reported []UnifiedFinding) ([]UnifiedFinding, error) {
	if !validFindingSource(source) {
		return nil, fmt.Errorf("finding source is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ProjectID == "" || input.ProjectRevision == "" {
		return nil, fmt.Errorf("finding project identity is required")
	}
	document, err := s.loadLocked(true)
	if err != nil {
		return nil, err
	}
	previous := document.Findings
	byID := make(map[string]UnifiedFinding, len(previous))
	current := make([]UnifiedFinding, 0, len(previous)+len(reported))
	for _, finding := range previous {
		if finding.Source == source {
			byID[finding.ID] = finding
			continue
		}
		current = append(current, finding)
	}
	seen := make(map[string]bool, len(reported))
	for _, finding := range reported {
		if finding.Source != source {
			return nil, fmt.Errorf("finding source does not match reconciliation source")
		}
		finding = normalizeFinding(finding, input)
		if err := validateFinding(finding); err != nil {
			return nil, err
		}
		if prior, ok := byID[finding.ID]; ok && prior.Status != FindingStatusRetired {
			finding.Status = prior.Status
		}
		seen[finding.ID] = true
		current = append(current, finding)
	}
	for id, finding := range byID {
		if seen[id] {
			continue
		}
		finding.Status = unseenFindingStatus(finding.Status)
		finding.Freshness = FindingFreshnessStale
		current = append(current, finding)
	}
	sort.Slice(current, func(i, j int) bool { return current[i].ID < current[j].ID })
	document.Findings = current
	applyFindingCompletions(document)
	if err := s.storeLocked(document); err != nil {
		return nil, err
	}
	return cloneFindings(current), nil
}

// RecordFindings adds or refreshes findings of one source without retiring
// the others it has. A finding that comes back keeps its triage and first
// detection time; a retired one opens again.
func (s *FindingStore) RecordFindings(input FindingInput, source string, reported []UnifiedFinding) error {
	if !validFindingSource(source) || input.ProjectID == "" || input.ProjectRevision == "" {
		return fmt.Errorf("finding source or project identity is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked(true)
	if err != nil {
		return err
	}
	byID := make(map[string]int, len(document.Findings))
	for index, finding := range document.Findings {
		byID[finding.ID] = index
	}
	for _, finding := range reported {
		if finding.Source != source {
			return fmt.Errorf("finding source does not match the recorded source")
		}
		finding = normalizeFinding(finding, input)
		if err := validateFinding(finding); err != nil {
			return err
		}
		index, known := byID[finding.ID]
		if !known {
			byID[finding.ID] = len(document.Findings)
			document.Findings = append(document.Findings, finding)
			continue
		}
		prior := document.Findings[index]
		if prior.Status != FindingStatusRetired {
			finding.Status = prior.Status
		}
		finding.DetectedAt = prior.DetectedAt
		document.Findings[index] = finding
	}
	sort.Slice(document.Findings, func(i, j int) bool { return document.Findings[i].ID < document.Findings[j].ID })
	return s.storeLocked(document)
}

func unseenFindingStatus(status string) string {
	if status == FindingStatusInProgress {
		return FindingStatusInProgress
	}
	return FindingStatusRetired
}

// SetStatus records an explicit user triage decision for one persisted finding.
func (s *FindingStore) SetStatus(id, status string) error {
	if id == "" || !validFindingStatus(status) || status == FindingStatusRetired {
		return fmt.Errorf("finding status is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked(true)
	if err != nil {
		return err
	}
	findings := document.Findings
	for index := range findings {
		if findings[index].ID == id {
			findings[index].Status = status
			updateSemanticCompletion(document, findings[index], status)
			return s.storeLocked(document)
		}
	}
	return fmt.Errorf("finding not found")
}

func (s *FindingStore) clearLocked() error {
	path, err := storage.MetadataPath(s.root, findingStorePath)
	if err != nil {
		return fmt.Errorf("clear findings: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear findings: %w", err)
	}
	return nil
}

// ClearExcept removes persisted findings except those that are currently in
// progress and those from the rules source, which no analysis run reproduces.
func (s *FindingStore) ClearExcept(keep []FindingTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked(true)
	if err != nil {
		return err
	}
	if len(keep) == 0 {
		// Nothing is in progress: completion records go with the findings.
		document.Completed, document.Dismissed = nil, nil
	}
	retained := make([]UnifiedFinding, 0)
	for _, finding := range document.Findings {
		if finding.Source == FindingSourceRules {
			retained = append(retained, finding)
			continue
		}
		if finding.Status == FindingStatusInProgress || findingMatchesAnyTarget(finding, keep) {
			if finding.Status == FindingStatusOpen || finding.Status == FindingStatusRetired {
				finding.Status = FindingStatusInProgress
			}
			retained = append(retained, finding)
		}
	}
	if len(retained) == 0 && len(document.Completed) == 0 && len(document.Dismissed) == 0 {
		return s.clearLocked()
	}
	document.Findings = retained
	return s.storeLocked(document)
}

func findingMatchesAnyTarget(finding UnifiedFinding, targets []FindingTarget) bool {
	for _, target := range targets {
		if (target.ProjectID == "" || target.ProjectID == finding.ProjectID) && target.ID == finding.ID {
			return true
		}
	}
	return false
}

func (s *FindingStore) loadLocked(recoverCorrupt bool) (*findingDocument, error) {
	path, err := storage.MetadataPath(s.root, findingStorePath)
	if err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &findingDocument{SchemaVersion: findingStoreSchema, Findings: []UnifiedFinding{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read findings: %w", err)
	}
	var document findingDocument
	if err := json.Unmarshal(data, &document); err != nil || document.SchemaVersion != findingStoreSchema {
		if !recoverCorrupt {
			return nil, fmt.Errorf("findings storage is corrupt or unsupported")
		}
		if recoverErr := storage.RecoverCorrupt(path); recoverErr != nil {
			return nil, recoverErr
		}
		return &findingDocument{SchemaVersion: findingStoreSchema, Findings: []UnifiedFinding{}}, nil
	}
	return &document, nil
}

func (s *FindingStore) storeLocked(document *findingDocument) error {
	path, err := storage.MetadataPath(s.root, findingStorePath)
	if err != nil {
		return fmt.Errorf("store findings: %w", err)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode findings: %w", err)
	}
	if err := storage.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("store findings: %w", err)
	}
	return nil
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
	return validPersistedGoTestCandidate(spec.GoTestCandidate)
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

// SuggestedFindingsForProject adapts fresh structured AI risks without ever
// claiming they came from a verification tool.
func SuggestedFindingsForProject(report ProjectAnalysisReport) []UnifiedFinding {
	if report.Status != ProjectAnalysisStatusFresh {
		return []UnifiedFinding{}
	}
	findings := make([]UnifiedFinding, 0, len(report.Risks))
	for _, risk := range report.Risks {
		eng := risk.EngineeringInsight
		if eng == nil {
			eng = report.EngineeringInsight
		}
		findings = append(findings, UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: risk.Severity, Title: risk.Summary, Message: risk.Summary, OriginatingAnalysis: "project", EngineeringInsight: CloneEngineeringInsight(eng)})
	}
	return findings
}

// SuggestedFindingsForFile adapts fresh file-analysis risks with the precise
// file path and hash that must remain current before a fix is prepared.
func SuggestedFindingsForFile(analysis FileAnalysis) []UnifiedFinding {
	if analysis.Status != AnalysisStatusFresh {
		return []UnifiedFinding{}
	}
	findings := make([]UnifiedFinding, 0, len(analysis.Risks))
	for idx, risk := range analysis.Risks {
		title := strings.TrimSpace(risk.Title)
		if title == "" {
			// Historical reports and providers without structured output may omit titles.
			title = risk.Summary
		}
		location := FindingLocation{Path: analysis.Path}
		eng := resolveFileRiskInsight(analysis, risk, idx)
		findings = append(findings, UnifiedFinding{Category: risk.Category, Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: risk.Severity, Title: title, Message: risk.Summary, FileHash: analysis.ContentHash, Location: location, OriginatingAnalysis: "file", TaskSpec: insight.CloneBugTaskSpec(risk.TaskSpec), EngineeringInsight: eng})
	}
	return findings
}

func resolveFileRiskInsight(analysis FileAnalysis, risk Finding, riskIdx int) *EngineeringInsight {
	if risk.EngineeringInsight != nil {
		return CloneEngineeringInsight(risk.EngineeringInsight)
	}
	if sug := matchSuggestionForRisk(analysis.Suggestions, riskIdx, len(analysis.Risks)); sug != nil {
		if sug.EngineeringInsight != nil {
			return CloneEngineeringInsight(sug.EngineeringInsight)
		}
		lesson := strings.TrimSpace(sug.Action)
		if lesson == "" {
			lesson = strings.TrimSpace(sug.Summary)
		}
		why := strings.TrimSpace(sug.Summary)
		if why == "" {
			why = strings.TrimSpace(sug.Title)
		}
		if lesson != "" && why != "" && strings.TrimSpace(risk.Summary) != "" {
			return &EngineeringInsight{
				Mechanism:          strings.TrimSpace(risk.Summary),
				WhyItMattersHere:   why,
				TransferableLesson: lesson,
			}
		}
	}
	return CloneEngineeringInsight(analysis.EngineeringInsight)
}

func matchSuggestionForRisk(suggestions []Suggestion, riskIdx, riskCount int) *Suggestion {
	if len(suggestions) == 0 {
		return nil
	}
	if riskCount == 1 && len(suggestions) == 1 {
		return &suggestions[0]
	}
	if riskIdx >= 0 && riskIdx < len(suggestions) && len(suggestions) == riskCount {
		return &suggestions[riskIdx]
	}
	return nil
}
