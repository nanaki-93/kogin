// Package app contains the daemon's configured application service.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/app/changes"
	"github.com/nanaki-93/mini-orca/v2/internal/app/gitops"
	"github.com/nanaki-93/mini-orca/v2/internal/brand"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

// EffectiveModel is the non-secret metadata for one configured generation scope.
type EffectiveModel struct {
	Scope            string           `json:"scope"`
	Profile          string           `json:"profile"`
	Model            string           `json:"model"`
	ReasoningEffort  string           `json:"reasoning_effort,omitempty"`
	ProviderOrigin   string           `json:"provider_origin"`
	RemoteProvider   bool             `json:"remote_provider"`
	Temperature      *float32         `json:"temperature"`
	TopP             *float32         `json:"top_p,omitempty"`
	TopK             *int             `json:"top_k,omitempty"`
	MaxTokens        *int             `json:"max_tokens"`
	ContextMaxTokens int              `json:"context_max_tokens"`
	Timeout          string           `json:"timeout"`
	MaxRetries       int              `json:"max_retries"`
	Capabilities     llm.Capabilities `json:"capabilities,omitzero"`
}

// ModelCatalog exposes the configured model metadata for each fixed scope and
// the configuration file it came from (empty for the built-in defaults).
type ModelCatalog struct {
	Scopes     map[string]EffectiveModel `json:"scopes"`
	ConfigFile string                    `json:"config_file"`
}

type modelRuntime struct {
	profile   config.ModelProfile
	effective EffectiveModel
	client    *agentruntime.ProviderClient
}

type modelOutput = agentruntime.Result

// Service owns configured model access and the active project's generation path.
type Service struct {
	// global is the daemon's configuration; configured is what the active
	// project runs with (global with the project's own file applied).
	global                          *config.Config
	configured                      atomic.Pointer[configuration]
	worktreesMu                     sync.Mutex
	worktrees                       map[string]*Service
	owner                           *Service // The service that opened this worktree's; nil for the active project.
	manager                         *project.Manager
	projectMaps                     projectMapCache
	docsRefreshMu                   sync.Mutex // One docs refresh at a time.
	docsJob                         docsRefreshJob
	discoverPiModels                func(context.Context, string) ([]llm.PiModel, error)
	discoverAgyModels               func(context.Context, string) ([]llm.AgyModel, error)
	discoverClaudeModels            func(context.Context, string) ([]llm.ClaudeModel, error)
	jobLifecycleMu                  sync.Mutex
	changesMu                       sync.Mutex
	changeAuthority                 map[string]string
	changeWorkflow                  changeWorkflowController
	analysisRun                     *analysisRunController
	writeAnalysisRun                func(string, []byte, os.FileMode) error
	executionTrust                  *executionTrustStore
	checkBaselines                  checkBaselines
	events                          *eventBroker
	buildDeclarationContext         func(string, project.FunctionContextOptions) (string, project.ContextManifest, error)
	loadSecurityFileReport          func(string, project.SecurityReportInput) (*project.SecurityFileReport, error)
	beforeSecurityReviewPublication func()
}

// New creates the service for manager's project. cfg is the daemon's
// configuration; the project's own configuration file, when it has a valid
// one, is applied on top (see ApplyProjectConfiguration).
func New(cfg *config.Config, manager *project.Manager) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("application config is required")
	}
	if manager == nil {
		return nil, fmt.Errorf("project manager is required")
	}
	configured, err := newConfiguration(cfg)
	if err != nil {
		return nil, err
	}
	contextBuilder := project.NewContextBuilder()
	service := &Service{
		global:                  cfg,
		worktrees:               make(map[string]*Service),
		discoverPiModels:        llm.DiscoverPiModels,
		discoverAgyModels:       llm.DiscoverAgyModels,
		discoverClaudeModels:    llm.DiscoverClaudeModels,
		manager:                 manager,
		changeAuthority:         make(map[string]string),
		analysisRun:             &analysisRunController{},
		executionTrust:          newExecutionTrustStore(),
		events:                  newEventBroker(),
		buildDeclarationContext: contextBuilder.BuildFunctionWithManifest,
		loadSecurityFileReport:  project.LoadSecurityFileReport,
	}
	service.configured.Store(configured)
	if root := manager.Root(); root != "" {
		service.loadProjectConfiguration(root)
	}
	return service, nil
}

func newModelRuntime(profile config.ModelProfile, timeout time.Duration, maxRetries int) modelRuntime {
	runtime := modelRuntime{
		profile: profile,
		client:  agentruntime.NewProviderClient(profile),
		effective: EffectiveModel{
			Scope: string(profile.Scope), Profile: string(profile.Scope), Model: profile.Model,
			ReasoningEffort: profile.ReasoningEffort,
			ProviderOrigin:  modelProviderOrigin(profile), RemoteProvider: profile.IsCLI() || !isLoopbackURL(profile.APIBaseURL),
			ContextMaxTokens: profile.ContextMaxTokens,
			Timeout:          timeout.String(), MaxRetries: maxRetries,
			Capabilities: llm.ProviderCapabilities(profile),
		},
	}
	if !profile.IsCLI() {
		runtime.effective.Temperature = &profile.Temperature
		runtime.effective.TopP = profile.TopP
		runtime.effective.TopK = profile.TopK
		runtime.effective.MaxTokens = &profile.MaxTokens
	}
	return runtime
}

func modelProviderOrigin(profile config.ModelProfile) string {
	if profile.IsCLI() {
		return "cli://" + string(profile.Provider)
	}
	return providerOrigin(profile.APIBaseURL)
}

func configuredDuration(seconds int, defaultValue time.Duration) time.Duration {
	if seconds <= 0 {
		return defaultValue
	}
	return time.Duration(seconds) * time.Second
}

// ValidateMutableRequest rejects source-scoped operations based on a different project or file state.
func (s *Service) ValidateMutableRequest(id, revision, targetFile, baseFileHash string) error {
	return s.manager.ValidateMutableRequest(id, revision, targetFile, baseFileHash)
}

// ImportProject indexes root for activation. It asks root's analyze model for
// a project overview only when that model is local or the caller confirmed
// its remote provider; otherwise it records the deterministic inventory, and
// a later analysis writes the overview.
func (s *Service) ImportProject(ctx context.Context, root string, confirmRemote bool) (*project.Analysis, error) {
	if err := migrateLegacyMetadata(ctx, root); err != nil {
		return nil, err
	}
	target := s.importConfiguration(root)
	runtime := target.runtimes.analyze
	if !runtime.effective.RemoteProvider || confirmRemote {
		return s.analyzeProjectKnowledge(ctx, root, target)
	}
	timed, cancel := context.WithTimeout(ctx, target.importTimeout)
	defer cancel()
	snapshot, err := project.ScanProject(timed, root)
	if err != nil {
		return nil, err
	}
	analysis, err := project.NewAnalyzerWithProvenance(nil, runtime.profile.Model, string(config.AnalyzeModelScope), runtime.effective.ProviderOrigin, runtime.effective.ReasoningEffort).AnalyzeSnapshot(timed, snapshot)
	if err != nil {
		return nil, err
	}
	analysis.Report.Failure = importWithoutOverview
	analysis.Summary = importWithoutOverview
	return analysis, project.StoreProjectAnalysisReport(analysis.Path, analysis.Report)
}

// migrateLegacyMetadata moves the project's data from .mini-orca, its folder
// before the rename to Kasuri, to .kasuri. When it moves it, Git is told
// locally to ignore .kasuri as it may have ignored .mini-orca; a project that
// is not a Git repository is fine.
func migrateLegacyMetadata(ctx context.Context, root string) error {
	moved, err := storage.MigrateLegacyMetadata(root)
	if err != nil || !moved {
		return err
	}
	_ = gitops.AddLocalExclude(ctx, root, "/"+brand.MetadataDir+"/")
	return nil
}

const importWithoutOverview = "The project overview was not requested from the remote analyze model. Run an analysis to write it."

// importConfiguration is the configuration root will run with once active.
func (s *Service) importConfiguration(root string) *configuration {
	if target := s.projectConfiguration(root); target != nil && (!usesDaemonConfiguration(target) || s.conf().config != s.global) {
		return target
	}
	return s.conf()
}

// RestoreProject reopens a previously imported project from local persisted
// analysis without making a model request.
func (s *Service) RestoreProject(root string) (*project.Analysis, error) {
	if err := migrateLegacyMetadata(context.Background(), root); err != nil {
		return nil, err
	}
	if _, err := agentstate.Open(root); err != nil {
		return nil, fmt.Errorf("inspect saved agent journal: %w", err)
	}
	runtime := s.conf().runtimes.analyze
	return project.NewAnalyzerWithProvenance(nil, runtime.profile.Model, string(config.AnalyzeModelScope), runtime.effective.ProviderOrigin, runtime.effective.ReasoningEffort).Restore(root)
}

// EffectiveModels returns safe metadata for every configured prompt scope.
func (s *Service) EffectiveModels() []EffectiveModel {
	return []EffectiveModel{s.conf().runtimes.analyze.effective, s.conf().runtimes.bug.effective, s.conf().runtimes.function.effective}
}

// CurrentModelCatalog returns only the safe effective metadata needed for
// model destination display and remote-confirmation decisions.
func (s *Service) CurrentModelCatalog() ModelCatalog {
	catalog := ModelCatalog{Scopes: make(map[string]EffectiveModel, 3)}
	if s.conf().config != nil {
		catalog.ConfigFile = s.conf().config.Source
	}
	for _, profile := range s.EffectiveModels() {
		catalog.Scopes[profile.Scope] = profile
	}
	return catalog
}

func requireModelRuntimeConfirmation(runtime modelRuntime, confirmed bool) error {
	if runtime.effective.RemoteProvider && !confirmed {
		return fmt.Errorf("remote %s provider requires explicit confirmation", runtime.effective.Scope)
	}
	return nil
}

func (s *Service) contextManifestForRuntime(manifest project.ContextManifest, runtime modelRuntime) project.ContextManifest {
	return bindContextManifestModel(manifest, runtime.effective)
}

func bindContextManifestModel(manifest project.ContextManifest, model EffectiveModel) project.ContextManifest {
	manifest.Scope, manifest.Model = model.Scope, model.Model
	manifest.ProviderOrigin, manifest.RemoteProvider = model.ProviderOrigin, model.RemoteProvider
	return manifest
}

func isLoopbackURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.Trim(parsed.Hostname(), "[]")
	if host == "localhost" || host == "::1" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *Service) retryRequest(ctx context.Context, runtime modelRuntime, messages []llm.ChatMessage, schema *llm.JSONSchema) (modelOutput, error) {
	return s.retryRequestAuthorized(ctx, runtime, messages, schema, nil)
}

// The guard reserves and validates each attempt immediately before transport.
// Guard failures are never retried or treated as provider failures.
func (s *Service) retryRequestAuthorized(ctx context.Context, runtime modelRuntime, messages []llm.ChatMessage, schema *llm.JSONSchema, beforeAttempt func(context.Context) error) (modelOutput, error) {
	return s.retryAgentRequest(ctx, runtime, agentruntime.Request{Messages: messages, Schema: schema}, beforeAttempt)
}

func (s *Service) retryAgentRequest(ctx context.Context, runtime modelRuntime, request agentruntime.Request, beforeAttempt func(context.Context) error) (modelOutput, error) {
	request.Stream = request.Stream || agentruntime.StreamingRequested(ctx)
	ctx, release, err := s.agentRunContext(ctx, runtime)
	if err != nil {
		return modelOutput{}, err
	}
	defer release()
	var lastErr error
	for attempt := 0; attempt <= runtime.effective.MaxRetries; attempt++ {
		if beforeAttempt != nil {
			if err := beforeAttempt(ctx); err != nil {
				return modelOutput{}, err
			}
		}
		result, err := executeAgentAttempt(ctx, runtime, request)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil || runtimeRequestTerminal(runtime, err) || attempt == runtime.effective.MaxRetries {
			break
		}
		wait := s.conf().retryBase << attempt
		if wait > s.conf().retryMax {
			wait = s.conf().retryMax
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return modelOutput{}, context.Cause(ctx)
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return modelOutput{}, context.Cause(ctx)
	}
	return modelOutput{}, lastErr
}

func executeAgentAttempt(ctx context.Context, runtime modelRuntime, request agentruntime.Request) (result modelOutput, err error) {
	ctx, trace, err := agentruntime.StartTrace(ctx, "retry", "provider_attempt")
	if err != nil {
		return result, err
	}
	defer func() { err = trace.Finish(ctx, err) }()
	return runtime.execute(ctx, request)
}

func runtimeRequestTerminal(runtime modelRuntime, err error) bool {
	// A plan's usage limit lasts until it resets: retrying only spends the wait.
	return errors.Is(err, llm.ErrPlanLimit) || errors.Is(err, agentstate.ErrPersistence) || errors.Is(err, agentruntime.ErrBudgetExceeded) || errors.Is(err, agentruntime.ErrPolicyDenied) || errors.Is(err, llm.ErrRedirectRejected) || errors.Is(err, llm.ErrStructuredRequestRejected) ||
		((runtime.profile.IsCLI() || runtime.profile.IsGoogle()) && errors.Is(err, llm.ErrRequestRejected))
}

func providerOrigin(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func duration(value string) time.Duration {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 5 * time.Minute
	}
	return parsed
}
func newOpaqueID(prefix string) string {
	return changes.NewOpaqueID(prefix)
}
