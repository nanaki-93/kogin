package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func globalTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Default()
	cfg.ModelScopes.Analyze = ModelProfileConfig{APIBaseURL: "https://api.example/v1", APIKey: "global-key", Model: "global-analyze"}
	cfg.Timeouts.ImportSeconds = 111
	cfg.Logging.Level = "debug"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMergeProjectReplacesNamedScopesAndKeys(t *testing.T) {
	global := globalTestConfig(t)
	merged, err := MergeProject(global, []byte("model_scopes:\n  analyze:\n    provider: agy\n    model: gemini-pro\ntimeouts:\n  analysis_seconds: 42\n"))
	if err != nil {
		t.Fatal(err)
	}
	analyze := merged.ModelScopes.Analyze
	if analyze.Provider != AgyProvider || analyze.Model != "gemini-pro" || analyze.APIKey != "" || analyze.APIBaseURL != "" {
		t.Fatalf("a named scope must replace the daemon's whole: %+v", analyze)
	}
	if merged.ModelScopes.Bug != global.ModelScopes.Bug || merged.Timeouts.AnalysisSeconds != 42 || merged.Timeouts.ImportSeconds != 111 || merged.Logging.Level != "debug" {
		t.Fatalf("unnamed keys must keep the daemon's values: %+v", merged)
	}
	if global.ModelScopes.Analyze.Model != "global-analyze" || global.Timeouts.AnalysisSeconds == 42 {
		t.Fatal("merging changed the daemon's configuration")
	}
	for document, want := range map[string]string{
		"logging:\n  level: info\n":                   `"logging" configures the daemon`,
		"project_path: /elsewhere\n":                  `"project_path" configures the daemon`,
		"model_scopes:\n  analyze:\n    colour: 1\n":  "model_scopes.analyze.colour",
		"model_scopes:\n  analyze:\n    model: x\n":   "api_base_url",
		"- just\n- a list\n":                          "must be a mapping",
		"timeouts:\n  import_seconds: 1\n---\nx: 1\n": "one YAML document",
	} {
		if _, err := MergeProject(global, []byte(document)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", document, err)
		}
	}
}

func TestUpdateProjectWritesAPrivateIgnoredFileAndKeepsOtherKeys(t *testing.T) {
	root := t.TempDir()
	global := globalTestConfig(t)
	if cfg, found, err := LoadProject(root, global); err != nil || found || cfg != global {
		t.Fatalf("no project file: %v %v", found, err)
	}
	path := filepath.Join(root, ".kasuri", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# my notes\ntimeouts:\n  analysis_seconds: 42 # slow laptop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	two := 2
	update := ProjectUpdate{
		ModelScopes: map[ModelScope]ModelProfileConfig{
			AnalyzeModelScope: {Provider: AgyProvider, Model: "gemini-pro", ReasoningEffort: "medium"},
			BugModelScope:     {APIBaseURL: "http://localhost:1234/v1", Model: "qwen", MaxTokens: &two},
			CodeModelScope:    {Provider: AgyProvider, Model: "gemini-flash"},
		},
		Roles:  &AgentRoles{Creation: CodeModelScope, Testing: BugModelScope, Review: AnalyzeModelScope},
		Limits: &AgentLimits{ModelCalls: 20, ToolCalls: 40, InputTokens: 1000, OutputTokens: 500, ElapsedSeconds: 600, ConcurrentCalls: 2, Repairs: 1},
	}
	merged, err := UpdateProject(root, global, update)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Source != path || merged.ModelScopes.Bug.Model != "qwen" || merged.AgentRuntime.MaxModelCalls != 20 || merged.Timeouts.AnalysisSeconds != 42 {
		t.Fatalf("merged: %+v", merged)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# my notes", "analysis_seconds: 42 # slow laptop", "provider: agy", "model: gemini-pro", "max_tokens: 2", "creation: code", "max_model_calls: 20", "max_repairs: 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "api_key") || strings.Contains(text, "cli_path") {
		t.Errorf("unset fields were written:\n%s", text)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info.Mode(), err)
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".kasuri", ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), "config.yaml") {
		t.Fatalf(".gitignore: %q %v", ignore, err)
	}
	if _, err := UpdateProject(root, global, update); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(root, ".kasuri", ".gitignore")); strings.Count(string(again), "config.yaml") != 1 {
		t.Fatalf("the ignore entry was repeated: %q", again)
	}
	loaded, found, err := LoadProject(root, global)
	if err != nil || !found || loaded.ModelScopes.Code.Model != "gemini-flash" {
		t.Fatalf("reload: %+v %v %v", loaded, found, err)
	}

	invalid := ProjectUpdate{ModelScopes: map[ModelScope]ModelProfileConfig{AnalyzeModelScope: {Model: "no-address"}}}
	if _, err := UpdateProject(root, global, invalid); err == nil {
		t.Fatal("an invalid configuration was written")
	}
	if after, _ := os.ReadFile(path); string(after) != string(data) {
		t.Fatal("a refused update changed the file")
	}
}

func TestANewProjectFileStartsWithItsHeader(t *testing.T) {
	root := t.TempDir()
	if _, err := UpdateProject(root, globalTestConfig(t), ProjectUpdate{Roles: &AgentRoles{Creation: BugModelScope, Testing: CodeModelScope, Review: AnalyzeModelScope}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".kasuri", "config.yaml"))
	if err != nil || !strings.HasPrefix(string(data), "# Kasuri configuration for this project.") {
		t.Fatalf("header: %q %v", data, err)
	}
}
