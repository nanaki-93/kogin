package config

import (
	"errors"
	"fmt"
	"slices"
)

type AgentRoles struct {
	Creation ModelScope `yaml:"creation"`
	Testing  ModelScope `yaml:"testing"`
	Review   ModelScope `yaml:"review"`
}

type AgentRuntimeConfig struct {
	Telemetry AgentTelemetry `yaml:"telemetry"`
	Roles     AgentRoles     `yaml:"roles"`
	Context   AgentContext   `yaml:"context"`
	// Skills is read only to refuse a list: project skills were removed with
	// file explanation. The empty list older example configs carry is fine.
	Skills             []string `yaml:"skills"`
	MaxModelCalls      int      `yaml:"max_model_calls"`
	MaxToolCalls       int      `yaml:"max_tool_calls"`
	MaxInputTokens     int64    `yaml:"max_input_tokens"`
	MaxOutputTokens    int64    `yaml:"max_output_tokens"`
	MaxElapsedSeconds  int      `yaml:"max_elapsed_seconds"`
	MaxConcurrentCalls int      `yaml:"max_concurrent_calls"`
	MaxRepairs         *int     `yaml:"max_repairs"`
}

// AgentLimits is immutable after admission. Token budgets use reported usage
// when available and explicit conservative estimates otherwise.
type AgentLimits struct {
	ModelCalls      int   `json:"model_calls"`
	ToolCalls       int   `json:"tool_calls"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ElapsedSeconds  int   `json:"elapsed_seconds"`
	ConcurrentCalls int   `json:"concurrent_calls"`
	Repairs         int   `json:"repairs"`
}

type AgentRuntime struct {
	// Passive reporting settings have their own version/destination identity;
	// they must not invalidate previously admitted execution preferences.
	Telemetry AgentTelemetry `json:"-"`
	Roles     AgentRoles
	Limits    AgentLimits
	Context   AgentContext
	// RetiredSkills is always empty (nil or [], as the config file had it).
	// It is kept, under its old name, only so the fingerprint of agent
	// settings saved before project skills were removed stays the same.
	RetiredSkills []string `json:"Skills"`
}

// ErrSkillsRemoved refuses a configured skill list.
var ErrSkillsRemoved = errors.New("agent_runtime.skills is no longer supported: project skills were removed with file explanation; delete the setting")

// AgentContext controls optional work beyond a normal completion. Provider
// caching needs an explicit retention duration; zero never selects remote TTL.
type AgentContext struct {
	Compaction         bool `yaml:"compaction"`
	TokenThreshold     int  `yaml:"token_threshold"`
	RetainEvents       int  `yaml:"retain_events"`
	SummaryMaxTokens   int  `yaml:"summary_max_tokens"`
	LocalCache         bool `yaml:"local_cache"`
	CacheTTLSeconds    int  `yaml:"cache_ttl_seconds"`
	ProviderCache      bool `yaml:"provider_cache"`
	ProviderTTLSeconds int  `yaml:"provider_ttl_seconds"`
}

func ResolveAgentContext(value AgentContext) (AgentContext, error) {
	for _, item := range []struct {
		field    *int
		fallback int
	}{
		{&value.TokenThreshold, 8000}, {&value.RetainEvents, 4},
		{&value.SummaryMaxTokens, 1024}, {&value.CacheTTLSeconds, 600},
	} {
		if *item.field == 0 {
			*item.field = item.fallback
		}
	}
	for _, item := range []struct {
		name                    string
		value, minimum, maximum int
	}{
		{"token_threshold", value.TokenThreshold, 512, 128000},
		{"retain_events", value.RetainEvents, 2, 64},
		{"summary_max_tokens", value.SummaryMaxTokens, 128, 8192},
		{"cache_ttl_seconds", value.CacheTTLSeconds, 60, 86400},
		{"provider_ttl_seconds", value.ProviderTTLSeconds, 0, 3600},
	} {
		if item.value < item.minimum || item.value > item.maximum {
			return value, fmt.Errorf("agent_runtime.context.%s must be between %d and %d", item.name, item.minimum, item.maximum)
		}
	}
	if value.SummaryMaxTokens >= value.TokenThreshold {
		return value, fmt.Errorf("agent_runtime.context.summary_max_tokens must be less than token_threshold")
	}
	if value.ProviderCache && value.ProviderTTLSeconds < 60 {
		return value, fmt.Errorf("agent_runtime.context.provider_cache requires an explicit provider_ttl_seconds between 60 and 3600")
	}
	return value, nil
}

func resolveAgentRoles(roles AgentRoles) (AgentRoles, error) {
	if roles.Creation == "" {
		roles.Creation = BugModelScope
	}
	if roles.Testing == "" {
		roles.Testing = CodeModelScope
	}
	if roles.Review == "" {
		roles.Review = AnalyzeModelScope
	}
	for _, scope := range []ModelScope{roles.Creation, roles.Testing, roles.Review} {
		if scope != AnalyzeModelScope && scope != BugModelScope && scope != CodeModelScope {
			return AgentRoles{}, fmt.Errorf("agent_runtime.roles must reference analyze, bug, or code profiles")
		}
	}
	return roles, nil
}

func ResolveAgentRuntime(configured AgentRuntimeConfig) (AgentRuntime, error) {
	roles, err := resolveAgentRoles(configured.Roles)
	if err != nil {
		return AgentRuntime{}, err
	}
	limits := AgentLimits{ModelCalls: configured.MaxModelCalls, ToolCalls: configured.MaxToolCalls, InputTokens: configured.MaxInputTokens, OutputTokens: configured.MaxOutputTokens, ElapsedSeconds: configured.MaxElapsedSeconds, ConcurrentCalls: configured.MaxConcurrentCalls, Repairs: 2}
	for _, item := range []struct {
		value    *int
		fallback int
	}{
		{&limits.ModelCalls, 1024}, {&limits.ToolCalls, 4096}, {&limits.ElapsedSeconds, 86400}, {&limits.ConcurrentCalls, 4},
	} {
		if *item.value == 0 {
			*item.value = item.fallback
		}
	}
	if limits.InputTokens == 0 {
		limits.InputTokens = 128 << 20
	}
	if limits.OutputTokens == 0 {
		limits.OutputTokens = 64 << 20
	}
	if configured.MaxRepairs != nil {
		limits.Repairs = *configured.MaxRepairs
	}
	if err := limits.Validate(); err != nil {
		return AgentRuntime{}, err
	}
	context, err := ResolveAgentContext(configured.Context)
	if err != nil {
		return AgentRuntime{}, err
	}
	if len(configured.Skills) > 0 {
		return AgentRuntime{}, ErrSkillsRemoved
	}
	telemetry, err := ResolveAgentTelemetry(configured.Telemetry)
	if err != nil {
		return AgentRuntime{}, err
	}
	return AgentRuntime{Roles: roles, Limits: limits, Context: context, RetiredSkills: slices.Clone(configured.Skills), Telemetry: telemetry}, nil
}

func (limits AgentLimits) Validate() error {
	for _, item := range []struct {
		name                    string
		value, minimum, maximum int64
	}{
		{"max_model_calls", int64(limits.ModelCalls), 1, 4096}, {"max_tool_calls", int64(limits.ToolCalls), 1, 16384},
		{"max_input_tokens", limits.InputTokens, 1, 1 << 40}, {"max_output_tokens", limits.OutputTokens, 1, 1 << 40},
		{"max_elapsed_seconds", int64(limits.ElapsedSeconds), 1, 604800}, {"max_concurrent_calls", int64(limits.ConcurrentCalls), 1, 32},
		{"max_repairs", int64(limits.Repairs), 0, 2},
	} {
		if item.value < item.minimum || item.value > item.maximum {
			return fmt.Errorf("agent_runtime.%s must be between %d and %d", item.name, item.minimum, item.maximum)
		}
	}
	return nil
}
