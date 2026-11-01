package app

import (
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/logging"
)

// configuration is everything the service derives from the configuration it
// runs with. It is immutable and replaced whole, never edited, when the active
// project's configuration changes; readers take one snapshot with conf().
type configuration struct {
	config       *config.Config
	agentRuntime config.AgentRuntime
	runtimes     scopedRuntimes
	// projectFile is the project configuration file applied on top of the
	// daemon's, or empty; projectError says why an existing one was not.
	projectFile  string
	projectError string

	importTimeout        time.Duration
	analysisTimeout      time.Duration
	workflowModelTimeout time.Duration
	focusedCheckTimeout  time.Duration
	retryBase            time.Duration
	retryMax             time.Duration
}

// Defaults for time limits the configuration leaves unset.
const (
	defaultModelTimeout        = 5 * time.Minute
	defaultFocusedCheckTimeout = time.Minute
)

func newConfiguration(cfg *config.Config) (*configuration, error) {
	profiles, err := config.ResolveModelProfiles(cfg)
	if err != nil {
		return nil, err
	}
	agentConfig, err := config.ResolveAgentRuntime(cfg.AgentRuntime)
	if err != nil {
		return nil, err
	}
	functionTimeout := configuredDuration(cfg.Timeouts.GenerationSeconds, defaultModelTimeout)
	importTimeout := configuredDuration(cfg.Timeouts.ImportSeconds, defaultModelTimeout)
	analysisTimeout := configuredDuration(cfg.Timeouts.AnalysisSeconds, defaultModelTimeout)
	maxRetries := positiveOr(cfg.Retry.MaxRetries, 3)
	return &configuration{
		config:       cfg,
		agentRuntime: agentConfig,
		runtimes: scopedRuntimes{
			analyze:  newModelRuntime(profiles.Analyze, importTimeout, maxRetries),
			bug:      newModelRuntime(profiles.Bug, analysisTimeout, maxRetries),
			function: newModelRuntime(profiles.Code, functionTimeout, maxRetries),
		},
		importTimeout:        importTimeout,
		analysisTimeout:      analysisTimeout,
		workflowModelTimeout: configuredDuration(cfg.Timeouts.WorkflowModelSeconds, config.DefaultWorkflowModelSeconds*time.Second),
		focusedCheckTimeout:  configuredDuration(cfg.Timeouts.FocusedCheckSeconds, defaultFocusedCheckTimeout),
		retryBase:            time.Duration(positiveOr(cfg.Retry.BackoffBase, 1000)) * time.Millisecond,
		retryMax:             time.Duration(positiveOr(cfg.Retry.BackoffMax, 30000)) * time.Millisecond,
	}, nil
}

func usesDaemonConfiguration(c *configuration) bool {
	return c.projectFile == "" && c.projectError == ""
}

func positiveOr(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// conf is the configuration the service runs with now. A service built
// without New (in tests) gets an empty one.
func (s *Service) conf() *configuration {
	if current := s.configured.Load(); current != nil {
		return current
	}
	empty := &configuration{}
	if s.configured.CompareAndSwap(nil, empty) {
		return empty
	}
	return s.configured.Load()
}

// projectConfiguration is the configuration root runs with: the daemon's with
// root's own file applied when it has a valid one. An invalid file is
// reported and the daemon's configuration is used.
func (s *Service) projectConfiguration(root string) *configuration {
	if s.global == nil {
		return nil
	}
	merged, found, err := config.LoadProject(root, s.global)
	if !found {
		next, _ := newConfiguration(s.global) // The daemon's configuration was validated at startup.
		return next
	}
	var next *configuration
	if err == nil {
		next, err = newConfiguration(merged)
	}
	if err != nil {
		logging.Error("Project configuration not applied; using the daemon's", "file", config.ProjectConfigPath, "error", err)
		fallback, _ := newConfiguration(s.global)
		fallback.projectError = err.Error()
		return fallback
	}
	next.projectFile = merged.Source
	return next
}

// loadProjectConfiguration makes root's configuration the one the service
// runs with. Callers stop or hold the work the old one admitted.
func (s *Service) loadProjectConfiguration(root string) {
	current, next := s.conf(), s.projectConfiguration(root)
	if next == nil || usesDaemonConfiguration(next) && usesDaemonConfiguration(current) && current.config == s.global {
		return // Already the daemon's configuration; keep its runtimes.
	}
	s.configured.Store(next)
	if next.projectFile != "" {
		logging.Info("Project configuration applied", "file", next.projectFile)
	}
	s.notifyEvent("configuration")
}
