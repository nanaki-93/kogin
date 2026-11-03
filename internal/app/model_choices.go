package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

type ModelChoice struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Provider string         `json:"provider"`
	Source   string         `json:"source"`
	Location string         `json:"location"`
	Model    EffectiveModel `json:"model"`
}

type AvailableModels struct {
	Models   []ModelChoice     `json:"models"`
	Defaults map[string]string `json:"defaults"`
	Pi       ModelDiscovery    `json:"pi"`
	Agy      ModelDiscovery    `json:"agy"`
	Claude   ModelDiscovery    `json:"claude"`
}

type ModelDiscovery struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// IDs describe model configurations, not the job that originally configured them.
func configuredModelID(profile config.ModelProfile) (string, error) {
	profile.Scope, profile.APIKey, profile.ContextMaxTokens = "", "", 0
	profile.AccessToken = ""
	encoded, err := json.Marshal(profile)
	if err != nil {
		return "", fmt.Errorf("encode configured model identity: %w", err)
	}
	return fmt.Sprintf("configured:%x", sha256.Sum256(encoded)), nil
}

func cliModelSelection(selection string) (config.ModelProvider, string, bool) {
	if model, ok := strings.CutPrefix(selection, "agy:"); ok {
		return config.AgyProvider, model, config.ValidAgyModelID(model)
	}
	if model, ok := strings.CutPrefix(selection, "claude:"); ok {
		return config.ClaudeProvider, model, config.ValidClaudeModelID(model)
	}
	if model, ok := strings.CutPrefix(selection, "pi:"); ok {
		provider, id, found := strings.Cut(model, "/")
		return config.PiProvider, model, found && llm.ValidPiModelID(provider, id)
	}
	return "", "", false
}

func validModelSelection(selection string) bool {
	if selection == "analyze" || selection == "bug" || selection == "code" {
		return true
	}
	if _, _, ok := cliModelSelection(selection); ok {
		return true
	}
	if !strings.HasPrefix(selection, "configured:") {
		return false
	}
	value, err := hex.DecodeString(strings.TrimPrefix(selection, "configured:"))
	return err == nil && len(value) == sha256.Size
}

func (s *Service) cliExecutable(provider config.ModelProvider) string {
	for _, runtime := range []modelRuntime{s.conf().runtimes.analyze, s.conf().runtimes.bug, s.conf().runtimes.function} {
		if runtime.profile.Provider == provider {
			return runtime.profile.CLIPath
		}
	}
	return string(provider)
}

func (s *Service) selectedRuntime(scope config.ModelScope, selection string) (modelRuntime, bool) {
	if runtime, ok := s.conf().runtimes.forScope(selection); ok {
		return runtime, true
	}
	base, _ := s.conf().runtimes.forScope(string(scope))
	for _, runtime := range []modelRuntime{s.conf().runtimes.analyze, s.conf().runtimes.bug, s.conf().runtimes.function} {
		id, err := configuredModelID(runtime.profile)
		if err != nil {
			return modelRuntime{}, false
		}
		if id != selection {
			continue
		}
		// Reuse the immutable transport and sampling values; only the operation's
		// context and dispatch limits change when assigning this model.
		selected := runtime
		selected.profile.Scope, selected.profile.ContextMaxTokens = scope, base.profile.ContextMaxTokens
		selected.effective.Scope, selected.effective.Profile = string(scope), selection
		selected.effective.ContextMaxTokens = base.profile.ContextMaxTokens
		selected.effective.Timeout, selected.effective.MaxRetries = base.effective.Timeout, base.effective.MaxRetries
		return selected, true
	}
	if provider, model, ok := cliModelSelection(selection); ok {
		profile := config.ModelProfile{Scope: scope, Provider: provider, CLIPath: s.cliExecutable(provider), Model: model, ContextMaxTokens: base.profile.ContextMaxTokens}
		runtime := newModelRuntime(profile, duration(base.effective.Timeout), base.effective.MaxRetries)
		runtime.effective.Profile = selection
		return runtime, true
	}
	return modelRuntime{}, false
}

func (s *Service) validateModelSelections(ctx context.Context, selections []string) error {
	available := map[config.ModelProvider]map[string]bool{}
	for _, selection := range selections {
		if provider, _, cli := cliModelSelection(selection); cli {
			if available[provider] == nil {
				models, err := s.discoverModelChoices(ctx, provider)
				if err != nil {
					return fmt.Errorf("%s model catalog unavailable: %w", provider, err)
				}
				available[provider] = make(map[string]bool, len(models))
				for _, model := range models {
					available[provider][model.ID] = true
				}
			}
			if available[provider][selection] {
				continue
			}
		} else if _, ok := s.selectedRuntime(config.AnalyzeModelScope, selection); ok {
			continue
		}
		return fmt.Errorf("%w: the selected model %s is not available: its CLI or the configuration no longer offers it. Choose another model in Settings → Models & agents", project.ErrRevisionConflict, selection)
	}
	return nil
}

func (s *Service) validateAnalysisModelSelections(ctx context.Context, models *AnalysisModels, resume *AnalysisRunIdentity) error {
	if resume != nil {
		run, err := s.ReadAnalysisRun(ctx, resume.ProjectID, resume.ProjectRevision)
		// An explicit preview/resume can recover a progress-write fault using
		// the retained in-memory run. Other read failures still stop admission.
		if err != nil && (run == nil || !errors.Is(err, errAnalysisRunPersistence)) {
			return err
		}
		if run == nil || run.Identity != *resume {
			return project.ErrRevisionConflict
		}
		models = run.Plan.Models
	}
	if models == nil {
		return nil
	}
	return s.validateModelSelections(ctx, models.selections())
}
