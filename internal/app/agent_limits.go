package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// Admission selects destinations before workers start. Per-attempt source and
// consent guards still run in their workflow owner immediately before dispatch.
func (s *Service) agentRunContext(ctx context.Context, runtimes ...modelRuntime) (context.Context, context.CancelFunc, error) {
	return s.agentRunContextAt(ctx, s.manager.Root(), runtimes...)
}

func (s *Service) agentRunContextAt(ctx context.Context, root string, runtimes ...modelRuntime) (context.Context, context.CancelFunc, error) {
	return s.agentRunContextForSnapshot(ctx, root, nil, runtimes...)
}

func (s *Service) agentRunContextForSnapshot(ctx context.Context, root string, snapshot *project.Analysis, runtimes ...modelRuntime) (context.Context, context.CancelFunc, error) {
	ctx, err := s.freezeAgentSettings(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	settings := ctx.Value(agentSettingsKey{}).(admittedAgentSettings)
	ctx = s.observeAgentRuns(ctx)
	clients := make([]*agentruntime.ProviderClient, 0, len(runtimes))
	for _, runtime := range runtimes {
		clients = append(clients, runtime.client)
	}
	admitted, err := agentruntime.WithPolicy(ctx, clients...)
	if err != nil {
		agentruntime.CancelRun(ctx, err)
		return nil, nil, fmt.Errorf("admit agent destinations: %w", err)
	}
	bounded, cancel, err := agentruntime.WithBudget(admitted, settings.runtime.Limits)
	if err != nil {
		return nil, nil, err
	}
	store, binding, err := s.agentJournal(root)
	if err == nil {
		binding.Configuration = settings.configuration
		if snapshot != nil {
			binding.Project, binding.Revision = snapshot.ProjectID, snapshot.ProjectRevision
		}
		binding = inheritWorkflowRevision(bounded, binding)
		bounded, err = agentruntime.WithPersistence(bounded, store, binding)
	}
	if err != nil {
		agentruntime.CancelRun(ctx, err)
		cancel()
		return nil, nil, err
	}
	bounded, err = agentruntime.WithContextConfig(bounded, settings.runtime.Context)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}

func (s *Service) agentJournal(root string) (*agentstate.Store, agentstate.Binding, error) {
	canonical, err := project.CanonicalRoot(root)
	if err != nil {
		return nil, agentstate.Binding{}, err
	}
	store, err := openAgentJournal(canonical)
	if err != nil {
		return nil, agentstate.Binding{}, fmt.Errorf("%w: open checkout journal: %w", agentstate.ErrPersistence, err)
	}
	policy, err := project.NewContextPolicy(canonical)
	if err != nil {
		return nil, agentstate.Binding{}, err
	}
	identity := sha256.Sum256([]byte(canonical))
	binding := agentstate.Binding{Project: hex.EncodeToString(identity[:]), Revision: "unindexed", Policy: policy.Version()}
	if canonical == s.manager.Root() {
		index, err := s.manager.Index()
		if err != nil && !errors.Is(err, project.ErrNoActiveProject) {
			return nil, binding, err
		}
		if index != nil {
			binding.Project = index.ProjectID
			binding.Revision = index.ProjectRevision
		}
	}
	binding.Configuration, err = s.agentConfiguration(s.conf().agentRuntime)
	return store, binding, err
}

// inheritWorkflowRevision keeps a change workflow's requests on the project
// revision its run was admitted with. A reindex during the run does not stop
// a task whose freshness scope is unchanged; that scope, not the revision,
// guards its source.
func inheritWorkflowRevision(ctx context.Context, binding agentstate.Binding) agentstate.Binding {
	admitted, ok := agentruntime.PersistenceBinding(ctx)
	if ok && ctx.Value(changeWorkflowContextKey{}) != nil && admitted.Project == binding.Project && admitted.Policy == binding.Policy && admitted.Configuration == binding.Configuration {
		binding.Revision = admitted.Revision
	}
	return binding
}

func (s *Service) workflowAgentContext(ctx context.Context, models ChangeWorkflowModels) (context.Context, context.CancelFunc, error) {
	runtimes := make([]modelRuntime, 0, 3)
	for stage, profile := range models.selections() {
		runtimes = append(runtimes, s.workflowStageRuntime(stage, profile))
	}
	return s.agentRunContext(ctx, runtimes...)
}

func (s *Service) analysisAgentContext(ctx context.Context, preview *AnalysisRunPreview) (context.Context, context.CancelFunc, error) {
	runtimes := make([]modelRuntime, 0, 4)
	for _, provider := range preview.Providers {
		for _, stage := range provider.Stages {
			runtime := s.analysisModelRuntime(stage, preview.Models)
			// Missing models remain unavailable in the stage/recovery owner.
			// They receive no grant and cannot silently acquire a fallback.
			if runtime.client != nil {
				runtimes = append(runtimes, runtime)
			}
		}
	}
	return s.agentRunContext(ctx, runtimes...)
}

func (s *Service) resolveWorkflowModels(ctx context.Context, models ChangeWorkflowModels) ChangeWorkflowModels {
	roles := s.admittedRuntime(ctx).Roles
	if models.Create == "" {
		models.Create = string(roles.Creation)
	}
	if models.Test == "" {
		models.Test = string(roles.Testing)
	}
	if models.Review == "" {
		models.Review = string(roles.Review)
	}
	return models
}
