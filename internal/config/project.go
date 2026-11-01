package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/nanaki-93/kasuri/v2/internal/storage"
	"gopkg.in/yaml.v3"
)

// A project's own configuration lives in its private metadata folder. It
// uses the daemon's schema without the daemon-level sections and is applied
// on top of the daemon's configuration: a model scope it names replaces that
// scope whole (so a provider change leaves no stale address or key behind),
// and every other key it sets replaces the daemon's value for that key.

// ProjectConfigPath is the project configuration file, relative to the project.
const ProjectConfigPath = ".kasuri/config.yaml"

const maxProjectConfigBytes = 1 << 20

// daemonOnlyFields are top-level keys that configure the daemon process, not
// a project.
var daemonOnlyFields = []string{"logging", "project_path"}

var modelScopeKeys = []ModelScope{AnalyzeModelScope, BugModelScope, CodeModelScope}

// ProjectFile is the absolute path of root's configuration file, refusing
// metadata paths that escape root through symlinks.
func ProjectFile(root string) (string, error) {
	return storage.MetadataPath(root, ".kasuri", "config.yaml")
}

// LoadProject returns the configuration root runs with: global with root's
// project configuration applied, and whether that file exists. Without the
// file, it returns global unchanged.
func LoadProject(root string, global *Config) (*Config, bool, error) {
	data, err := ReadProject(root)
	if errors.Is(err, os.ErrNotExist) {
		return global, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	merged, err := MergeProject(global, data)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", ProjectConfigPath, err)
	}
	if path, err := ProjectFile(root); err == nil {
		merged.Source = path
	}
	return merged, true, nil
}

// ReadProject reads root's project configuration file.
func ReadProject(root string) ([]byte, error) {
	path, err := ProjectFile(root)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProjectConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxProjectConfigBytes {
		return nil, fmt.Errorf("%s exceeds 1 MiB", ProjectConfigPath)
	}
	return data, nil
}

// MergeProject applies a project configuration document to a copy of global
// and validates the result. global is not changed.
func MergeProject(global *Config, data []byte) (*Config, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse project configuration: %w", err)
	}
	merged, err := cloneConfig(global)
	if err != nil {
		return nil, err
	}
	if len(document.Content) == 0 {
		return merged, nil
	}
	top := document.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("project configuration must be a mapping")
	}
	for index := 0; index+1 < len(top.Content); index += 2 {
		if key := top.Content[index].Value; slices.Contains(daemonOnlyFields, key) {
			return nil, fmt.Errorf("%q configures the daemon; set it in the daemon's configuration, not the project's", key)
		}
	}
	if err := rejectUnknownMapping(top, "", configurationFields); err != nil {
		return nil, err
	}
	for _, scope := range namedModelScopes(top) {
		*merged.ModelScopes.profile(scope) = ModelProfileConfig{}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(merged); err != nil {
		return nil, fmt.Errorf("parse project configuration: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse project configuration: it must contain one YAML document")
	}
	merged.applyDefaults()
	if err := merged.Validate(); err != nil {
		return nil, err
	}
	return merged, nil
}

func namedModelScopes(top *yaml.Node) []ModelScope {
	scopes := mappingValue(top, "model_scopes")
	if scopes == nil {
		return nil
	}
	var named []ModelScope
	for _, scope := range modelScopeKeys {
		if mappingValue(scopes, string(scope)) != nil {
			named = append(named, scope)
		}
	}
	return named
}

func (scopes *ModelScopesConfig) profile(scope ModelScope) *ModelProfileConfig {
	switch scope {
	case BugModelScope:
		return &scopes.Bug
	case CodeModelScope:
		return &scopes.Code
	default:
		return &scopes.Analyze
	}
}

// Profile returns the configured portion of a scope.
func (scopes ModelScopesConfig) Profile(scope ModelScope) ModelProfileConfig {
	return *scopes.profile(scope)
}

// cloneConfig deep-copies a configuration through its YAML form.
func cloneConfig(cfg *Config) (*Config, error) {
	if cfg == nil {
		cfg = Default()
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var clone Config
	if err := yaml.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}
