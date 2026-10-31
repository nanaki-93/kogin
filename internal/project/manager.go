package project

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Manager owns the active project selected by the UI.
type Manager struct {
	mu       sync.RWMutex
	root     string
	analysis *Analysis
	index    *ProjectIndex
}

func NewManager(initialRoot string) (*Manager, error) {
	root, err := CanonicalRoot(initialRoot)
	if err != nil {
		return nil, err
	}
	return &Manager{root: root}, nil
}

func (m *Manager) Root() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.root
}

// ActivateProject activates an imported project and persists its deterministic index.
func (m *Manager) ActivateProject(root string, analysis *Analysis) error {
	return m.activate(root, analysis, true)
}

func (m *Manager) Set(root string, analysis *Analysis) error {
	return m.ActivateProject(root, analysis)
}

// RestoreProject activates a previously imported project while keeping startup
// restoration free from persisted index rewrites.
func (m *Manager) RestoreProject(root string, analysis *Analysis) error {
	return m.activate(root, analysis, false)
}

func (m *Manager) Restore(root string, analysis *Analysis) error {
	return m.RestoreProject(root, analysis)
}

func (m *Manager) activate(root string, analysis *Analysis, persistIndex bool) error {
	if analysis == nil {
		return fmt.Errorf("project analysis is required")
	}
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return err
	}
	id := projectID(canonical)
	revision, err := projectRevision(canonical)
	if err != nil {
		return err
	}
	index, err := buildIndex(canonical, id, revision, persistIndex)
	if err != nil {
		return err
	}
	analysis.ProjectID = id
	analysis.ProjectRevision = revision
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root = canonical
	m.analysis = analysis
	m.index = index
	return nil
}

// Reindex refreshes deterministic facts without contacting the model.
func (m *Manager) Reindex() (*ProjectIndex, error) {
	m.mu.RLock()
	if m.analysis == nil {
		m.mu.RUnlock()
		return nil, ErrNoActiveProject
	}
	root := m.root
	id := m.analysis.ProjectID
	m.mu.RUnlock()
	revision, err := projectRevision(root)
	if err != nil {
		return nil, err
	}
	index, err := BuildIndex(root, id, revision)
	if err != nil {
		return nil, err
	}
	detection := detectProject(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.root != root || m.analysis == nil || m.analysis.ProjectID != id {
		return nil, ErrRevisionConflict
	}
	m.analysis.ProjectRevision = revision
	m.analysis.Type = detection.Type
	m.analysis.BuildFile = detection.BuildFile
	m.analysis.FileCount = len(index.Files)
	m.analysis.SourceFileCount = 0
	m.analysis.TotalLines = 0
	m.analysis.Languages = make(map[string]int)
	m.analysis.Files = make([]string, 0, len(index.Files))
	for _, file := range index.Files {
		m.analysis.Files = append(m.analysis.Files, file.Path)
		if file.Language != "Text" {
			m.analysis.SourceFileCount++
			m.analysis.Languages[file.Language]++
			m.analysis.TotalLines += file.LineCount
		}
	}
	if m.analysis.Report.Status == ProjectAnalysisStatusFresh && m.analysis.Report.ProjectRevision != revision {
		m.analysis.Report.Status = ProjectAnalysisStatusStale
		m.analysis.AIStatus = ProjectAnalysisStatusStale
	}
	m.index = index
	return cloneIndex(index), nil
}

// Index returns a copy of the active deterministic project index.
func (m *Manager) Index() (*ProjectIndex, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.index == nil {
		return nil, ErrNoActiveProject
	}
	return cloneIndex(m.index), nil
}

// UpdateFileAnalysisStatus persists the cache status projection for one file.
// The project identity and revision guard the update against late analysis
// responses from a previous project state.
func (m *Manager) UpdateFileAnalysisStatus(projectID, projectRevision, relative, status string) error {
	if !validAnalysisStatus(status) {
		return fmt.Errorf("invalid file analysis status %q", status)
	}
	normalized, err := normalizedIndexedPath(relative)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validFileAnalysisStatusUpdate(projectID, projectRevision); err != nil {
		return err
	}
	return m.updateIndexedFileStatus(normalized, status)
}

// ResetFileAnalysisStatuses resets every indexed file's analysis status to missing.
func (m *Manager) ResetFileAnalysisStatuses(projectID, projectRevision string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validFileAnalysisStatusUpdate(projectID, projectRevision); err != nil {
		return err
	}
	changed := false
	for index := range m.index.Files {
		if m.index.Files[index].AnalysisStatus != "" && m.index.Files[index].AnalysisStatus != AnalysisStatusMissing {
			m.index.Files[index].AnalysisStatus = AnalysisStatusMissing
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeIndex(m.root, m.index)
}

func normalizedIndexedPath(relative string) (string, error) {
	normalized := filepath.ToSlash(filepath.Clean(relative))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid indexed file path: %s", relative)
	}
	return normalized, nil
}

func (m *Manager) validFileAnalysisStatusUpdate(projectID, projectRevision string) error {
	if m.analysis == nil || m.index == nil {
		return ErrNoActiveProject
	}
	if m.analysis.ProjectID != projectID || m.analysis.ProjectRevision != projectRevision || m.index.ProjectID != projectID || m.index.ProjectRevision != projectRevision {
		return ErrRevisionConflict
	}
	return nil
}

func (m *Manager) updateIndexedFileStatus(normalized, status string) error {
	for index := range m.index.Files {
		if m.index.Files[index].Path != normalized {
			continue
		}
		if m.index.Files[index].AnalysisStatus == status {
			return nil
		}
		previous := m.index.Files[index].AnalysisStatus
		m.index.Files[index].AnalysisStatus = status
		if err := writeIndex(m.root, m.index); err != nil {
			m.index.Files[index].AnalysisStatus = previous
			return err
		}
		return nil
	}
	return fmt.Errorf("indexed file not found: %s", normalized)
}

func (m *Manager) ImpactPreview(relative string) (ImpactPreview, error) {
	if _, err := m.IndexedFile(relative); err != nil {
		return ImpactPreview{}, err
	}
	index, err := m.Index()
	if err != nil {
		return ImpactPreview{}, err
	}
	return BuildImpactPreview(index, relative), nil
}

// IndexedFile returns one active-project file after canonical-path and policy
// checks. The final lock check prevents a file from a previously active project
// being returned after a concurrent import or reindex.
func (m *Manager) IndexedFile(relative string) (*IndexFile, error) {
	m.mu.RLock()
	if m.index == nil || m.analysis == nil {
		m.mu.RUnlock()
		return nil, ErrNoActiveProject
	}
	root := m.root
	revision := m.index.ProjectRevision
	m.mu.RUnlock()

	policy, err := NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	decision := policy.Decide(relative)
	if decision.Reason == "unsafe path" {
		return nil, fmt.Errorf("invalid indexed file path: %s", relative)
	}
	if !decision.Include {
		return nil, fmt.Errorf("%w: %s", ErrExcludedFile, decision.Reason)
	}
	resolved, err := ResolveFile(root, relative)
	if err != nil {
		return nil, err
	}
	normalized, err := filepath.Rel(root, resolved)
	if err != nil {
		return nil, fmt.Errorf("normalize indexed file path: %w", err)
	}
	normalized = filepath.ToSlash(normalized)

	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.root != root || m.index == nil || m.index.ProjectRevision != revision {
		return nil, ErrRevisionConflict
	}
	for _, file := range m.index.Files {
		if file.Path == normalized {
			copy := file
			copy.Imports = append([]string(nil), file.Imports...)
			copy.Diagnostics = append([]Diagnostic(nil), file.Diagnostics...)
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("indexed file not found: %s", normalized)
}

func (m *Manager) Analysis() (*Analysis, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.analysis == nil {
		return nil, ErrNoActiveProject
	}
	copy := *m.analysis
	copy.Languages = cloneMap(m.analysis.Languages)
	copy.Files = append([]string(nil), m.analysis.Files...)
	copy.Report = *cloneProjectAnalysisReport(&m.analysis.Report)
	return &copy, nil
}

// UpdateReport replaces the active project's overview report, when it is
// still the project the report was written for.
func (m *Manager) UpdateReport(projectID string, report ProjectAnalysisReport, summary string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.analysis == nil || m.analysis.ProjectID != projectID {
		return ErrRevisionConflict
	}
	m.analysis.Report = *cloneProjectAnalysisReport(&report)
	m.analysis.AIStatus, m.analysis.Summary = report.Status, summary
	return nil
}

// ValidateMutableRequest rejects stale projects and files before candidate work.
func (m *Manager) ValidateMutableRequest(id, revision, relativePath, baseFileHash string) error {
	if id == "" || revision == "" || baseFileHash == "" {
		return fmt.Errorf("project_id, project_revision, and base_file_hash are required")
	}
	m.mu.RLock()
	if m.analysis == nil {
		m.mu.RUnlock()
		return ErrNoActiveProject
	}
	root := m.root
	currentID := m.analysis.ProjectID
	currentRevision := m.analysis.ProjectRevision
	m.mu.RUnlock()
	if id != currentID || revision != currentRevision {
		return ErrRevisionConflict
	}
	fullPath, err := ResolveFile(root, relativePath)
	if err != nil {
		return err
	}
	currentHash, err := hashFile(fullPath)
	if err != nil {
		return err
	}
	if currentHash != baseFileHash {
		return ErrRevisionConflict
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.root != root || m.analysis == nil || m.analysis.ProjectID != id || m.analysis.ProjectRevision != revision {
		return ErrRevisionConflict
	}
	return nil
}

func cloneMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneIndex(source *ProjectIndex) *ProjectIndex {
	copy := *source
	copy.Files = make([]IndexFile, len(source.Files))
	for i, file := range source.Files {
		copy.Files[i] = file
		copy.Files[i].Imports = append([]string(nil), file.Imports...)
		copy.Files[i].Diagnostics = append([]Diagnostic(nil), file.Diagnostics...)
	}
	return &copy
}
