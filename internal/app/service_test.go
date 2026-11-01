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
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestRemoteProviderRequiresConfirmation(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig("https://example.com/v1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []config.ModelScope{config.AnalyzeModelScope, config.BugModelScope, config.CodeModelScope} {
		if err := service.requireRemoteConfirmation(scope, false); err == nil {
			t.Fatalf("expected %s confirmation error", scope)
		}
		if err := service.requireRemoteConfirmation(scope, true); err != nil {
			t.Fatal(err)
		}
	}
	if !service.CurrentModelCatalog().Scopes["code"].RemoteProvider {
		t.Fatal("effective model must disclose that the configured provider is remote")
	}
}

func TestModelRuntimeRetainsCompletionMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "fixture" || request.Messages[0].Content != "request" || request.Stream {
			t.Errorf("unexpected transport request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{
			Model: "reported-model", Usage: llm.ChatUsage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42},
			Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: "answer"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()
	runtime := newModelRuntime(config.ModelProfile{APIBaseURL: server.URL, Model: "fixture"}, time.Minute, 3)
	result, err := runtime.execute(context.Background(), agentruntime.Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}})
	if err != nil || result.Content != "answer" || result.Model != "reported-model" || result.Usage.TotalTokens != 42 || result.FinishReason != "stop" {
		t.Fatalf("result=%+v, error=%v", result, err)
	}
}

func TestNativeGoogleRuntimeImportsAndRetainsGuardedMetadata(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, ":generateContent") || r.Header.Get("X-Goog-Api-Key") != "explicit-key" {
			t.Error("native import used wrong protocol")
		}
		json.NewEncoder(w).Encode(map[string]any{"modelVersion": "reported-google", "candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": projectReportFixture}}}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	profile := service.conf().runtimes.analyze.profile
	profile.Provider, profile.APIKey = config.GeminiProvider, "explicit-key"
	service.conf().runtimes.analyze = newModelRuntime(profile, time.Minute, 3)
	analysis, err := service.ImportProject(context.Background(), root, true)
	if err != nil || analysis.AIStatus != project.ProjectAnalysisStatusFresh || analysis.Report.Model != "reported-google" || calls != 1 {
		t.Fatalf("analysis=%+v error=%v calls=%d", analysis, err, calls)
	}
	paths, err := filepath.Glob(filepath.Join(root, ".kasuri", "agents", "*", "journal.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("native import did not persist a session", err)
	}
	before, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "explicit-key") {
		t.Fatal("provider credential leaked into the agent journal")
	}
	if _, err := service.RestoreProject(root); err != nil || calls != 1 {
		t.Fatalf("restore dispatched: calls=%d error=%v", calls, err)
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || string(after) != string(before) {
		t.Fatal("passive restore rewrote the agent journal", err)
	}
	profile.APIBaseURL = "https://generativelanguage.googleapis.com"
	service.conf().runtimes.analyze = newModelRuntime(profile, time.Minute, 3)
	if err := service.requireRemoteConfirmation(config.AnalyzeModelScope, false); err == nil {
		t.Fatal("native remote admission skipped consent")
	}
	if err := service.requireRemoteConfirmation(config.AnalyzeModelScope, true); err != nil {
		t.Fatal(err)
	}
	metadata := service.CurrentModelCatalog().Scopes["analyze"]
	if !metadata.Capabilities.Streaming || !metadata.Capabilities.Tools {
		t.Fatalf("capabilities=%+v", metadata.Capabilities)
	}
}

func TestNativeGoogleRequestRejectionIsTerminalAndSecretsDoNotChangeModelID(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 3)
	profile := service.conf().runtimes.function.profile
	profile.Provider, profile.APIKey = config.GeminiProvider, "first-secret"
	runtime := newModelRuntime(profile, time.Minute, 3)
	_, err := service.retryRequest(context.Background(), runtime, []llm.ChatMessage{{Role: "user", Content: "request"}}, nil)
	if !errors.Is(err, llm.ErrRequestRejected) || requests != 1 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
	first, err := configuredModelID(profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.APIKey, profile.AccessToken = "second-secret", "another-secret"
	second, err := configuredModelID(profile)
	if err != nil || first != second {
		t.Fatal("credentials changed model identity")
	}
}

func TestRetryDoesNotResendPromptAfterProviderRedirectRejection(t *testing.T) {
	var firstRequests, secondRequests int
	second := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		secondRequests++
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstRequests++
		w.Header().Set("Location", second.URL+"/redirected?destination_secret=never-expose")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer first.Close()

	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(first.URL)
	cfg.Retry = config.RetryConfig{MaxRetries: 3, BackoffBase: 1, BackoffMax: 1}
	service, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.retryRequest(context.Background(), service.conf().runtimes.function, []llm.ChatMessage{{Role: "user", Content: "prompt must not be delivered twice"}}, nil)
	if !errors.Is(err, llm.ErrRedirectRejected) {
		t.Fatalf("retry error = %v", err)
	}
	if firstRequests != 1 || secondRequests != 0 {
		t.Fatalf("request counts first=%d second=%d, want one first request and no second request", firstRequests, secondRequests)
	}
}

func TestModelCatalogAndConfirmationAreScopeSpecific(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(&config.Config{
		ModelScopes: config.ModelScopesConfig{
			Analyze: config.ModelProfileConfig{APIBaseURL: "https://analyze.example/v1", APIKey: "do-not-return", Model: "analyze-model", ReasoningEffort: "high"},
			Bug:     config.ModelProfileConfig{APIBaseURL: "http://127.0.0.1:11434/v1", Model: "bug-model", ReasoningEffort: "low"},
			Code:    config.ModelProfileConfig{APIBaseURL: "https://function.example/v1", APIKey: "do-not-return", Model: "function-model", ReasoningEffort: "max"},
		},
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	catalog := service.CurrentModelCatalog()
	if len(catalog.Scopes) != 3 {
		t.Fatalf("catalog top-level compatibility = %+v", catalog)
	}
	if catalog.Scopes["analyze"].ProviderOrigin != "https://analyze.example" || catalog.Scopes["analyze"].ReasoningEffort != "high" || catalog.Scopes["bug"].ReasoningEffort != "low" || catalog.Scopes["code"].ReasoningEffort != "max" || catalog.Scopes["bug"].RemoteProvider || !catalog.Scopes["code"].RemoteProvider {
		t.Fatalf("scope catalog = %+v", catalog.Scopes)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-return") || strings.Contains(string(encoded), "/v1") {
		t.Fatalf("catalog leaked configured endpoint or key: %s", encoded)
	}
	if err := service.requireRemoteConfirmation(config.BugModelScope, false); err != nil {
		t.Fatalf("local bug provider was gated: %v", err)
	}
	if err := service.requireRemoteConfirmation(config.AnalyzeModelScope, false); err == nil {
		t.Fatal("remote analyze provider was not gated")
	}
	if err := service.requireRemoteConfirmation(config.CodeModelScope, false); err == nil {
		t.Fatal("remote function provider was not gated")
	}
}

func TestEffectiveModelKeepsCatalogAndPreviewMetadataShapes(t *testing.T) {
	temperature := float32(0.2)
	metadata := EffectiveModel{
		Scope: "code", Profile: "code", Model: "fixture-model", ReasoningEffort: "high",
		ProviderOrigin: "https://provider.example", RemoteProvider: true,
		Temperature: &temperature, MaxTokens: intPointer(4096), ContextMaxTokens: 8192, Timeout: "5m0s", MaxRetries: 3,
	}
	wantMetadata := `{"scope":"code","profile":"code","model":"fixture-model","reasoning_effort":"high","provider_origin":"https://provider.example","remote_provider":true,"temperature":0.2,"max_tokens":4096,"context_max_tokens":8192,"timeout":"5m0s","max_retries":3}`
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil || string(encodedMetadata) != wantMetadata {
		t.Fatalf("effective model JSON = %s, %v", encodedMetadata, err)
	}

	for name, value := range map[string]any{
		"catalog": ModelCatalog{Scopes: map[string]EffectiveModel{"code": metadata}},
		"preview": AnalysisProviderRequirement{Model: metadata},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if !strings.Contains(string(encoded), wantMetadata) {
			t.Fatalf("%s does not preserve model metadata: %s", name, encoded)
		}
	}

}

func TestScopedModelLocalMixedAndRemoteConfirmationMatrixUsesNoProvider(t *testing.T) {
	tests := []struct {
		name   string
		scopes config.ModelScopesConfig
		remote map[string]bool
	}{
		{
			name: "all local",
			scopes: config.ModelScopesConfig{
				Analyze: config.ModelProfileConfig{APIBaseURL: "http://127.0.0.1:11434/v1", Model: "local-analyze"},
				Bug:     config.ModelProfileConfig{APIBaseURL: "http://localhost:1234/v1", Model: "local-bug"},
				Code:    config.ModelProfileConfig{APIBaseURL: "http://[::1]:8080/v1", Model: "local-function"},
			},
			remote: map[string]bool{"analyze": false, "bug": false, "code": false},
		},
		{
			name: "mixed cloud cloud local",
			scopes: config.ModelScopesConfig{
				Analyze: config.ModelProfileConfig{APIBaseURL: "https://analyze.example/v1", Model: "cloud-analyze"},
				Bug:     config.ModelProfileConfig{APIBaseURL: "https://bug.example/v1", Model: "cloud-bug"},
				Code:    config.ModelProfileConfig{APIBaseURL: "http://127.0.0.1:11434/v1", Model: "local-function"},
			},
			remote: map[string]bool{"analyze": true, "bug": true, "code": false},
		},
		{
			name: "all remote",
			scopes: config.ModelScopesConfig{
				Analyze: config.ModelProfileConfig{APIBaseURL: "https://analyze.example/v1", Model: "cloud-analyze"},
				Bug:     config.ModelProfileConfig{APIBaseURL: "https://bug.example/v1", Model: "cloud-bug"},
				Code:    config.ModelProfileConfig{APIBaseURL: "https://function.example/v1", Model: "cloud-function"},
			},
			remote: map[string]bool{"analyze": true, "bug": true, "code": true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := project.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			service, err := New(&config.Config{ModelScopes: test.scopes}, manager)
			if err != nil {
				t.Fatal(err)
			}
			catalog := service.CurrentModelCatalog()
			for _, scope := range []config.ModelScope{config.AnalyzeModelScope, config.BugModelScope, config.CodeModelScope} {
				profile := catalog.Scopes[string(scope)]
				if profile.RemoteProvider != test.remote[string(scope)] {
					t.Fatalf("%s remote metadata = %+v", scope, profile)
				}
				err := service.requireRemoteConfirmation(scope, false)
				if test.remote[string(scope)] && err == nil || !test.remote[string(scope)] && err != nil {
					t.Fatalf("%s confirmation error = %v", scope, err)
				}
			}
		})
	}
}

func TestAnalyzeProjectStoresStructuredReport(t *testing.T) {
	var received llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "fixture-model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs a small daemon.","architecture":"One Go package.","components":["daemon"],"walkthroughs":[],"risks":[]}`}}}})
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(server.URL)
	cfg.ModelScopes.Analyze.Model = "analysis-model"
	service, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}

	analysis, err := service.ImportProject(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Report.Status != project.ProjectAnalysisStatusFresh || analysis.Report.Model != "fixture-model" || analysis.Report.ConfiguredModel != "analysis-model" || analysis.Report.Scope != "analyze" || analysis.Report.Profile != "analyze" || analysis.Report.Purpose != "Runs a small daemon." {
		t.Fatalf("project report = %+v", analysis.Report)
	}
	if len(received.Messages) != 2 || !strings.Contains(received.Messages[0].Content, "exactly one JSON object") {
		t.Fatalf("project analysis prompt = %+v", received.Messages)
	}
}

func TestAnalyzeProjectKeepsInventoryWhenProjectSummaryTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	providerCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// Expire only after dispatch: filesystem and race-detector overhead
		// must not turn a provider-timeout test into an admission-timeout test.
		cancel(context.DeadlineExceeded)
		<-r.Context().Done()
		providerCanceled <- struct{}{}
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := service.ImportProject(ctx, root, true)
	if err != nil {
		t.Fatalf("ImportProject() error = %v", err)
	}
	if analysis.FileCount != 1 || analysis.Report.Status != project.ProjectAnalysisStatusFailed || analysis.Report.Failure == "" {
		t.Fatalf("timed-out analysis = %+v", analysis)
	}
	select {
	case <-providerCanceled:
	case <-time.After(time.Second):
		t.Fatal("project summary request was not canceled")
	}
}

func TestScopedModelRuntimesRouteOnlyAssignedOperations(t *testing.T) {
	var analyzeCalls, bugCalls, functionCalls int
	var analyzeRequest, bugRequest, functionRequest llm.ChatRequest
	analyzeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		analyzeCalls++
		if err := json.NewDecoder(r.Body).Decode(&analyzeRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "analyze-returned", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Understands the fixture.","architecture":"One package.","components":[],"walkthroughs":[],"risks":[]}`}}}})
	}))
	defer analyzeServer.Close()
	bugServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bugCalls++
		if err := json.NewDecoder(r.Body).Decode(&bugRequest); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(bugRequest.Messages[0].Content, "captured file scope") {
			t.Fatal("semantic analysis received a function request")
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "bug-returned", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSemanticAnalysis}}}})
	}))
	defer bugServer.Close()
	functionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		functionCalls++
		if err := json.NewDecoder(r.Body).Decode(&functionRequest); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(functionRequest.Messages[0].Content, "captured file scope") {
			t.Fatal("function proposal did not receive the direct scoped request")
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "function-returned", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"explanation":"Updates Run.","changes":[{"path":"main.go","content":"package main\n\nfunc Run() { println(\"updated\") }\n"}]}`}}}})
	}))
	defer functionServer.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(&config.Config{
		ModelScopes: config.ModelScopesConfig{
			Analyze: config.ModelProfileConfig{APIBaseURL: analyzeServer.URL + "/v1", Model: "analyze-model", ReasoningEffort: "high"},
			Bug:     config.ModelProfileConfig{APIBaseURL: bugServer.URL + "/v1", Model: "bug-model", ReasoningEffort: "medium"},
			Code:    config.ModelProfileConfig{APIBaseURL: functionServer.URL + "/v1", Model: "function-model", ReasoningEffort: "low"},
		},
		Retry: config.RetryConfig{MaxRetries: 0, BackoffBase: 1, BackoffMax: 1},
	}, manager)
	if err != nil {
		t.Fatal(err)
	}

	analysis, err := service.ImportProject(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Report.Scope != "analyze" || analysis.Report.Model != "analyze-returned" || analysis.Report.ConfiguredModel != "analyze-model" || analysis.Report.ProviderOrigin != analyzeServer.URL || analysis.Report.ReasoningEffort != "high" {
		t.Fatalf("analysis provenance = %+v", analysis.Report)
	}
	if err := manager.Set(root, analysis); err != nil {
		t.Fatal(err)
	}
	stage, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || stage.Semantic == nil {
		t.Fatalf("file analysis = %+v, %v", stage, err)
	}
	if fileAnalysis := stage.Semantic; fileAnalysis.Scope != "bug" || fileAnalysis.ConfiguredModel != "bug-model" || fileAnalysis.Model != "bug-returned" || fileAnalysis.ProviderOrigin != bugServer.URL || fileAnalysis.ReasoningEffort != "medium" {
		t.Fatalf("file analysis provenance = %+v", fileAnalysis)
	}
	session := openChangeFixture(t, service, "main.go")
	proposal, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Clarify it."})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.ContextManifest.Scope != "code" || proposal.ContextManifest.Model != "function-model" || proposal.ContextManifest.ProviderOrigin != functionServer.URL {
		t.Fatalf("draft provenance = %+v", proposal.ContextManifest)
	}
	if analyzeCalls != 1 || bugCalls != 1 || functionCalls != 1 {
		t.Fatalf("scope calls = analyze:%d bug:%d function:%d", analyzeCalls, bugCalls, functionCalls)
	}
	if analyzeRequest.ReasoningEffort != "high" || bugRequest.ReasoningEffort != "medium" || functionRequest.ReasoningEffort != "low" {
		t.Fatalf("scope reasoning efforts = analyze:%q bug:%q function:%q", analyzeRequest.ReasoningEffort, bugRequest.ReasoningEffort, functionRequest.ReasoningEffort)
	}
}

func intPointer(value int) *int { return &value }

func scopedTestConfig(apiBaseURL string) *config.Config {
	cfg := config.Default()
	cfg.ModelScopes.Analyze.APIBaseURL = apiBaseURL
	cfg.ModelScopes.Bug.APIBaseURL = apiBaseURL
	cfg.ModelScopes.Code.APIBaseURL = apiBaseURL
	return cfg
}
