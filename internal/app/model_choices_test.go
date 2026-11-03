package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestAvailableModelsDeduplicatesJobsAndIncludesBothCLICatalogs(t *testing.T) {
	base, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	cfg := scopedTestConfig("http://127.0.0.1:1")
	cfg.ModelScopes.Analyze = config.ModelProfileConfig{Provider: config.AgyProvider, CLIPath: "/private/test-agy", Model: "configured-agy", ReasoningEffort: "medium"}
	cfg.ModelScopes.Bug = cfg.ModelScopes.Analyze
	cfg.ModelScopes.Code = config.ModelProfileConfig{Provider: config.PiProvider, CLIPath: "/private/test-pi", Model: "provider/default"}
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	s.discoverPiModels = func(_ context.Context, executable string) ([]llm.PiModel, error) {
		if executable != "/private/test-pi" {
			t.Fatalf("Pi executable = %q", executable)
		}
		return []llm.PiModel{
			{Provider: "lm-studio", ID: "qwen/local", Name: "Qwen Local", BaseURL: "http://127.0.0.1:1234/v1?api_key=private-token"},
			{Provider: "openai-codex", ID: "gpt-5", Name: "GPT-5", BaseURL: "https://provider.invalid/v1"},
			{Provider: "custom", ID: "model"},
		}, nil
	}
	s.discoverAgyModels = func(_ context.Context, executable string) ([]llm.AgyModel, error) {
		if executable != "/private/test-agy" {
			t.Fatalf("Agy executable = %q", executable)
		}
		return []llm.AgyModel{{ID: "gemini-new-high", Name: "Gemini New (High)"}}, nil
	}
	s.discoverClaudeModels = func(_ context.Context, executable string) ([]llm.ClaudeModel, error) {
		if executable != "claude" {
			t.Fatalf("Claude executable = %q", executable)
		}
		return []llm.ClaudeModel{{ID: "sonnet", Name: "Claude Sonnet"}}, nil
	}
	catalog, err := s.AvailableModels(context.Background())
	if err != nil || len(catalog.Models) != 7 || catalog.Pi.Status != "ready" || catalog.Agy.Status != "ready" || catalog.Claude.Status != "ready" {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	if catalog.Defaults["analyze"] != catalog.Defaults["bug"] || catalog.Defaults["bug"] == catalog.Defaults["code"] {
		t.Fatalf("models still depend on their original job: %+v", catalog.Defaults)
	}
	locations := map[string]string{"pi:lm-studio/qwen/local": "local", "pi:openai-codex/gpt-5": "remote", "pi:custom/model": "unknown"}
	for _, choice := range catalog.Models {
		if choice.Model.Scope != "" || choice.Model.Profile != choice.ID {
			t.Fatalf("catalog retained a job: %+v", choice)
		}
		if choice.Source == "pi" && (choice.Location != locations[choice.ID] || choice.Model.ProviderOrigin != "cli://pi" || !choice.Model.RemoteProvider) {
			t.Fatalf("Pi location/consent metadata = %+v", choice)
		}
		if choice.Source == "claude" && (choice.ID != "claude:sonnet" || choice.Location != "remote" || choice.Model.ProviderOrigin != "cli://claude" || !choice.Model.RemoteProvider) {
			t.Fatalf("Claude catalog/consent metadata = %+v", choice)
		}
		if choice.Source == "agy" && (choice.ID != "agy:gemini-new-high" || choice.Name != "Gemini New (High)" || choice.Location != "unknown" || choice.Model.ProviderOrigin != "cli://agy" || !choice.Model.RemoteProvider || choice.Model.ReasoningEffort != "") {
			t.Fatalf("Agy catalog/consent metadata = %+v", choice)
		}
	}
	encoded, _ := json.Marshal(catalog)
	for _, secret := range []string{"private-token", "/private/test-pi", "/private/test-agy", "api_key", "APIKey"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("catalog leaked %q", secret)
		}
	}
	piDiscovery := s.discoverPiModels
	s.discoverPiModels = func(context.Context, string) ([]llm.PiModel, error) { return nil, errors.New("private-token") }
	partial, err := s.AvailableModels(context.Background())
	if err != nil || len(partial.Models) != 4 || partial.Pi.Status != "unavailable" || partial.Agy.Status != "ready" || strings.Contains(partial.Pi.Message, "private-token") {
		t.Fatalf("partial catalog = %+v, %v", partial, err)
	}
	s.discoverPiModels = piDiscovery
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) { return nil, errors.New("private-token") }
	partial, err = s.AvailableModels(context.Background())
	if err != nil || len(partial.Models) != 6 || partial.Pi.Status != "ready" || partial.Agy.Status != "unavailable" || strings.Contains(partial.Agy.Message, "private-token") {
		t.Fatalf("partial Agy catalog = %+v, %v", partial, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AvailableModels(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled catalog = %v", err)
	}
}

func TestCatalogModelsDispatchIndependentlyOfConfiguredJobsAndPersist(t *testing.T) {
	analyze, analyzeCalls := analysisResponseServer(t, analysisReply)
	bug, bugCalls := analysisResponseServer(t, analysisReply)
	function, functionCalls := analysisResponseServer(t, analysisReply)
	base, root := newSemanticAnalysisService(t, analyze.URL, 0)
	cfg := scopedTestConfig(analyze.URL)
	cfg.ModelScopes.Bug.APIBaseURL = bug.URL
	cfg.ModelScopes.Code.APIBaseURL = function.URL
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	s.discoverPiModels = func(context.Context, string) ([]llm.PiModel, error) { return []llm.PiModel{}, nil }
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) { return []llm.AgyModel{}, nil }
	catalog, err := s.AvailableModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	models := &AnalysisModels{Code: catalog.Defaults["code"], Review: catalog.Defaults["bug"]}
	preview := analysisModelsPreviewFor(t, s, models, AnalysisRunLimits{100, 30, 2})
	if analyzeCalls.Load()+bugCalls.Load()+functionCalls.Load() != 0 {
		t.Fatal("preview dispatched inference")
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if run.Status != AnalysisRunCompleted || functionCalls.Load() != 1 || bugCalls.Load() != 2 || analyzeCalls.Load() != 0 {
		t.Fatalf("dispatch = %s: %d/%d/%d", run.Status, functionCalls.Load(), bugCalls.Load(), analyzeCalls.Load())
	}
	restored, err := loadAnalysisRun(root)
	if err != nil || restored.Plan.Models == nil || *restored.Plan.Models != *models {
		t.Fatalf("saved model choices = %+v, %v", restored, err)
	}
	restarted, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	cached := analysisModelsPreviewFor(t, restarted, models, preview.Limits)
	if cached.ExpectedModelRequests != 0 {
		t.Fatalf("restart lost selected-model caches: %+v", cached)
	}
	retargeted := analysisStartFor(cached)
	retargeted.Models.Code = "configured:" + strings.Repeat("0", 64)
	if _, err := restarted.StartAnalysisRun(context.Background(), retargeted); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("unknown model was admitted: %v", err)
	}
}

func TestCLICatalogChoicesRequireMembershipAndConsentBeforeDispatch(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.PiProvider, config.AgyProvider} {
		t.Run(string(provider), func(t *testing.T) {
			testCLICatalogChoiceAdmission(t, provider)
		})
	}
}

func testCLICatalogChoiceAdmission(t *testing.T, provider config.ModelProvider) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CLI providers require process-group cleanup")
	}
	base, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	calls := filepath.Join(t.TempDir(), "calls")
	cfg := config.Default()
	model := "gemini-new-high"
	if provider == config.PiProvider {
		model = "lm-studio/local-model"
	}
	cfg.ModelScopes.Code = cliAppProfile(t, provider, model, `{"explanation":"Updates the message.","changes":[{"path":"main.go","content":"package main\nimport \"fmt\"\nfunc Run() { fmt.Println(\"updated\") }\n"}]}`, calls)
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	available := []llm.PiModel{{Provider: "lm-studio", ID: "local-model", BaseURL: "http://127.0.0.1:1234/v1"}}
	s.discoverPiModels = func(context.Context, string) ([]llm.PiModel, error) { return available, nil }
	agyAvailable := []llm.AgyModel{{ID: model, Name: "Gemini New (High)"}}
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) { return agyAvailable, nil }
	session := openChangeFixture(t, s, "main.go")
	request := ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Update the message.", Profile: string(provider) + ":" + model}
	if _, err := s.SendChangeMessage(context.Background(), session.ID, request); err == nil {
		t.Fatal("CLI dispatched without consent")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("unconfirmed selection launched inference")
	}
	request.ConfirmRemoteProvider = true
	proposal, err := s.SendChangeMessage(context.Background(), session.ID, request)
	if err != nil || len(proposal.Changes) != 1 || proposal.ContextManifest.Model != model || proposal.ContextManifest.ProviderOrigin != "cli://"+string(provider) {
		t.Fatalf("CLI proposal = %+v, %v", proposal, err)
	}
	models := &AnalysisModels{Code: "bug", Review: request.Profile}
	preview := analysisModelsPreviewFor(t, s, models, AnalysisRunLimits{100, 30, 2})
	available = nil
	agyAvailable = nil
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("removed CLI model admitted: %v", err)
	}
	if run, err := s.ReadAnalysisRun(context.Background(), preview.Identity.ProjectID, preview.Identity.ProjectRevision); err != nil || run != nil {
		t.Fatal("invalid selection created a run")
	}
	request.ChangeIdentity = changeIdentity(proposal)
	if _, err := s.SendChangeMessage(context.Background(), session.ID, request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("removed CLI model generated a proposal: %v", err)
	}
	count, err := os.ReadFile(calls)
	if err != nil || string(count) != "x" {
		t.Fatalf("unexpected inference calls: %q, %v", count, err)
	}
}

func TestModelSelectionsCheckBothCatalogsOnceAndPreserveLimits(t *testing.T) {
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	piCalls, agyCalls := 0, 0
	s.discoverPiModels = func(_ context.Context, executable string) ([]llm.PiModel, error) {
		piCalls++
		if executable != "pi" {
			t.Fatalf("default Pi executable = %s", executable)
		}
		return []llm.PiModel{{Provider: "google", ID: "gemini"}}, nil
	}
	s.discoverAgyModels = func(_ context.Context, executable string) ([]llm.AgyModel, error) {
		agyCalls++
		if executable != "agy" {
			t.Fatalf("default Agy executable = %s", executable)
		}
		return []llm.AgyModel{{ID: "gemini", Name: "Gemini"}}, nil
	}
	selections := []string{"pi:google/gemini", "agy:gemini", "agy:gemini", "pi:google/gemini"}
	if err := s.validateModelSelections(context.Background(), selections); err != nil || piCalls != 1 || agyCalls != 1 {
		t.Fatalf("catalog validation = %v, calls = %d/%d", err, piCalls, agyCalls)
	}
	selected, ok := s.selectedRuntime(config.BugModelScope, "agy:gemini")
	base := s.conf().runtimes.bug
	if !ok || selected.profile.Provider != config.AgyProvider || selected.profile.Model != "gemini" || selected.profile.ContextMaxTokens != base.profile.ContextMaxTokens || selected.effective.Timeout != base.effective.Timeout || selected.effective.MaxRetries != base.effective.MaxRetries {
		t.Fatalf("Agy operation limits = %+v", selected)
	}
	for _, selection := range []string{"agy:", "agy:--prompt", "agy:Gemini Flash", "agy:gemini\n", "agy:../model"} {
		models := &AnalysisModels{Code: selection, Review: "analyze"}
		if err := models.Validate(); err == nil {
			t.Fatalf("invalid selection accepted: %q", selection)
		}
	}
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) { return nil, context.DeadlineExceeded }
	if err := s.validateModelSelections(context.Background(), []string{"agy:gemini"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery failure did not stop admission: %v", err)
	}
}
