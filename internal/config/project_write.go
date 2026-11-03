package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/storage"
	"gopkg.in/yaml.v3"
)

// ProjectUpdate is what project setup writes. Keys of the file it does not
// name, and their comments, are kept.
type ProjectUpdate struct {
	// ModelScopes replaces each named scope whole.
	ModelScopes map[ModelScope]ModelProfileConfig
	Roles       *AgentRoles
	// Limits sets agent_runtime's max_* budget keys.
	Limits *AgentLimits
	// Timeouts sets every timeouts key.
	Timeouts *TimeoutConfig
}

const projectConfigHeader = "# Kogin configuration for this project. It is applied on top of the\n# daemon's configuration and may hold API keys: keep it out of version control."

const projectIgnore = "config.yaml"

// UpdateProject applies update to root's project configuration, creating the
// file when needed. The result is validated on top of global before it is
// written; the file is private (0600) and kept out of Git. It returns the
// configuration root now runs with.
func UpdateProject(root string, global *Config, update ProjectUpdate) (*Config, error) {
	document, err := projectDocument(root)
	if err != nil {
		return nil, err
	}
	top := document.Content[0]
	for _, scope := range modelScopeKeys {
		if profile, ok := update.ModelScopes[scope]; ok {
			setMappingValue(ensureMapping(top, "model_scopes"), string(scope), structNode(profile))
		}
	}
	if update.Roles != nil {
		setMappingValue(ensureMapping(top, "agent_runtime"), "roles", structNode(*update.Roles))
	}
	if limits := update.Limits; limits != nil {
		runtime := ensureMapping(top, "agent_runtime")
		for _, item := range []struct {
			key   string
			value any
		}{
			{"max_model_calls", limits.ModelCalls}, {"max_tool_calls", limits.ToolCalls}, {"max_input_tokens", limits.InputTokens},
			{"max_output_tokens", limits.OutputTokens}, {"max_elapsed_seconds", limits.ElapsedSeconds},
			{"max_concurrent_calls", limits.ConcurrentCalls}, {"max_repairs", limits.Repairs},
		} {
			setMappingValue(runtime, item.key, scalarNode(item.value))
		}
	}
	if timeouts := update.Timeouts; timeouts != nil {
		setMappingValue(top, "timeouts", structNode(*timeouts))
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	data := encoded.Bytes()
	merged, err := MergeProject(global, data)
	if err != nil {
		return nil, err
	}
	path, err := ProjectFile(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := storage.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", ProjectConfigPath, err)
	}
	if err := ignoreProjectConfig(root); err != nil {
		return nil, err
	}
	merged.Source = path
	return merged, nil
}

// projectDocument parses root's configuration file, or starts an empty one.
func projectDocument(root string) (*yaml.Node, error) {
	data, err := ReadProject(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var document yaml.Node
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("parse %s: %w", ProjectConfigPath, err)
		}
	}
	if len(document.Content) == 0 {
		top := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: projectConfigHeader}
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{top}}
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", ProjectConfigPath)
	}
	return &document, nil
}

// ignoreProjectConfig keeps the configuration file out of Git with a
// .gitignore inside the metadata folder, leaving the project's own alone.
func ignoreProjectConfig(root string) error {
	path, err := storage.MetadataPath(root, ".kogin", ".gitignore")
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for line := range strings.SplitSeq(string(existing), "\n") {
		if strings.TrimSpace(line) == projectIgnore {
			return nil
		}
	}
	content := string(existing)
	if content == "" {
		content = "# Kogin: the project configuration may hold API keys.\n"
	} else if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return storage.WriteFile(path, []byte(content+projectIgnore+"\n"), 0o644)
}

func ensureMapping(node *yaml.Node, key string) *yaml.Node {
	if value := mappingValue(node, key); value != nil && value.Kind == yaml.MappingNode {
		return value
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMappingValue(node, key, value)
	return value
}

func setMappingValue(node *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			value.LineComment = node.Content[index+1].LineComment
			node.Content[index+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func scalarNode(value any) *yaml.Node {
	var node yaml.Node
	_ = node.Encode(value)
	return &node
}

// structNode renders a struct's set fields in declaration order under their
// yaml keys, leaving out empty strings and unset pointers.
func structNode(value any) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	reflected := reflect.ValueOf(value)
	for index, field := range reflect.VisibleFields(reflected.Type()) {
		key, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		item := reflected.Field(index)
		if key == "" || key == "-" || item.IsZero() {
			continue
		}
		if item.Kind() == reflect.Pointer {
			item = item.Elem()
		}
		setMappingValue(node, key, scalarNode(item.Interface()))
	}
	return node
}
