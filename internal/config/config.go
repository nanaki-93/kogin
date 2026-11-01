package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/logging"
	"gopkg.in/yaml.v3"
)

// Config is the daemon's single YAML configuration schema.
type Config struct {
	AgentRuntime AgentRuntimeConfig `yaml:"agent_runtime"`
	ModelScopes  ModelScopesConfig  `yaml:"model_scopes"`
	Retry        RetryConfig        `yaml:"retry"`
	Timeouts     TimeoutConfig      `yaml:"timeouts"`
	Logging      LoggingConfig      `yaml:"logging"`
	ProjectPath  string             `yaml:"project_path"`
	// Source is the file the configuration was read from; empty for the
	// built-in defaults.
	Source string `yaml:"-"`
}

// LoggingConfig holds configuration for the logger.
type LoggingConfig struct {
	Level         string   `yaml:"level"`
	Format        string   `yaml:"format"`
	Filename      string   `yaml:"filename"`
	SensitiveKeys []string `yaml:"sensitive_keys"`
}

// ModelScopesConfig contains the three fixed model profiles used by the daemon.
type ModelScopesConfig struct {
	Analyze ModelProfileConfig `yaml:"analyze"`
	Bug     ModelProfileConfig `yaml:"bug"`
	Code    ModelProfileConfig `yaml:"code"`
}

// ModelProfileConfig is the configured portion of a fixed scope. Pointer
// numeric fields preserve the difference between an omitted value and zero.
type ModelProfileConfig struct {
	Provider         ModelProvider `yaml:"provider"`
	CLIPath          string        `yaml:"cli_path"`
	APIBaseURL       string        `yaml:"api_base_url"`
	APIKey           string        `yaml:"api_key"`
	AccessToken      string        `yaml:"access_token"`
	VertexProject    string        `yaml:"vertex_project"`
	VertexLocation   string        `yaml:"vertex_location"`
	Model            string        `yaml:"model"`
	ReasoningEffort  string        `yaml:"reasoning_effort"`
	Temperature      *float32      `yaml:"temperature"`
	TopP             *float32      `yaml:"top_p"`
	TopK             *int          `yaml:"top_k"`
	MinP             *float32      `yaml:"min_p"`
	PresencePenalty  *float32      `yaml:"presence_penalty"`
	RepeatPenalty    *float32      `yaml:"repeat_penalty"`
	MaxTokens        *int          `yaml:"max_tokens"`
	ContextMaxTokens *int          `yaml:"context_max_tokens"`
}

// RetryConfig holds retry-related configuration.
type RetryConfig struct {
	MaxRetries  int `yaml:"max_retries"`
	BackoffBase int `yaml:"backoff_base"`
	BackoffMax  int `yaml:"backoff_max"`
}

// TimeoutConfig sets operation deadlines independently of HTTP server timeouts.
// Zero values use conservative local-daemon defaults.
type TimeoutConfig struct {
	ImportSeconds        int `yaml:"import_seconds" json:"import_seconds"`
	AnalysisSeconds      int `yaml:"analysis_seconds" json:"analysis_seconds"`
	GenerationSeconds    int `yaml:"generation_seconds" json:"generation_seconds"`
	WorkflowModelSeconds int `yaml:"workflow_model_seconds" json:"workflow_model_seconds"`
	FocusedCheckSeconds  int `yaml:"focused_check_seconds" json:"focused_check_seconds"`
}

// MaxTimeoutSeconds bounds a time limit set from the app: one day.
const MaxTimeoutSeconds = 86400

// ValidateSet checks time limits set from the app, where each one is explicit:
// from one second to MaxTimeoutSeconds.
func (t TimeoutConfig) ValidateSet() error {
	for _, item := range []struct {
		key   string
		value int
	}{
		{"import_seconds", t.ImportSeconds}, {"analysis_seconds", t.AnalysisSeconds}, {"generation_seconds", t.GenerationSeconds},
		{"workflow_model_seconds", t.WorkflowModelSeconds}, {"focused_check_seconds", t.FocusedCheckSeconds},
	} {
		if item.value < 1 || item.value > MaxTimeoutSeconds {
			return fmt.Errorf("timeouts.%s must be between 1 and %d seconds", item.key, MaxTimeoutSeconds)
		}
	}
	return nil
}

const DefaultWorkflowModelSeconds = 900

func (c *Config) applyDefaults() {
	if c.Retry.MaxRetries == 0 {
		c.Retry.MaxRetries = 3
	}
	if c.Retry.BackoffBase == 0 {
		c.Retry.BackoffBase = 1000
	}
	if c.Retry.BackoffMax == 0 {
		c.Retry.BackoffMax = 30000
	}
	if c.Timeouts.ImportSeconds == 0 {
		c.Timeouts.ImportSeconds = 300
	}
	if c.Timeouts.AnalysisSeconds == 0 {
		c.Timeouts.AnalysisSeconds = 300
	}
	if c.Timeouts.GenerationSeconds == 0 {
		c.Timeouts.GenerationSeconds = 300
	}
	if c.Timeouts.WorkflowModelSeconds == 0 {
		c.Timeouts.WorkflowModelSeconds = DefaultWorkflowModelSeconds
	}
	if c.Timeouts.FocusedCheckSeconds == 0 {
		c.Timeouts.FocusedCheckSeconds = 60
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.ProjectPath == "" {
		if workingDirectory, err := os.Getwd(); err == nil {
			c.ProjectPath = workingDirectory
		} else {
			c.ProjectPath = "."
		}
	}
}

// LoadConfig reads the first daemon configuration file Find locates, or
// returns the built-in local defaults when there is none. Source records
// which file was read.
func LoadConfig() (*Config, error) {
	executable, _ := os.Executable()
	path := Find(executable)
	if path == "" {
		logging.Warn("No config.yaml found; using built-in local model defaults until a project configures its own", "searched", Candidates(executable))
		return Default(), nil
	}
	cfg, err := LoadFromYAML(path)
	if err != nil {
		return nil, err
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	cfg.Source = path
	logging.Info("Loaded config", "path", path)
	return cfg, nil
}

// LoadFromYAML reads the only supported configuration format. Unknown keys are
// rejected before a startup can silently accept retired configuration.
func LoadFromYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found: %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}
	if err := rejectUnknownFields(data); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse config file: configuration must contain one YAML document")
		}
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that the configured scopes are complete and safe before any
// provider request can be constructed.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("configuration is required")
	}
	if err := c.validateRetryAndTimeouts(); err != nil {
		return err
	}
	if _, err := ResolveModelProfiles(c); err != nil {
		return err
	}
	_, err := ResolveAgentRuntime(c.AgentRuntime)
	return err
}

func (c *Config) validateRetryAndTimeouts() error {
	if c.Retry.MaxRetries < 0 || c.Retry.MaxRetries > 10 || c.Retry.BackoffBase < 0 || c.Retry.BackoffMax < 0 || (c.Retry.BackoffMax > 0 && c.Retry.BackoffBase > c.Retry.BackoffMax) {
		return fmt.Errorf("retry configuration must use non-negative backoff and 0..10 max_retries")
	}
	if c.Timeouts.ImportSeconds < 0 || c.Timeouts.AnalysisSeconds < 0 || c.Timeouts.GenerationSeconds < 0 || c.Timeouts.WorkflowModelSeconds < 0 || c.Timeouts.FocusedCheckSeconds < 0 {
		return fmt.Errorf("timeouts must be non-negative")
	}
	return nil
}

// configurationFields mirrors Config's yaml tags, so an unknown key is reported
// with its full path before strict decoding runs.
var configurationFields = yamlFields(reflect.TypeFor[Config]())

// yamlFields lists the yaml keys of a struct type; nested structs list theirs.
func yamlFields(structType reflect.Type) fieldSet {
	fields := fieldSet{}
	for field := range structType.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Kind() == reflect.Struct {
			fields[name] = yamlFields(fieldType)
		} else {
			fields[name] = nil
		}
	}
	return fields
}

type fieldSet map[string]fieldSet

func rejectUnknownFields(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) == 0 {
		return nil
	}
	return rejectUnknownMapping(document.Content[0], "", configurationFields)
}

func rejectUnknownMapping(node *yaml.Node, prefix string, fields fieldSet) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index].Value
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		children, known := fields[key]
		if !known {
			return fmt.Errorf("unsupported configuration field %q", path)
		}
		if len(children) > 0 {
			if err := rejectUnknownMapping(node.Content[index+1], path, children); err != nil {
				return err
			}
		}
	}
	return nil
}

func missingScopeError(scope ModelScope) error {
	return fmt.Errorf("model_scopes.%s is required", scope)
}

func missingFieldError(scope ModelScope, field string) error {
	return fmt.Errorf("model_scopes.%s.%s is required", scope, strings.TrimSpace(field))
}
