package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

// Project setup writes the project's own configuration file
// (config.ProjectConfigPath): its three model scopes, the scope each agent
// role uses, the per-run budget and the time limits. It is offered the first time a project
// without that file opens; skipping it keeps the daemon's configuration.

// Setup states.
const (
	SetupNeeded     = "needed"     // No project configuration and setup not skipped.
	SetupSkipped    = "skipped"    // The daemon's configuration was kept on purpose.
	SetupConfigured = "configured" // The project's configuration is applied.
	SetupInvalid    = "invalid"    // The project's file exists but could not be applied.
)

const setupSkipPath = ".kasuri/setup.json"

// SetupModel is one model scope as setup shows and edits it. Secrets are
// never returned: APIKeySet says whether one is configured, and an empty
// APIKey in a save keeps it.
type SetupModel struct {
	Provider         string   `json:"provider"`
	CLIPath          string   `json:"cli_path,omitempty"`
	APIBaseURL       string   `json:"api_base_url,omitempty"`
	APIKey           string   `json:"api_key,omitempty"`
	APIKeySet        bool     `json:"api_key_set"`
	ClearAPIKey      bool     `json:"clear_api_key,omitempty"`
	VertexProject    string   `json:"vertex_project,omitempty"`
	VertexLocation   string   `json:"vertex_location,omitempty"`
	Model            string   `json:"model"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	Temperature      *float32 `json:"temperature,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	ContextMaxTokens *int     `json:"context_max_tokens,omitempty"`
}

// SetupRoles names the model scope of each agent role.
type SetupRoles struct {
	Creation string `json:"creation"`
	Testing  string `json:"testing"`
	Review   string `json:"review"`
}

// ProjectSetup is the project's configuration as setup edits it.
type ProjectSetup struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	State           string `json:"state"`
	// ConfigFile is where the project's configuration is (or will be) saved;
	// DaemonConfigFile is the daemon's own, empty for the built-in defaults.
	ConfigFile       string                `json:"config_file"`
	DaemonConfigFile string                `json:"daemon_config_file"`
	Error            string                `json:"error,omitempty"`
	Models           map[string]SetupModel `json:"models"`
	Roles            SetupRoles            `json:"roles"`
	Limits           config.AgentLimits    `json:"limits"`
	// Timeouts are the time limits in effect, defaults included.
	Timeouts config.TimeoutConfig `json:"timeouts"`
	// OverridesReset says saving replaced agent overrides from Settings.
	OverridesReset bool `json:"overrides_reset,omitempty"`
}

// ProjectSetupRequest saves a project's configuration.
type ProjectSetupRequest struct {
	ProjectID       string                `json:"project_id"`
	ProjectRevision string                `json:"project_revision"`
	Models          map[string]SetupModel `json:"models"`
	Roles           SetupRoles            `json:"roles"`
	Limits          config.AgentLimits    `json:"limits"`
	// Timeouts, when given, sets every time limit.
	Timeouts *config.TimeoutConfig `json:"timeouts,omitempty"`
}

// ProjectTimeoutsRequest saves only the project's time limits.
type ProjectTimeoutsRequest struct {
	ProjectID       string               `json:"project_id"`
	ProjectRevision string               `json:"project_revision"`
	Timeouts        config.TimeoutConfig `json:"timeouts"`
}

// ErrSetupBusy reports running work a configuration change would cut short.
var ErrSetupBusy = errors.New("work is running")

// ErrSetupInvalid reports a configuration that cannot be saved; its message
// names the field and the reason.
var ErrSetupInvalid = errors.New("invalid project configuration")

// ProjectSetup reports the active project's configuration for setup. The
// values are what the project runs with now: its own file, or the daemon's
// configuration as the starting point.
func (s *Service) ProjectSetup(ctx context.Context, id, revision string) (*ProjectSetup, error) {
	if err := s.requireActiveRevision(id, revision); err != nil {
		return nil, err
	}
	root := s.manager.Root()
	file, err := config.ProjectFile(root)
	if err != nil {
		return nil, err
	}
	current := s.conf()
	setup := &ProjectSetup{ProjectID: id, ProjectRevision: revision, ConfigFile: file, Error: current.projectError}
	if s.global != nil {
		setup.DaemonConfigFile = s.global.Source
	}
	switch {
	case current.projectError != "":
		setup.State = SetupInvalid
	case current.projectFile != "":
		setup.State = SetupConfigured
	case setupSkipped(root):
		setup.State = SetupSkipped
	default:
		setup.State = SetupNeeded
	}
	cfg := current.config
	if cfg == nil {
		cfg = config.Default()
	}
	setup.Models = map[string]SetupModel{}
	for _, scope := range []config.ModelScope{config.AnalyzeModelScope, config.BugModelScope, config.CodeModelScope} {
		setup.Models[string(scope)] = setupModel(cfg.ModelScopes.Profile(scope))
	}
	roles := current.agentRuntime.Roles
	setup.Roles = SetupRoles{Creation: string(roles.Creation), Testing: string(roles.Testing), Review: string(roles.Review)}
	setup.Limits = current.agentRuntime.Limits
	setup.Timeouts = effectiveTimeouts(cfg.Timeouts)
	return setup, ctx.Err()
}

// effectiveTimeouts fills unset time limits with the defaults the service uses.
func effectiveTimeouts(timeouts config.TimeoutConfig) config.TimeoutConfig {
	seconds := func(value int, fallback time.Duration) int {
		return int(configuredDuration(value, fallback) / time.Second)
	}
	return config.TimeoutConfig{
		ImportSeconds: seconds(timeouts.ImportSeconds, defaultModelTimeout), AnalysisSeconds: seconds(timeouts.AnalysisSeconds, defaultModelTimeout),
		GenerationSeconds: seconds(timeouts.GenerationSeconds, defaultModelTimeout), WorkflowModelSeconds: seconds(timeouts.WorkflowModelSeconds, config.DefaultWorkflowModelSeconds*time.Second),
		FocusedCheckSeconds: seconds(timeouts.FocusedCheckSeconds, defaultFocusedCheckTimeout),
	}
}

// SaveProjectSetup writes the project's configuration and applies it. It is
// refused while work runs: that work was admitted with the old models.
func (s *Service) SaveProjectSetup(ctx context.Context, request ProjectSetupRequest) (*ProjectSetup, error) {
	s.jobLifecycleMu.Lock()
	err := s.applyProjectUpdate(request.ProjectID, request.ProjectRevision, func() (config.ProjectUpdate, error) { return s.setupUpdate(request) })
	if err != nil {
		s.jobLifecycleMu.Unlock()
		return nil, err
	}
	reset, err := s.resetAgentOverrides(ctx, s.manager.Root())
	s.jobLifecycleMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("the configuration was saved, but agent overrides could not be reset: %w", err)
	}
	setup, err := s.ProjectSetup(ctx, request.ProjectID, request.ProjectRevision)
	if setup != nil {
		setup.OverridesReset = reset
	}
	return setup, err
}

// SaveProjectTimeouts writes only the project's time limits and applies them.
// Agent overrides stay: time limits are not part of what they were made for.
func (s *Service) SaveProjectTimeouts(ctx context.Context, request ProjectTimeoutsRequest) (*ProjectSetup, error) {
	s.jobLifecycleMu.Lock()
	err := s.applyProjectUpdate(request.ProjectID, request.ProjectRevision, func() (config.ProjectUpdate, error) {
		timeouts := request.Timeouts
		return config.ProjectUpdate{Timeouts: &timeouts}, timeouts.ValidateSet()
	})
	s.jobLifecycleMu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.ProjectSetup(ctx, request.ProjectID, request.ProjectRevision)
}

// applyProjectUpdate writes an update to the active project's configuration
// file and applies it at once. The caller holds jobLifecycleMu. It is refused
// while work runs, since that work was admitted with the old configuration.
func (s *Service) applyProjectUpdate(id, revision string, build func() (config.ProjectUpdate, error)) error {
	if err := s.requireActiveRevision(id, revision); err != nil {
		return err
	}
	if busy := s.activeWork(); len(busy) > 0 {
		return fmt.Errorf("%w: %s; stop it or wait for it to finish, then save again", ErrSetupBusy, strings.Join(busy, ", "))
	}
	root := s.manager.Root()
	update, err := build()
	if err == nil {
		_, err = config.UpdateProject(root, s.global, update)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSetupInvalid, err)
	}
	if err := s.closeWorktreeServices(); err != nil {
		return err
	}
	s.loadProjectConfiguration(root)
	return nil
}

// resetAgentOverrides clears the agent overrides saved in Settings → Models &
// agents: setup has just set the roles and budget they deviated from, and
// overrides from the old models would otherwise hold up all agent work.
func (s *Service) resetAgentOverrides(ctx context.Context, root string) (bool, error) {
	store, binding, err := s.agentJournal(root)
	if err != nil {
		return false, err
	}
	record, err := store.Settings(ctx)
	if err != nil || record.Values == nil {
		return false, err
	}
	if _, err := store.SaveSettings(ctx, record.Version, binding.Configuration, nil); err != nil {
		return false, err
	}
	s.notifyEvent("agent_settings")
	return true, nil
}

// withConfiguredAgentSettings admits work under the configuration's own agent
// settings, not the project's overrides. A connection test must work while
// overrides wait for a reset.
func (s *Service) withConfiguredAgentSettings(ctx context.Context, root string) (context.Context, error) {
	canonical, err := project.CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	_, binding, err := s.agentJournal(canonical)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, agentSettingsKey{}, admittedAgentSettings{root: canonical, runtime: s.conf().agentRuntime, configuration: binding.Configuration}), nil
}

// SkipProjectSetup keeps the daemon's configuration for the active project
// and stops offering setup for it.
func (s *Service) SkipProjectSetup(ctx context.Context, id, revision string) (*ProjectSetup, error) {
	if err := s.requireActiveRevision(id, revision); err != nil {
		return nil, err
	}
	path, err := storage.MetadataPath(s.manager.Root(), setupSkipPath)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(map[string]time.Time{"skipped_at": time.Now().UTC()})
	if err == nil {
		err = storage.WriteFile(path, data, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("record skipped setup: %w", err)
	}
	s.notifyEvent("configuration")
	return s.ProjectSetup(ctx, id, revision)
}

func setupSkipped(root string) bool {
	path, err := storage.MetadataPath(root, setupSkipPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (s *Service) requireActiveRevision(id, revision string) error {
	index, err := s.manager.Index()
	if err != nil {
		return err
	}
	if id != index.ProjectID || revision != index.ProjectRevision {
		return project.ErrRevisionConflict
	}
	return nil
}

// setupUpdate turns a setup request into the file update, keeping configured
// secrets the request leaves empty.
func (s *Service) setupUpdate(request ProjectSetupRequest) (config.ProjectUpdate, error) {
	current := s.conf().config
	if current == nil {
		current = config.Default()
	}
	update := config.ProjectUpdate{ModelScopes: map[config.ModelScope]config.ModelProfileConfig{}}
	for _, scope := range []config.ModelScope{config.AnalyzeModelScope, config.BugModelScope, config.CodeModelScope} {
		model, ok := request.Models[string(scope)]
		if !ok {
			return update, fmt.Errorf("setup requires the %s model", scope)
		}
		update.ModelScopes[scope] = model.profile(current.ModelScopes.Profile(scope))
	}
	roles := config.AgentRoles{Creation: config.ModelScope(request.Roles.Creation), Testing: config.ModelScope(request.Roles.Testing), Review: config.ModelScope(request.Roles.Review)}
	limits := request.Limits
	if err := limits.Validate(); err != nil {
		return update, err
	}
	update.Roles, update.Limits = &roles, &limits
	if request.Timeouts != nil {
		timeouts := *request.Timeouts
		if err := timeouts.ValidateSet(); err != nil {
			return update, err
		}
		update.Timeouts = &timeouts
	}
	return update, nil
}

func setupModel(profile config.ModelProfileConfig) SetupModel {
	return SetupModel{
		Provider: string(profile.Provider), CLIPath: profile.CLIPath, APIBaseURL: profile.APIBaseURL,
		APIKeySet: profile.APIKey != "", VertexProject: profile.VertexProject, VertexLocation: profile.VertexLocation,
		Model: profile.Model, ReasoningEffort: profile.ReasoningEffort, Temperature: profile.Temperature,
		MaxTokens: profile.MaxTokens, ContextMaxTokens: profile.ContextMaxTokens,
	}
}

// profile is the scope's new configuration; secrets come from current unless
// the request sets or clears them, and only for the same provider.
func (model SetupModel) profile(current config.ModelProfileConfig) config.ModelProfileConfig {
	profile := config.ModelProfileConfig{
		Provider: config.ModelProvider(model.Provider), CLIPath: strings.TrimSpace(model.CLIPath), APIBaseURL: strings.TrimSpace(model.APIBaseURL),
		APIKey: model.APIKey, VertexProject: strings.TrimSpace(model.VertexProject), VertexLocation: strings.TrimSpace(model.VertexLocation),
		Model: strings.TrimSpace(model.Model), ReasoningEffort: model.ReasoningEffort, Temperature: model.Temperature,
		MaxTokens: model.MaxTokens, ContextMaxTokens: model.ContextMaxTokens,
	}
	if current.Provider == profile.Provider {
		if profile.APIKey == "" && !model.ClearAPIKey {
			profile.APIKey = current.APIKey
		}
		profile.AccessToken = current.AccessToken
		// Sampling settings setup does not show are kept.
		profile.TopP, profile.TopK, profile.MinP = current.TopP, current.TopK, current.MinP
		profile.PresencePenalty, profile.RepeatPenalty = current.PresencePenalty, current.RepeatPenalty
	}
	return profile
}

// activeWork names the running work a configuration change would cut short.
func (s *Service) activeWork() []string {
	var busy []string
	s.analysisRun.mu.Lock()
	if s.analysisRun.cancel != nil {
		busy = append(busy, "an analysis is running")
	}
	s.analysisRun.mu.Unlock()
	s.changeWorkflow.mu.Lock()
	for _, job := range s.changeWorkflow.allJobs() {
		if !job.finished {
			busy = append(busy, "agents are working on a task")
			break
		}
	}
	s.changeWorkflow.mu.Unlock()
	s.worktreesMu.Lock()
	children := make([]*Service, 0, len(s.worktrees))
	for _, child := range s.worktrees {
		children = append(children, child)
	}
	s.worktreesMu.Unlock()
	for _, child := range children {
		if len(child.activeWork()) > 0 {
			busy = append(busy, "work is running in a worktree")
			break
		}
	}
	return busy
}

// SetupModelTest is the outcome of a model connection test.
type SetupModelTest struct {
	OK        bool   `json:"ok"`
	Model     string `json:"model,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

const setupTestTimeout = 90 * time.Second

// TestSetupModel sends one short request to a model as setup would configure
// it for scope, recorded as an agent run like any other model use. A
// configuration that cannot be valid is an error; a failed request is a
// result that says why.
func (s *Service) TestSetupModel(ctx context.Context, id, revision, scope string, model SetupModel) (*SetupModelTest, error) {
	if err := s.requireActiveRevision(id, revision); err != nil {
		return nil, err
	}
	modelScope := config.ModelScope(scope)
	if modelScope != config.AnalyzeModelScope && modelScope != config.BugModelScope && modelScope != config.CodeModelScope {
		return nil, fmt.Errorf("%w: scope must be analyze, bug or code", ErrSetupInvalid)
	}
	current := s.conf().config
	if current == nil {
		current = config.Default()
	}
	profile, err := config.ResolveModelProfile(modelScope, model.profile(current.ModelScopes.Profile(modelScope)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSetupInvalid, err)
	}
	runtime := newModelRuntime(profile, setupTestTimeout, 0)
	timed, cancelTimeout := context.WithTimeout(ctx, setupTestTimeout)
	defer cancelTimeout()
	configured, err := s.withConfiguredAgentSettings(timed, s.manager.Root())
	if err != nil {
		return nil, err
	}
	admitted, release, err := s.agentRunContext(configured, runtime)
	if err != nil {
		return nil, err
	}
	defer release()
	started := time.Now()
	result, err := runtime.execute(admitted, agentruntime.Request{Messages: []llm.ChatMessage{
		{Role: "system", Content: "You check a connection. Answer briefly."},
		{Role: "user", Content: "Reply with the single word OK."},
	}})
	test := &SetupModelTest{OK: err == nil, LatencyMS: time.Since(started).Milliseconds(), Model: result.Model}
	if err != nil {
		test.Reason = modelFailureReason(err)
		test.Detail = truncateDetail(err.Error())
	}
	return test, nil
}

func truncateDetail(text string) string {
	if len(text) > 300 {
		return textutil.Truncate(text, 300) + "…"
	}
	return text
}
