package project

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

// ModuleSummaryPromptVersion names the prompt that writes module summaries.
const ModuleSummaryPromptVersion = "module-summary-v2"

const moduleSummariesSchemaVersion = "1"

// ModuleSummaryRecord is a stored module summary and the inputs it was
// written from: Key changes when a member file's summary, the model or the
// prompt changes.
type ModuleSummaryRecord struct {
	ModuleSummary
	Key         string    `json:"key"`
	Model       string    `json:"model"`
	GeneratedAt time.Time `json:"generated_at"`
}

// ModuleSummaries are a project's stored module summaries, by component path.
type ModuleSummaries struct {
	SchemaVersion string                         `json:"schema_version"`
	ProjectID     string                         `json:"project_id"`
	Entries       map[string]ModuleSummaryRecord `json:"entries"`
}

// LoadModuleSummaries reads the stored module summaries; a missing or
// unreadable file, or one from another project, reads as none.
func LoadModuleSummaries(root, projectID string) (ModuleSummaries, error) {
	empty := ModuleSummaries{SchemaVersion: moduleSummariesSchemaVersion, ProjectID: projectID, Entries: map[string]ModuleSummaryRecord{}}
	path, err := storage.MetadataPath(root, ".mini-orca", "module-summaries.json")
	if err != nil {
		return empty, fmt.Errorf("read module summaries: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("read module summaries: %w", err)
	}
	var stored ModuleSummaries
	if json.Unmarshal(data, &stored) != nil || stored.SchemaVersion != moduleSummariesSchemaVersion || stored.Entries == nil {
		return empty, storage.RecoverCorrupt(path)
	}
	if stored.ProjectID != projectID {
		return empty, nil
	}
	return stored, nil
}

// StoreModuleSummaries atomically replaces the stored module summaries.
func StoreModuleSummaries(root string, summaries ModuleSummaries) error {
	summaries.SchemaVersion = moduleSummariesSchemaVersion
	path, err := storage.MetadataPath(root, ".mini-orca", "module-summaries.json")
	if err != nil {
		return fmt.Errorf("store module summaries: %w", err)
	}
	data, err := json.MarshalIndent(summaries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode module summaries: %w", err)
	}
	if err := storage.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("store module summaries: %w", err)
	}
	return nil
}

// Summaries are the stored summaries by path, as the docs take them.
func (m ModuleSummaries) Summaries() map[string]ModuleSummary {
	summaries := make(map[string]ModuleSummary, len(m.Entries))
	for path, entry := range m.Entries {
		summaries[path] = entry.ModuleSummary
	}
	return summaries
}
