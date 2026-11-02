package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/api"
	"github.com/nanaki-93/kasuri/v2/internal/api/handlers"
	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/brand"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/logging"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/version"
)

func main() {
	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		logging.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// Initialize logger
	logging.Init(logging.Config{
		Level:         cfg.Logging.Level,
		Format:        cfg.Logging.Format,
		Filename:      cfg.Logging.Filename,
		SensitiveKeys: cfg.Logging.SensitiveKeys,
	})

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		logging.Error("Invalid configuration", "error", err)
		os.Exit(1)
	}

	projectManager, err := project.NewManager(cfg.ProjectPath)
	if err != nil {
		logging.Error("Invalid initial project path", "error", err)
		os.Exit(1)
	}

	application, err := app.New(cfg, projectManager)
	if err != nil {
		logging.Error("Failed to initialize application service", "error", err)
		os.Exit(1)
	}

	// Log successful startup
	logging.Info("Kasuri daemon started successfully")
	for _, model := range application.EffectiveModels() {
		logging.Info("Model scope initialized", "scope", model.Scope, "model", model.Model, "reasoning_effort", model.ReasoningEffort, "provider_origin", model.ProviderOrigin, "remote_provider", model.RemoteProvider)
	}
	logging.Info("Workflow: single-coder preview with explicit review")

	token, err := newWriteToken()
	if err != nil {
		logging.Error("Write token not saved; only the Web UI can change state", "error", err)
	} else {
		logging.Info("Write token saved for local clients", "file", token.file)
	}

	// Start HTTP server with all API endpoints
	server := startHTTPServer(application, projectManager, token)

	// Wait for shutdown signal
	quit := waitForShutdown()
	logging.Info("Daemon stopping", "reason", quit)
	application.CancelChangeWorkflows()

	// Each phase gets its own budget: an HTTP drain that runs out of time
	// must not leave running jobs and their check processes uncanceled.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHTTP()
	if err := server.Shutdown(httpCtx); err != nil {
		logging.Error("HTTP server shutdown error", "error", err)
	}
	jobsCtx, cancelJobs := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelJobs()
	if err := application.ShutdownChangeWorkflows(jobsCtx); err != nil {
		logging.Error("Change workflow shutdown error", "error", err)
	}

	logging.Info("Shutdown complete")
}

// startHTTPServer creates and starts the HTTP server with all API endpoints.
func startHTTPServer(
	application *app.Service,
	projectManager *project.Manager,
	token writeToken,
) *http.Server {
	mux := newHTTPMux(application, projectManager, token)

	server := &http.Server{
		Addr:         daemonAddress(),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}
	// Event streams end only when their request context does; close them at
	// shutdown so Shutdown can drain instead of waiting out its deadline.
	streams, closeStreams := context.WithCancel(context.Background())
	server.BaseContext = func(net.Listener) context.Context { return streams }
	server.RegisterOnShutdown(closeStreams)

	// Start server in a goroutine
	go func() {
		logging.Info("HTTP server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	return server
}

// daemonAddress is loopback-only unless a local deployment explicitly opts in
// to another bind address (for example, a container port mapping).
func daemonAddress() string {
	if address := brand.Getenv("KASURI_BIND_ADDRESS"); address != "" {
		return address
	}
	return "127.0.0.1:9090"
}

// newHTTPMux registers only the local daemon API routes. Requests that change
// state must carry the launch's write token.
func newHTTPMux(
	application *app.Service,
	projectManager *project.Manager,
	token writeToken,
) http.Handler {
	current := newProjectHTTPMux(application, projectManager, token.value)
	worktrees := &worktreeRouter{token: token.value}
	return api.LocalOnly(api.RequireWriteToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/current/events" {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(6 * time.Minute))
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			api.WriteAppError(w, api.BadRequest("invalid request query", "Provide valid URL-encoded query parameters.", err))
			return
		}
		paths, scoped := query["worktree_path"]
		if !scoped || r.URL.Path == "/ui" {
			current.ServeHTTP(w, r)
			return
		}
		if len(paths) != 1 || paths[0] == "" {
			api.WriteAppError(w, api.BadRequest("invalid worktree scope", "Provide worktree_path once with an absolute registered worktree path.", nil))
			return
		}
		if r.URL.Path != "/api/projects/current" && !strings.HasPrefix(r.URL.Path, "/api/projects/current/") {
			api.WriteError(w, http.StatusBadRequest, "worktree scope requires a project endpoint")
			return
		}
		service, manager, err := application.WorktreeService(r.Context(), paths[0])
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Worktree scope belongs to routing, not the endpoint's identity guards or filters.
		query.Del("worktree_path")
		scopedRequest := r.Clone(r.Context())
		scopedRequest.URL.RawQuery = query.Encode()
		worktrees.handler(service, manager).ServeHTTP(w, scopedRequest)
	}), token.value, token.file), daemonHostPolicy())
}

func newProjectHTTPMux(application *app.Service, projectManager *project.Manager, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/current", func(w http.ResponseWriter, r *http.Request) {
		analysis, err := projectManager.Analysis()
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		api.WriteJSON(w, http.StatusOK, analysis)
	})

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version.Version + `"}`))
	})

	// Status endpoint
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"running","version":"` + version.Version + `","workflow":"shared_proposals"}`))
	})

	webUIHandler := handlers.NewWebUIHandler(token)
	mux.HandleFunc("GET /ui", webUIHandler.Serve)
	mux.HandleFunc("GET /ui/assets/{path...}", webUIHandler.ServeAsset)

	changeHandler := handlers.NewChangeHandler(application, projectManager)
	settingsHandler := handlers.NewAgentSettingsHandler(application)
	setupHandler := handlers.NewSetupHandler(application)
	mux.HandleFunc("GET /api/projects/current/setup", setupHandler.Get)
	mux.HandleFunc("POST /api/projects/current/setup", setupHandler.Save)
	mux.HandleFunc("POST /api/projects/current/setup/skip", setupHandler.Skip)
	mux.HandleFunc("POST /api/projects/current/setup/timeouts", setupHandler.SaveTimeouts)
	mux.HandleFunc("POST /api/projects/current/setup/test-model", setupHandler.TestModel)
	mux.HandleFunc("GET /api/projects/current/agent-settings", settingsHandler.Get)
	mux.HandleFunc("POST /api/projects/current/agent-settings/control", settingsHandler.Control)
	gitHandler := handlers.NewGitHandler(application)
	mux.HandleFunc("GET /api/projects/current/git/workspace", gitHandler.Workspace)
	mux.HandleFunc("POST /api/projects/current/git/merge-preview", gitHandler.PreviewMerge)
	mux.HandleFunc("POST /api/projects/current/git/merge", gitHandler.Merge)
	mux.HandleFunc("POST /api/projects/current/git/merge-checks", gitHandler.MergeChecks)
	mux.HandleFunc("POST /api/projects/current/git/cleanup-merged", gitHandler.CleanupMergedWorktree)
	mux.HandleFunc("POST /api/projects/current/git/abort-merge", gitHandler.AbortMerge)
	mux.HandleFunc("POST /api/projects/current/git/discard-worktree", gitHandler.DiscardWorktree)
	mux.HandleFunc("GET /api/projects/current/changes", changeHandler.List)
	mux.HandleFunc("POST /api/projects/current/changes", changeHandler.Open)
	mux.HandleFunc("GET /api/projects/current/usage", changeHandler.Usage)
	mux.HandleFunc("GET /api/projects/current/quarantine", changeHandler.Quarantine)
	mux.HandleFunc("POST /api/projects/current/quarantine/delete", changeHandler.DeleteQuarantined)
	mux.HandleFunc("GET /api/projects/current/changes/{sessionID}", changeHandler.Get)
	mux.HandleFunc("GET /api/projects/current/changes/{sessionID}/rules", changeHandler.Rules)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/messages", changeHandler.Message)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/checks", changeHandler.Checks)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/workflow", changeHandler.StartWorkflow)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/workflow/cancel", changeHandler.CancelWorkflow)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/workflow/input", changeHandler.RespondInput)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/review", changeHandler.Review)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/review-override", changeHandler.OverrideReview)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/review-file", changeHandler.ReviewFile)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/comments", changeHandler.Comment)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/comments/delete", changeHandler.DeleteComment)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/apply", changeHandler.Apply)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/delete", changeHandler.Delete)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/reanalyze", changeHandler.Reanalyze)
	mux.HandleFunc("POST /api/projects/current/changes/{sessionID}/retake", changeHandler.Retake)

	modelHandler := handlers.NewModelHandler(application)
	mux.HandleFunc("GET /api/models/current", modelHandler.Current)
	mux.HandleFunc("GET /api/models/available", modelHandler.Available)

	analysisHandler := handlers.NewAnalysisHandler(application)
	mux.HandleFunc("GET /api/projects/current/analysis/selection", analysisHandler.Selection)
	mux.HandleFunc("POST /api/projects/current/analysis/selection", analysisHandler.SaveSelection)
	mux.HandleFunc("POST /api/projects/current/analysis/preview", analysisHandler.Preview)
	mux.HandleFunc("POST /api/projects/current/analysis/run", analysisHandler.Start)
	mux.HandleFunc("GET /api/projects/current/analysis/run", analysisHandler.Current)
	mux.HandleFunc("POST /api/projects/current/analysis/run/control", analysisHandler.Control)

	projectHandler := handlers.NewProjectHandler(projectManager, application)
	mux.HandleFunc("POST /api/projects/import", projectHandler.Import)
	mux.HandleFunc("POST /api/projects/restore", projectHandler.Restore)
	mux.HandleFunc("GET /api/projects/current/overview", projectHandler.Overview)
	mux.HandleFunc("GET /api/projects/current/docs/component", projectHandler.DocsComponent)
	mux.HandleFunc("POST /api/projects/current/docs/refresh", projectHandler.RefreshDocs)
	mux.HandleFunc("POST /api/projects/current/ask", projectHandler.Ask)
	mux.HandleFunc("GET /api/projects/current/findings", projectHandler.Findings)
	mux.HandleFunc("GET /api/projects/current/rules", projectHandler.Rules)
	mux.HandleFunc("PATCH /api/projects/current/findings/{findingID}", projectHandler.UpdateFindingStatus)
	mux.HandleFunc("GET /api/projects/current/findings/task", projectHandler.IssuesTask)
	mux.HandleFunc("GET /api/projects/current/findings/{findingID}/task", projectHandler.IssueTask)
	mux.HandleFunc("GET /api/projects/current/execution-trust", projectHandler.ExecutionTrust)
	mux.HandleFunc("POST /api/projects/current/execution-trust", projectHandler.TrustProjectExecution)
	mux.HandleFunc("GET /api/projects/current/index", projectHandler.Index)
	mux.HandleFunc("GET /api/projects/current/files/info", projectHandler.FileInfo)
	mux.HandleFunc("GET /api/projects/current/files/analysis", projectHandler.FileAnalysis)
	mux.HandleFunc("POST /api/projects/current/reindex", projectHandler.Reindex)
	mux.HandleFunc("GET /api/projects/current/events", projectHandler.Events)

	return mux
}

// daemonHostPolicy accepts other hosts only for an explicit non-loopback bind;
// host names must additionally be listed in KASURI_ALLOWED_HOSTS.
func daemonHostPolicy() api.HostPolicy {
	policy := api.HostPolicy{External: daemonAllowsExternalHosts()}
	for _, name := range strings.Split(brand.Getenv("KASURI_ALLOWED_HOSTS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			policy.Names = append(policy.Names, name)
		}
	}
	return policy
}

func daemonAllowsExternalHosts() bool {
	address := brand.Getenv("KASURI_BIND_ADDRESS")
	if address == "" {
		return false
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// waitForShutdown blocks until the daemon is asked to stop.
func waitForShutdown() string {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	return (<-sigChan).String()
}
