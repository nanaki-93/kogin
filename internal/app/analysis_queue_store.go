package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

const analysisSelectionRelativePath = ".kasuri/analysis/selection.json"
const analysisSelectionExclusion = "Ignored in analysis file selection."

type storedAnalysisSelection struct {
	SchemaVersion string   `json:"schema_version"`
	ExcludedPaths []string `json:"excluded_paths"`
}

func validateAnalysisExcludedPaths(paths []string) error {
	if len(paths) > maxAnalysisInventoryFiles {
		return fmt.Errorf("too many analysis exclusions")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path == "." || !analysisMetadataPath(path) || seen[path] {
			return fmt.Errorf("invalid or duplicate analysis exclusion")
		}
		seen[path] = true
	}
	return nil
}

func loadAnalysisSelection(root string) ([]string, error) {
	path, err := storage.MetadataPath(root, analysisSelectionRelativePath)
	if err != nil {
		return nil, fmt.Errorf("read analysis selection: %w", err)
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read analysis selection: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxAnalysisRunMetadataBytes+1))
	decoder.DisallowUnknownFields()
	var saved storedAnalysisSelection
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("decode analysis selection: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing analysis selection data")
	}
	if saved.SchemaVersion != "1" || saved.ExcludedPaths == nil {
		return nil, fmt.Errorf("unsupported or invalid analysis selection")
	}
	if err := validateAnalysisExcludedPaths(saved.ExcludedPaths); err != nil {
		return nil, err
	}
	sort.Strings(saved.ExcludedPaths)
	return saved.ExcludedPaths, nil
}

func saveAnalysisSelection(root string, paths []string) error {
	path, err := storage.MetadataPath(root, analysisSelectionRelativePath)
	if err != nil {
		return fmt.Errorf("save analysis selection: %w", err)
	}
	var data bytes.Buffer
	if err := json.NewEncoder(&data).Encode(storedAnalysisSelection{SchemaVersion: "1", ExcludedPaths: paths}); err != nil {
		return err
	}
	if err := storage.WriteFile(path, data.Bytes(), 0600); err != nil {
		return fmt.Errorf("save analysis selection: %w", err)
	}
	return nil
}
