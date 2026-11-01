package app

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func (s *Service) AvailableModels(ctx context.Context) (AvailableModels, error) {
	catalog, err := s.configuredModelChoices()
	if err != nil {
		return AvailableModels{}, err
	}
	discoveries := []struct {
		provider config.ModelProvider
		name     string
		status   *ModelDiscovery
		models   []ModelChoice
	}{
		{provider: config.PiProvider, name: "Pi", status: &catalog.Pi},
		{provider: config.AgyProvider, name: "Agy", status: &catalog.Agy},
		{provider: config.ClaudeProvider, name: "Claude Code", status: &catalog.Claude},
	}
	var wg sync.WaitGroup
	wg.Add(len(discoveries))
	for i := range discoveries {
		go func(index int) {
			defer wg.Done()
			d := &discoveries[index]
			models, discoverErr := s.discoverModelChoices(ctx, d.provider)
			if discoverErr != nil {
				*d.status = ModelDiscovery{Status: "unavailable", Message: fmt.Sprintf("%s models could not be loaded. Check that %s is installed, signed in, and its model configuration is valid, then refresh models.", d.name, d.name)}
				return
			}
			*d.status = ModelDiscovery{Status: "ready"}
			d.models = models
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return AvailableModels{}, ctx.Err()
	}
	for i := range discoveries {
		catalog.Models = append(catalog.Models, discoveries[i].models...)
	}
	sort.SliceStable(catalog.Models, func(i, j int) bool {
		a, b := catalog.Models[i], catalog.Models[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Name < b.Name
	})
	return catalog, nil
}

func (s *Service) configuredModelChoices() (AvailableModels, error) {
	catalog := AvailableModels{Models: []ModelChoice{}, Defaults: map[string]string{}}
	seen := map[string]bool{}
	for _, runtime := range []modelRuntime{s.conf().runtimes.analyze, s.conf().runtimes.bug, s.conf().runtimes.function} {
		id, err := configuredModelID(runtime.profile)
		if err != nil {
			return AvailableModels{}, err
		}
		catalog.Defaults[string(runtime.profile.Scope)] = id
		if seen[id] {
			continue
		}
		seen[id] = true
		model := runtime.effective
		model.Scope, model.Profile = "", id
		location := "remote"
		if runtime.profile.IsCLI() {
			location = "unknown"
		} else if !model.RemoteProvider {
			location = "local"
		}
		catalog.Models = append(catalog.Models, ModelChoice{ID: id, Name: model.Model, Source: "configured", Provider: string(runtime.profile.Provider), Location: location, Model: model})
	}
	return catalog, nil
}

func (s *Service) discoverModelChoices(ctx context.Context, provider config.ModelProvider) ([]ModelChoice, error) {
	switch provider {
	case config.PiProvider:
		return s.piModelChoices(ctx)
	case config.AgyProvider:
		models, err := s.discoverAgyModels(ctx, s.cliExecutable(provider))
		if err != nil {
			return nil, err
		}
		choices := make([]ModelChoice, 0, len(models))
		for _, model := range models {
			choices = append(choices, s.catalogModelChoice("agy:"+model.ID, model.Name, "agy", "agy", "unknown"))
		}
		return choices, nil
	case config.ClaudeProvider:
		models, err := s.discoverClaudeModels(ctx, s.cliExecutable(provider))
		if err != nil {
			return nil, err
		}
		choices := make([]ModelChoice, 0, len(models))
		for _, model := range models {
			choices = append(choices, s.catalogModelChoice("claude:"+model.ID, model.Name, "anthropic", "claude", "remote"))
		}
		return choices, nil
	default:
		return nil, fmt.Errorf("unsupported model catalog provider")
	}
}

func (s *Service) piModelChoices(ctx context.Context) ([]ModelChoice, error) {
	models, err := s.discoverPiModels(ctx, s.cliExecutable(config.PiProvider))
	if err != nil {
		return nil, err
	}
	choices := make([]ModelChoice, 0, len(models))
	for _, model := range models {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		location := "remote"
		if model.BaseURL == "" {
			location = "unknown"
		} else if isLoopbackURL(model.BaseURL) {
			location = "local"
		}
		choices = append(choices, s.catalogModelChoice("pi:"+model.Provider+"/"+model.ID, name, model.Provider, "pi", location))
	}
	return choices, nil
}

func (s *Service) catalogModelChoice(id, name, provider, source, location string) ModelChoice {
	runtime := s.runtimeForAnalysisScope(config.AnalyzeModelScope, id)
	model := runtime.effective
	model.Scope = ""
	return ModelChoice{ID: id, Name: name, Provider: provider, Source: source, Location: location, Model: model}
}
