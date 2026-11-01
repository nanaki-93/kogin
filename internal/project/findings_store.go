package project

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

type findingDocument struct {
	SchemaVersion string              `json:"schema_version"`
	Findings      []UnifiedFinding    `json:"findings"`
	Completed     []FindingCompletion `json:"completed,omitempty"`
	Dismissed     []FindingTarget     `json:"dismissed,omitempty"`
}

// Store instances for the same checkout share read-modify-write ownership.
var findingStoreLocks sync.Map

// FindingStore persists only sanitized finding metadata under .kasuri.
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
