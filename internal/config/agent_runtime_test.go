package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestASkillListIsRefusedNowThatSkillsAreGone(t *testing.T) {
	for _, data := range []string{"agent_runtime:\n  skills: []\n", "agent_runtime:\n  max_model_calls: 10\n"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveAgentRuntime(cfg.AgentRuntime); err != nil {
			t.Fatalf("%q: %v", data, err)
		}
	}
	data := []byte("agent_runtime:\n  skills:\n    - project-skills/review-code\n")
	if err := rejectUnknownFields(data); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAgentRuntime(cfg.AgentRuntime); !errors.Is(err, ErrSkillsRemoved) {
		t.Fatalf("a skill list was accepted: %v", err)
	}
}

// The settings fingerprint hashes the runtime as JSON: it keeps the "Skills"
// key the runtime had, so settings saved before skills were removed still
// match their configuration.
func TestTheRuntimeKeepsTheSkillsKeyOfEarlierFingerprints(t *testing.T) {
	for config, want := range map[string]string{"agent_runtime: {}\n": `"Skills":null`, "agent_runtime:\n  skills: []\n": `"Skills":[]`} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(config), &cfg); err != nil {
			t.Fatal(err)
		}
		resolved, err := ResolveAgentRuntime(cfg.AgentRuntime)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(resolved)
		if err != nil || !strings.HasSuffix(string(data), want+"}") {
			t.Fatalf("%q: %s %v", config, data, err)
		}
	}
}

func TestAgentRuntimeDefaultsAndStrictLimits(t *testing.T) {
	resolved, err := ResolveAgentRuntime(AgentRuntimeConfig{})
	if err != nil || resolved.Roles != (AgentRoles{BugModelScope, CodeModelScope, AnalyzeModelScope}) || resolved.Limits.ModelCalls != 1024 || resolved.Limits.Repairs != 2 {
		t.Fatalf("defaults=%+v error=%v", resolved, err)
	}
	for _, mutate := range []func(*AgentRuntimeConfig){
		func(c *AgentRuntimeConfig) { c.Roles.Creation = "unknown" },
		func(c *AgentRuntimeConfig) { c.MaxModelCalls = -1 },
		func(c *AgentRuntimeConfig) { c.MaxToolCalls = 16385 },
		func(c *AgentRuntimeConfig) { c.MaxInputTokens = -1 },
		func(c *AgentRuntimeConfig) { c.MaxOutputTokens = 1<<40 + 1 },
		func(c *AgentRuntimeConfig) { c.MaxElapsedSeconds = 604801 },
		func(c *AgentRuntimeConfig) { c.MaxConcurrentCalls = 33 },
		func(c *AgentRuntimeConfig) { value := 3; c.MaxRepairs = &value },
	} {
		cfg := Default()
		mutate(&cfg.AgentRuntime)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg.AgentRuntime)
		}
	}
}

func TestAgentRuntimeYAMLPreservesDisabledRepairs(t *testing.T) {
	data := []byte("agent_runtime:\n  roles:\n    creation: code\n  max_model_calls: 7\n  max_repairs: 0\n")
	if err := rejectUnknownFields(data); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := ResolveAgentRuntime(cfg.AgentRuntime)
	if err != nil || runtime.Limits.Repairs != 0 || runtime.Limits.ModelCalls != 7 || runtime.Roles.Creation != CodeModelScope {
		t.Fatalf("runtime=%+v error=%v", runtime, err)
	}
	if err := rejectUnknownFields([]byte("agent_runtime:\n  roles:\n    cretaion: bug\n")); err == nil {
		t.Fatal("unknown role key accepted")
	}
}

func TestAgentContextRequiresExplicitRetentionAndBoundedCompaction(t *testing.T) {
	value, err := ResolveAgentContext(AgentContext{})
	if err != nil || value.Compaction || value.LocalCache || value.ProviderCache || value.TokenThreshold != 8000 || value.RetainEvents != 4 || value.SummaryMaxTokens != 1024 {
		t.Fatalf("defaults=%+v %v", value, err)
	}
	for _, input := range []AgentContext{
		{TokenThreshold: -1}, {RetainEvents: 1}, {SummaryMaxTokens: 8193}, {CacheTTLSeconds: 59},
		{ProviderCache: true}, {ProviderTTLSeconds: 3601}, {TokenThreshold: 1024},
	} {
		if _, err := ResolveAgentContext(input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	data := []byte("agent_runtime:\n  context:\n    compaction: true\n    local_cache: true\n    provider_cache: true\n    provider_ttl_seconds: 600\n")
	if err := rejectUnknownFields(data); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveAgentRuntime(cfg.AgentRuntime)
	if err != nil || !resolved.Context.Compaction || !resolved.Context.LocalCache || !resolved.Context.ProviderCache || resolved.Context.ProviderTTLSeconds != 600 {
		t.Fatalf("configuration=%+v %v", resolved, err)
	}
	if err := rejectUnknownFields([]byte("agent_runtime:\n  context:\n    ttl: 10\n")); err == nil {
		t.Fatal("unknown context option accepted")
	}
}

func TestRetiredFeatureKeysAreRejected(t *testing.T) {
	for _, data := range []string{
		"agent_runtime:\n  live_model: gemini-3.8-live\n",
		"agent_runtime:\n  triggers: {}\n",
		"integrations:\n  mcp: []\n",
	} {
		if err := rejectUnknownFields([]byte(data)); err == nil {
			t.Fatalf("accepted retired configuration %q", data)
		}
	}
}
