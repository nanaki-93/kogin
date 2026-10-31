package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func settingsQuery(t *testing.T, s *Service) AgentRunQuery {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return AgentRunQuery{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision}
}

func settingsControl(t *testing.T, s *Service) AgentSettingsControl {
	t.Helper()
	view, err := s.AgentSettings(t.Context(), settingsQuery(t, s))
	if err != nil {
		t.Fatal(err)
	}
	values := &agentstate.AgentPreferences{Roles: view.Roles, Limits: view.Limits, DisabledSkills: []string{}}
	for _, skill := range view.Skills {
		if !skill.Enabled {
			values.DisabledSkills = append(values.DisabledSkills, skill.Directory)
		}
	}
	return AgentSettingsControl{ProjectID: view.ProjectID, ProjectRevision: view.ProjectRevision, Configuration: view.Configuration, Version: view.Version, Action: "replace", Confirm: true, Values: values}
}

func TestAgentSettingsArePassiveGuardedDurableAndBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	installSkillFixture(t, s, root)
	query := settingsQuery(t, s)
	view, err := s.AgentSettings(t.Context(), query)
	if err != nil || view.State != "configured" || view.Version != 0 || len(view.Models) != 3 || len(view.Skills) != 1 || !view.Skills[0].Enabled {
		t.Fatal(view, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".mini-orca", "agents")); !os.IsNotExist(err) {
		t.Fatal("passive settings created a journal", err)
	}
	if view.Models[0].MaxTokens != nil {
		*view.Models[0].MaxTokens = 1
		if *s.conf().runtimes.analyze.effective.MaxTokens == 1 {
			t.Fatal("view mutated provider configuration")
		}
	}
	request := settingsControl(t, s)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ControlAgentSettings(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled editor was admitted", err)
	}
	for _, alter := range []func(*AgentSettingsControl){
		func(r *AgentSettingsControl) { r.Confirm = false },
		func(r *AgentSettingsControl) { r.ProjectRevision = "stale" },
		func(r *AgentSettingsControl) { r.Configuration = "old-config" },
		func(r *AgentSettingsControl) { r.Version++ },
		func(r *AgentSettingsControl) { r.Values.Roles.Creation = "unconfigured" },
		func(r *AgentSettingsControl) { r.Values.Limits.ModelCalls++ },
		func(r *AgentSettingsControl) { r.Values.Limits.ToolCalls = 0 },
		func(r *AgentSettingsControl) { r.Values.DisabledSkills = []string{"new/skill"} },
	} {
		invalid := request
		copy := *request.Values
		invalid.Values = &copy
		alter(&invalid)
		if _, err := s.ControlAgentSettings(t.Context(), invalid); err == nil {
			t.Fatal("invalid settings mutation succeeded", invalid)
		}
	}
	request.Values.Roles.Creation = "code"
	request.Values.Limits.ModelCalls = 10
	request.Values.DisabledSkills = []string{"project-skills/review-code"}
	updated, err := s.ControlAgentSettings(t.Context(), request)
	if err != nil || updated.Version != 1 || updated.State != "overridden" || updated.Skills[0].Enabled || updated.Limits.ModelCalls != 10 {
		t.Fatal(updated, err)
	}
	// A new owner reads the same private checkout overrides without calling a
	// provider, acquiring a tool grant, or modifying project source.
	restarted := &Service{manager: s.manager}
	restarted.configured.Store(&configuration{runtimes: s.conf().runtimes, agentRuntime: s.conf().agentRuntime})
	reloaded, err := restarted.AgentSettings(t.Context(), query)
	if err != nil || reloaded.Roles.Creation != "code" || reloaded.Version != updated.Version {
		t.Fatal(reloaded, err)
	}
	if _, err := s.ControlAgentSettings(t.Context(), request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatal("stale writer replaced newer settings", err)
	}
	if calls.Load() != 0 {
		t.Fatal("settings dispatched a provider", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "SHOULD_NEVER_EXIST")); !os.IsNotExist(err) {
		t.Fatal("skill script executed", err)
	}
}

func TestSettingsFreezeNestedBudgetsRolesSkillsAndDeadlines(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	installSkillFixture(t, s, root)
	ctx, release, err := s.agentRunContext(t.Context(), s.conf().runtimes.function)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := s.admittedRuntime(ctx)
	models := ChangeWorkflowModels{Create: "bug", Test: "code", Review: "analyze"}
	deadline := s.changeWorkflowTimeout(ctx, models, 0, true)
	request := settingsControl(t, s)
	request.Values.Limits.ToolCalls, request.Values.Limits.Repairs, request.Values.Limits.ConcurrentCalls = 1, 0, 1
	request.Values.Roles.Creation = "code"
	request.Values.DisabledSkills = []string{"project-skills/review-code"}
	if _, err := s.ControlAgentSettings(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	nested, stop, err := s.agentRunContext(ctx, s.conf().runtimes.function)
	if err != nil {
		t.Fatal("active journal binding changed", err)
	}
	defer stop()
	if agentruntime.BudgetFromContext(nested) != agentruntime.BudgetFromContext(ctx) || s.admittedRuntime(nested).Limits != before.Limits || !slices.Equal(s.admittedRuntime(nested).Skills, before.Skills) || s.resolveWorkflowModels(nested, ChangeWorkflowModels{}).Create != "bug" || s.changeWorkflowTimeout(nested, models, 0, true) != deadline {
		t.Fatal("settings changed an active operation")
	}
	for range 2 {
		if err := agentruntime.BudgetFromContext(nested).AdmitTool(nested); err != nil {
			t.Fatal("old budget was reduced", err)
		}
	}
	next, cancel, err := s.agentRunContext(t.Context(), s.conf().runtimes.function)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if s.admittedRuntime(next).Limits.Repairs != 0 || len(s.admittedRuntime(next).Skills) != 0 || s.resolveWorkflowModels(next, ChangeWorkflowModels{}).Create != "code" || s.workflowReviewWaves(next, 3) != 3 {
		t.Fatal("future admission ignored settings")
	}
	if err := agentruntime.BudgetFromContext(next).AdmitTool(next); err != nil {
		t.Fatal(err)
	}
	if err := agentruntime.BudgetFromContext(next).AdmitTool(next); !errors.Is(err, agentruntime.ErrBudgetExceeded) {
		t.Fatal("new budget was not enforced", err)
	}
	if _, err := s.freezeAgentSettings(ctx, t.TempDir()); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatal("captured settings crossed checkout boundaries", err)
	}
}

func TestStaleSettingsRequireResetAndReindexRejectsOldEditors(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	request := settingsControl(t, s)
	if _, err := s.ControlAgentSettings(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request = settingsControl(t, s)
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ControlAgentSettings(t.Context(), request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatal("old revision could edit settings", err)
	}
	// Simulate an operator changing configuration between daemon starts.
	s.conf().agentRuntime.Limits.ModelCalls--
	view, err := s.AgentSettings(t.Context(), settingsQuery(t, s))
	if err != nil || view.State != "stale" {
		t.Fatal(view, err)
	}
	if _, err := s.freezeAgentSettings(t.Context(), root); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatal("stale configuration silently became defaults", err)
	}
	reset := settingsControl(t, s)
	reset.Action, reset.Values = "reset", nil
	if _, err := s.ControlAgentSettings(t.Context(), reset); err != nil {
		t.Fatal(err)
	}
	if ctx, err := s.freezeAgentSettings(t.Context(), root); err != nil || s.admittedRuntime(ctx).Limits.ModelCalls != s.conf().agentRuntime.Limits.ModelCalls {
		t.Fatal("explicit reset did not restore configured defaults", err)
	}
}

func TestDisablingSkillDoesNotChangeAnAdmittedInvestigation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if call == 1 {
			if !strings.Contains(string(body), "list_skills") {
				t.Error("initial settings omitted enabled skills")
			}
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			repositoryToolCall(w, "load_skill", `{"name":"review-code"}`)
			return
		}
		if call == 3 && strings.Contains(string(body), "list_skills") {
			t.Error("future investigation retained disabled skill tools")
		}
		json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validDeclarationExplanation}}}})
	}))
	t.Cleanup(func() { once.Do(func() { close(release) }); server.Close() })
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	installSkillFixture(t, s, root)
	request := declarationRequest(t, s)
	done := make(chan error, 1)
	work, cancel := context.WithCancel(t.Context())
	var worker sync.WaitGroup
	worker.Go(func() { _, err := s.ExplainDeclaration(work, request); done <- err })
	t.Cleanup(func() { cancel(); once.Do(func() { close(release) }); worker.Wait() })
	select {
	case <-started:
	case err := <-done:
		t.Fatal("investigation failed before provider", err)
	case <-time.After(10 * time.Second):
		t.Fatal("investigation did not reach provider")
	}
	control := settingsControl(t, s)
	control.Values.DisabledSkills = []string{"project-skills/review-code"}
	control.Values.Limits.ModelCalls = 1
	if _, err := s.ControlAgentSettings(t.Context(), control); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("settings interrupted captured skill work", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("investigation did not finish")
	}
	if _, err := s.ExplainDeclaration(t.Context(), request); err != nil || calls.Load() != 3 {
		t.Fatal("future settings did not take effect", calls.Load(), err)
	}
}

func TestAgentRolesCanUseAnyAvailableCatalogModel(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	view, err := s.AgentSettings(t.Context(), settingsQuery(t, s))
	if err != nil || view.RoleModels["creation"].Model != s.conf().runtimes.bug.effective.Model || view.RoleModels["review"].Model != s.conf().runtimes.analyze.effective.Model {
		t.Fatalf("default role models: %+v %v", view.RoleModels, err)
	}
	reviewer, err := configuredModelID(s.conf().runtimes.function.profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []string{"configured:" + strings.Repeat("0", 64), "agy:missing-model", "gpt-unknown"} {
		request := settingsControl(t, s)
		request.Values.Roles.Review = unavailable
		if _, err := s.ControlAgentSettings(t.Context(), request); err == nil {
			t.Fatalf("unavailable role model %q was saved", unavailable)
		}
	}
	request := settingsControl(t, s)
	request.Values.Roles.Review = reviewer
	updated, err := s.ControlAgentSettings(t.Context(), request)
	if err != nil || updated.Roles.Review != reviewer || updated.State != "overridden" {
		t.Fatalf("catalog role: %+v %v", updated, err)
	}
	if model := updated.RoleModels["review"]; model.Model != s.conf().runtimes.function.effective.Model || model.Profile != reviewer || model.Scope != "analyze" {
		t.Fatalf("review role model: %+v", model)
	}
	ctx, err := s.freezeAgentSettings(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if models := s.resolveWorkflowModels(ctx, ChangeWorkflowModels{}); models.Review != reviewer || models.Create != "bug" {
		t.Fatalf("a run did not use the role's model: %+v", models)
	}
}
