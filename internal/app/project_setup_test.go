package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func setupIdentityOf(t *testing.T, s *Service) (string, string) {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return index.ProjectID, index.ProjectRevision
}

func setupRequestFrom(setup *ProjectSetup) ProjectSetupRequest {
	return ProjectSetupRequest{ProjectID: setup.ProjectID, ProjectRevision: setup.ProjectRevision, Models: setup.Models, Roles: setup.Roles, Limits: setup.Limits}
}

func TestProjectSetupIsOfferedThenSavedAndAppliedLive(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	s.global.ModelScopes.Analyze.APIKey = "daemon-key"
	id, revision := setupIdentityOf(t, s)
	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil || setup.State != SetupNeeded || setup.Models["bug"].Model != "analysis-model" || !setup.Models["analyze"].APIKeySet || setup.Models["analyze"].APIKey != "" {
		t.Fatalf("first setup: %+v %v", setup, err)
	}
	if !strings.HasSuffix(setup.ConfigFile, filepath.Join(".kasuri", "config.yaml")) || setup.Roles.Creation != "bug" || setup.Limits.ModelCalls == 0 {
		t.Fatalf("setup defaults: %+v", setup)
	}

	request := setupRequestFrom(setup)
	bug := request.Models["bug"]
	bug.Model = "project-bug-model"
	request.Models["bug"] = bug
	request.Roles.Review = "code"
	request.Limits.ModelCalls = 12
	saved, err := s.SaveProjectSetup(t.Context(), request)
	if err != nil || saved.State != SetupConfigured {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if s.conf().runtimes.bug.profile.Model != "project-bug-model" || s.conf().agentRuntime.Roles.Review != config.CodeModelScope || s.conf().agentRuntime.Limits.ModelCalls != 12 {
		t.Fatalf("the saved configuration was not applied: %+v", s.conf().agentRuntime)
	}
	if s.CurrentModelCatalog().ConfigFile != saved.ConfigFile {
		t.Fatalf("catalog source: %q", s.CurrentModelCatalog().ConfigFile)
	}
	data, err := os.ReadFile(filepath.Join(root, ".kasuri", "config.yaml"))
	if err != nil || !strings.Contains(string(data), "api_key: daemon-key") {
		t.Fatalf("the daemon's key must carry over when left empty: %s %v", data, err)
	}

	// A restarted daemon reads the project's file.
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(s.global, manager)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.conf().runtimes.bug.profile.Model != "project-bug-model" {
		t.Fatal("a new service ignored the project configuration")
	}

	stale := request
	stale.ProjectRevision = "sha256:old"
	if _, err := s.SaveProjectSetup(t.Context(), stale); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale identity: %v", err)
	}
	invalid := request
	invalid.Models = map[string]SetupModel{"analyze": request.Models["analyze"], "bug": {Model: "x"}, "code": request.Models["code"]}
	if _, err := s.SaveProjectSetup(t.Context(), invalid); !errors.Is(err, ErrSetupInvalid) || !strings.Contains(err.Error(), "model_scopes.bug.api_base_url") {
		t.Fatalf("invalid setup: %v", err)
	}
}

func TestProjectSetupCanBeSkippedAndReportsAnInvalidFile(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	id, revision := setupIdentityOf(t, s)
	skipped, err := s.SkipProjectSetup(t.Context(), id, revision)
	if err != nil || skipped.State != SetupSkipped {
		t.Fatalf("skip: %+v %v", skipped, err)
	}
	path := filepath.Join(root, ".kasuri", "config.yaml")
	if err := os.WriteFile(path, []byte("logging:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreActiveProject(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	id, revision = setupIdentityOf(t, s)
	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil || setup.State != SetupInvalid || !strings.Contains(setup.Error, `"logging" configures the daemon`) {
		t.Fatalf("invalid file: %+v %v", setup, err)
	}
	if s.conf().config != s.global {
		t.Fatal("an invalid project file must leave the daemon's configuration in use")
	}
}

func TestSwitchingProjectsAppliesEachProjectsConfiguration(t *testing.T) {
	s, first := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	id, revision := setupIdentityOf(t, s)
	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil {
		t.Fatal(err)
	}
	request := setupRequestFrom(setup)
	code := request.Models["code"]
	code.Model = "first-code-model"
	request.Models["code"] = code
	if _, err := s.SaveProjectSetup(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateProject(second, &project.Analysis{Name: "second", Path: second}); err != nil {
		t.Fatal(err)
	}
	if s.conf().config != s.global || s.conf().runtimes.function.profile.Model == "first-code-model" {
		t.Fatal("a project without a file must run with the daemon's configuration")
	}
	if err := s.RestoreActiveProject(first, &project.Analysis{Name: "fixture", Path: first}); err != nil {
		t.Fatal(err)
	}
	if s.conf().runtimes.function.profile.Model != "first-code-model" {
		t.Fatal("switching back did not apply the project's configuration")
	}
}

func TestProjectSetupWaitsForRunningWork(t *testing.T) {
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	id, revision := setupIdentityOf(t, s)
	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.analysisRun.mu.Lock()
	s.analysisRun.cancel = cancel
	s.analysisRun.mu.Unlock()
	defer func() {
		s.analysisRun.mu.Lock()
		s.analysisRun.cancel = nil
		s.analysisRun.mu.Unlock()
	}()
	if _, err := s.SaveProjectSetup(t.Context(), setupRequestFrom(setup)); !errors.Is(err, ErrSetupBusy) || !strings.Contains(err.Error(), "an analysis is running") {
		t.Fatalf("busy save: %v", err)
	}
}

func TestSetupModelTestReportsTheOutcome(t *testing.T) {
	server, calls := analysisResponseServer(t, func(AnalysisStage) string { return "OK" })
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	id, revision := setupIdentityOf(t, s)
	model := SetupModel{APIBaseURL: server.URL, Model: "tested-model"}
	result, err := s.TestSetupModel(t.Context(), id, revision, "code", model)
	if err != nil || !result.OK || calls.Load() != 1 {
		t.Fatalf("reachable model: %+v %v calls=%d", result, err, calls.Load())
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String()
	_ = listener.Close()
	result, err = s.TestSetupModel(t.Context(), id, revision, "code", SetupModel{APIBaseURL: closed, Model: "m"})
	if err != nil || result.OK || result.Reason != reasonModelUnreachable {
		t.Fatalf("unreachable model: %+v %v", result, err)
	}
	if _, err := s.TestSetupModel(t.Context(), id, revision, "code", SetupModel{Model: "m"}); !errors.Is(err, ErrSetupInvalid) {
		t.Fatalf("invalid model: %v", err)
	}
	if _, err := s.TestSetupModel(t.Context(), id, revision, "other", model); !errors.Is(err, ErrSetupInvalid) {
		t.Fatalf("invalid scope: %v", err)
	}
}

func TestSavingSetupResetsStaleAgentOverridesAndTestsIgnoreThem(t *testing.T) {
	server, calls := analysisResponseServer(t, func(AnalysisStage) string { return "OK" })
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	// An override saved in Settings, then a model change made outside setup.
	request := settingsControl(t, s)
	request.Values.Limits.ModelCalls = 10
	if _, err := s.ControlAgentSettings(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	s.conf().runtimes.bug.profile.Model = "changed-by-hand"
	if _, err := s.freezeAgentSettings(t.Context(), s.manager.Root()); !errors.Is(err, ErrAgentSettingsStale) || !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale overrides: %v", err)
	}

	id, revision := setupIdentityOf(t, s)
	test, err := s.TestSetupModel(t.Context(), id, revision, "code", SetupModel{APIBaseURL: server.URL, Model: "tested-model"})
	if err != nil || !test.OK || calls.Load() != 1 {
		t.Fatalf("a connection test must not wait for the overrides: %+v %v", test, err)
	}

	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveProjectSetup(t.Context(), setupRequestFrom(setup))
	if err != nil || !saved.OverridesReset {
		t.Fatalf("save: %+v %v", saved, err)
	}
	view, err := s.AgentSettings(t.Context(), settingsQuery(t, s))
	if err != nil || view.State != "configured" {
		t.Fatalf("overrides after setup: %+v %v", view, err)
	}
	if _, err := s.freezeAgentSettings(t.Context(), s.manager.Root()); err != nil {
		t.Fatalf("agent work after setup: %v", err)
	}
	again, err := s.SaveProjectSetup(t.Context(), setupRequestFrom(saved))
	if err != nil || again.OverridesReset {
		t.Fatalf("a second save has nothing to reset: %+v %v", again, err)
	}
}

func TestProjectTimeoutsAreSavedFromTheAppAndAppliedLive(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	id, revision := setupIdentityOf(t, s)
	setup, err := s.ProjectSetup(t.Context(), id, revision)
	if err != nil || setup.Timeouts.AnalysisSeconds != 300 || setup.Timeouts.WorkflowModelSeconds != config.DefaultWorkflowModelSeconds || setup.Timeouts.FocusedCheckSeconds != 60 {
		t.Fatalf("effective time limits: %+v %v", setup.Timeouts, err)
	}
	timeouts := setup.Timeouts
	timeouts.AnalysisSeconds = 900
	saved, err := s.SaveProjectTimeouts(t.Context(), ProjectTimeoutsRequest{ProjectID: id, ProjectRevision: revision, Timeouts: timeouts})
	if err != nil || saved.State != SetupConfigured || saved.Timeouts != timeouts {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if s.conf().analysisTimeout != 15*time.Minute || s.conf().runtimes.bug.effective.Timeout != "15m0s" {
		t.Fatalf("the time limit was not applied: %v %s", s.conf().analysisTimeout, s.conf().runtimes.bug.effective.Timeout)
	}
	data, err := os.ReadFile(filepath.Join(root, ".kasuri", "config.yaml"))
	if err != nil || !strings.Contains(string(data), "analysis_seconds: 900") || strings.Contains(string(data), "model_scopes") {
		t.Fatalf("only the time limits are written: %s %v", data, err)
	}

	// Setup without time limits keeps them; with them, it sets them.
	request := setupRequestFrom(saved)
	if _, err := s.SaveProjectSetup(t.Context(), request); err != nil || s.conf().analysisTimeout != 15*time.Minute {
		t.Fatalf("setup dropped the time limits: %v %v", s.conf().analysisTimeout, err)
	}
	timeouts.FocusedCheckSeconds = 120
	request.Timeouts = &timeouts
	if _, err := s.SaveProjectSetup(t.Context(), request); err != nil || s.conf().focusedCheckTimeout != 2*time.Minute {
		t.Fatalf("setup time limits: %v %v", s.conf().focusedCheckTimeout, err)
	}

	invalid := timeouts
	invalid.GenerationSeconds = 0
	if _, err := s.SaveProjectTimeouts(t.Context(), ProjectTimeoutsRequest{ProjectID: id, ProjectRevision: revision, Timeouts: invalid}); !errors.Is(err, ErrSetupInvalid) || !strings.Contains(err.Error(), "timeouts.generation_seconds") {
		t.Fatalf("invalid time limit: %v", err)
	}
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.analysisRun.mu.Lock()
	s.analysisRun.cancel = cancel
	s.analysisRun.mu.Unlock()
	defer func() {
		s.analysisRun.mu.Lock()
		s.analysisRun.cancel = nil
		s.analysisRun.mu.Unlock()
	}()
	if _, err := s.SaveProjectTimeouts(t.Context(), ProjectTimeoutsRequest{ProjectID: id, ProjectRevision: revision, Timeouts: timeouts}); !errors.Is(err, ErrSetupBusy) {
		t.Fatalf("busy save: %v", err)
	}
}
