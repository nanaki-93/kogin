package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type agentSettingsKey struct{}

// ErrAgentSettingsStale reports agent overrides (Settings → Models & agents)
// saved for an earlier model configuration. New agent work waits until they
// are reset, so an old override never silently applies to new models.
var ErrAgentSettingsStale = errors.New("agent settings were saved for an earlier model configuration; reset them in Settings → Models & agents")

type admittedAgentSettings struct {
	root          string
	runtime       config.AgentRuntime
	configuration string
}

func (s *Service) agentConfiguration(runtime config.AgentRuntime) (string, error) {
	data, err := json.Marshal(struct {
		Profiles [3]config.ModelProfile
		Runtime  config.AgentRuntime
	}{[3]config.ModelProfile{s.conf().runtimes.analyze.profile, s.conf().runtimes.bug.profile, s.conf().runtimes.function.profile}, runtime})
	if err != nil {
		return "", fmt.Errorf("fingerprint admitted configuration: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Service) freezeAgentSettings(ctx context.Context, root string) (context.Context, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	canonical, err := project.CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	if captured, ok := ctx.Value(agentSettingsKey{}).(admittedAgentSettings); ok {
		if captured.root != canonical {
			return nil, fmt.Errorf("%w: settings belong to another checkout", project.ErrRevisionConflict)
		}
		return ctx, nil
	}
	store, binding, err := s.agentJournal(canonical)
	if err != nil {
		return nil, err
	}
	record, err := store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	runtime, err := s.settingsRuntime(record, binding.Configuration)
	if err != nil {
		return nil, err
	}
	configuration := binding.Configuration
	if record.Values != nil {
		effective, err := s.agentConfiguration(runtime)
		if err != nil {
			return nil, err
		}
		// Retain the operator identity separately so a pure human handoff can
		// use its original settings after a future-run preference changes.
		digest, err := hex.DecodeString(effective)
		if err != nil {
			return nil, fmt.Errorf("encode settings fingerprint: %w", err)
		}
		configuration = "settings-v1:" + configuration + ":" + base64.RawURLEncoding.EncodeToString(digest)
	}
	return context.WithValue(ctx, agentSettingsKey{}, admittedAgentSettings{root: canonical, runtime: runtime, configuration: configuration}), nil
}

func (s *Service) settingsRuntime(record agentstate.SettingsRecord, configuration string) (config.AgentRuntime, error) {
	runtime := s.conf().agentRuntime
	telemetry, err := config.ResolveAgentTelemetry(runtime.Telemetry)
	if err != nil {
		return runtime, err
	}
	runtime.Telemetry = telemetry
	if record.Values == nil {
		return runtime, nil
	}
	if record.Configuration != configuration {
		return runtime, fmt.Errorf("%w: %w", project.ErrRevisionConflict, ErrAgentSettingsStale)
	}
	if err := s.validateAgentPreferences(*record.Values); err != nil {
		return runtime, fmt.Errorf("%w: invalid project settings: %v", project.ErrRevisionConflict, err)
	}
	values := record.Values
	runtime.Roles = config.AgentRoles{Creation: config.ModelScope(values.Roles.Creation), Testing: config.ModelScope(values.Roles.Testing), Review: config.ModelScope(values.Roles.Review)}
	runtime.Limits = values.Limits
	return runtime, nil
}

// Internal helpers run only after admission. Pure deadline calculations in tests
// can use the immutable configured defaults when no operation is admitted.
func (s *Service) admittedRuntime(ctx context.Context) config.AgentRuntime {
	if captured, ok := ctx.Value(agentSettingsKey{}).(admittedAgentSettings); ok {
		return captured.runtime
	}
	return s.conf().agentRuntime
}

func (s *Service) confirmationJournal(ctx context.Context, root, runID string) (*agentstate.Store, agentstate.Binding, error) {
	store, binding, err := s.agentJournal(root)
	if err != nil {
		return nil, binding, err
	}
	history, err := store.History(ctx)
	if err != nil {
		return nil, binding, err
	}
	for _, run := range history.Runs {
		if run.ID != runID {
			continue
		}
		parts := strings.Split(run.Binding.Configuration, ":")
		if len(parts) == 3 && parts[0] == "settings-v1" && parts[1] == binding.Configuration {
			binding.Configuration = run.Binding.Configuration
		}
		// A path-scoped task stays current across reindexing, so its waiting
		// input keeps the revision its run was admitted with.
		if run.Binding.Project == binding.Project && run.Binding.Policy == binding.Policy {
			binding.Revision = run.Binding.Revision
		}
		// The native handoff still compares the complete project/revision/
		// policy binding and the exact request and proposal identities.
		return store, binding, nil
	}
	return nil, binding, agentstate.ErrNotFound
}
