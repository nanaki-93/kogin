package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestSemanticStageCachesStructuredOneFileSummary(t *testing.T) {
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var gotSchema, wantSchema any
		if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict || request.ResponseFormat.JSONSchema.Name != fileAnalysisResponseSchemaName || json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &gotSchema) != nil || json.Unmarshal([]byte(fileAnalysisResponseSchemaDocument), &wantSchema) != nil || !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Fatalf("file analysis response format = %+v", request.ResponseFormat)
		}
		prompt = request.Messages[0].Content
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "fixture-model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs the selected command.","responsibilities":["dispatches work"],"dependencies":["fmt"],"side_effects":["writes stdout"],"risks":[{"category":"bugs","severity":"High","title":"Missing input validation","summary":"No input validation."}],"suggestions":[{"title":"Validate input","summary":"Reject blank names.","action":"fix"}]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	stage, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || stage.Semantic == nil {
		t.Fatalf("analysis = %+v, %v", stage, err)
	}
	result := stage.Semantic
	if result.Status != project.AnalysisStatusFresh || result.Purpose == "" || result.Model != "fixture-model" || len(result.Risks) != 1 || result.Risks[0].Severity != "high" || result.Risks[0].Title != "Missing input validation" {
		t.Fatalf("analysis = %+v", result)
	}
	if strings.Contains(prompt, "helper secret") || !strings.Contains(prompt, "func Run") || !strings.Contains(prompt, "TARGET_SOURCE (the only source content supplied)") || !strings.Contains(prompt, "target_path copied exactly") || !strings.Contains(prompt, "Ground selected-file behavior and engineering insights in TARGET_SOURCE") || !strings.Contains(prompt, "Default to tests that characterize current behavior") {
		t.Fatalf("semantic prompt was not one-file scoped: %s", prompt)
	}
	if _, err := os.Stat(filepath.Join(root, ".kogin", "file-analysis")); err != nil {
		t.Fatalf("analysis cache not written: %v", err)
	}
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if file := findAppIndexFile(index, "main.go"); file == nil || file.AnalysisStatus != project.AnalysisStatusFresh {
		t.Fatalf("index analysis status = %+v, want fresh", file)
	}
	cached, err := semanticStageFor(t, service, "main.go", false)
	if second := cached.Semantic; err != nil || !cached.Progress.Cached || second == nil || second.Status != project.AnalysisStatusFresh || len(second.Risks) != 1 || second.Risks[0].Title != "Missing input validation" {
		t.Fatalf("cached analysis = %+v, %v", cached, err)
	}
}

func TestSemanticStageRefreshClassifiesLegacyCacheWithoutPassiveModelCalls(t *testing.T) {
	var calls atomic.Int32
	reply := strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"security","severity":"low","summary":"A visible trust-boundary concern."}]`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	prepared, err := service.prepareFileAnalysis("main.go")
	if err != nil {
		t.Fatal(err)
	}
	legacy := newFileAnalysis(prepared, semanticAnalysisResponse{Purpose: "Previous explanation", Risks: []project.Finding{{Severity: "low", Summary: "Historical concern."}}})
	legacy.PromptVersion = "file-analysis-v13"
	if err := prepared.cache.Store(legacy); err != nil {
		t.Fatal(err)
	}
	for read := 0; read < 2; read++ {
		cached, err := service.CachedFileAnalysis("main.go")
		if err != nil || cached.Status != project.AnalysisStatusStale || cached.Purpose != legacy.Purpose || len(cached.Risks) != 1 || cached.Risks[0].Category.Valid() || calls.Load() != 0 {
			t.Fatalf("legacy read = %+v, %v; calls=%d", cached, err, calls.Load())
		}
	}
	stage, err := semanticStageFor(t, service, "main.go", true)
	if fresh := stage.Semantic; err != nil || fresh == nil || fresh.Status != project.AnalysisStatusFresh || fresh.PromptVersion != "file-analysis-v18" || len(fresh.Risks) != 1 || fresh.Risks[0].Category != project.FindingCategorySecurity || calls.Load() != 1 {
		t.Fatalf("explicit refresh = %+v, %v; calls=%d", stage, err, calls.Load())
	}
	if _, err := service.CachedFileAnalysis("main.go"); err != nil || calls.Load() != 1 {
		t.Fatal("reading categorized cache made another model call")
	}
}

func TestSemanticStageDoesNotRetryUnsupportedStructuredFormat(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.bug.effective.MaxRetries = 3
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSemantic)
	request.RemainingAttempts = 4
	result, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageFailed || requests != 1 {
		t.Fatalf("unsupported structured format result=%+v err=%v requests=%d", result, err, requests)
	}
}

func TestCachedFileAnalysisMarksV11PromptResultsStale(t *testing.T) {
	service, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	prepared, err := service.prepareFileAnalysis("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := EngineeringInsightPromptVersion(); got != "file-analysis-v18" {
		t.Fatalf("file analysis prompt version = %q, want file-analysis-v18", got)
	}
	legacy := project.FileAnalysis{
		SchemaVersion: "1", ProjectID: prepared.input.ProjectID, ProjectRevision: prepared.input.ProjectRevision,
		Path: prepared.input.Path, ContentHash: prepared.input.ContentHash, Language: prepared.input.Language,
		Status: project.AnalysisStatusFresh, Model: prepared.input.Model, ConfiguredModel: prepared.input.Model,
		Profile: prepared.input.Profile, Scope: prepared.input.Scope, ProviderOrigin: prepared.input.ProviderOrigin,
		ReasoningEffort: prepared.input.ReasoningEffort, PromptVersion: "file-analysis-v11", ContextPolicyVersion: prepared.input.ContextPolicyVersion,
	}
	if err := prepared.cache.Store(legacy); err != nil {
		t.Fatal(err)
	}
	cached, err := service.CachedFileAnalysis("main.go")
	if err != nil || cached.Status != project.AnalysisStatusStale {
		t.Fatalf("v11 cache = %+v, %v; want stale", cached, err)
	}
}

func TestSemanticStageRequiresRemoteConfirmationBeforeSendingPrompt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSemanticAnalysis}}}})
	}))
	defer server.Close()

	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.bug.effective.RemoteProvider = true
	if _, err := semanticStageFor(t, service, "main.go", false); err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("declined remote confirmation error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("declined confirmation sent %d provider requests", requests)
	}
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSemantic)
	request.ConfirmRemoteProvider = true
	if _, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); err != nil {
		t.Fatalf("confirmed analysis error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("confirmed request count = %d, want 1", requests)
	}
}

func TestSemanticStageValidatesAndCachesOneExactBugTaskWithoutAnotherModelCall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Explains the file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"high","summary":"Run ignores an error.","anchor":"func Run() { fmt.Println(\"run\") }","task_spec":{"schema_version":"2","target_path":"main.go","acceptance_criteria":["Return the formatting error to the caller."],"non_goals":["Do not edit helper files."],"test_candidate":{"path":"main_test.go","name":"TestRunReturnsError","content":"package main\n\nimport \"testing\"\n\nfunc TestRunReturnsError(t *testing.T) {}\n"}}}],"suggestions":[]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)

	stage, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || stage.Semantic == nil || len(stage.Semantic.Risks) != 1 {
		t.Fatalf("analysis = %+v, %v", stage, err)
	}
	task := stage.Semantic.Risks[0].TaskSpec
	if task == nil || task.TargetPath != "main.go" || task.TestCandidate == nil || task.TestCandidate.Path != "main_test.go" {
		t.Fatalf("validated task = %+v", task)
	}
	if risk := stage.Semantic.Risks[0]; risk.StartLine != 5 || risk.EndLine != 5 {
		t.Fatalf("risk lines = %d-%d", risk.StartLine, risk.EndLine)
	}
	content, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil || string(content) != "package main\n\nimport \"fmt\"\n\nfunc Run() { fmt.Println(\"run\") }\n" {
		t.Fatalf("analysis changed imported source: %q, %v", content, err)
	}
	cached, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || cached.Semantic == nil || cached.Semantic.Risks[0].TaskSpec == nil || calls != 1 {
		t.Fatalf("cached task = %+v, %v; calls = %d", cached, err, calls)
	}
}

func TestSemanticStageHonorsTimeout(t *testing.T) {
	handlerCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		handlerCanceled <- struct{}{}
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 1)
	result, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || result.Progress.Status != AnalysisStageFailed || result.Semantic != nil {
		t.Fatalf("analysis = %+v, %v; want a failed stage after the deadline", result, err)
	}
	waitForTestSignal(t, handlerCanceled, "analysis provider cancellation")
}

func findAppIndexFile(index *project.ProjectIndex, path string) *project.IndexFile {
	for i := range index.Files {
		if index.Files[i].Path == path {
			return &index.Files[i]
		}
	}
	return nil
}
