package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const validSecurityReview = `{"findings":[{"rule":"go.input.validation","category":"input-validation","title":"Input reaches an operation","source_anchor":{"path":"main.go","start_line":5,"end_line":5},"severity":"medium","confidence":"low","evidence_kind":"model_suspicion","observed_condition":"The operation uses input without an observed validation boundary.","preconditions_or_unknowns":"Attacker control and reachability are unknown.","remediation":"Validate untrusted input at the boundary.","verification_idea":"Use a safe unit test with a representative invalid value."}]}`

// securityAIStageResult reviews main.go as an analysis run's security_ai
// stage does.
func securityAIStageResult(t *testing.T, ctx context.Context, s *Service, confirmRemote bool) (analysisFileStageResult, error) {
	t.Helper()
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityAI)
	request.ConfirmRemoteProvider = confirmRemote
	return s.analyzeFileStage(ctx, request, analysisStageAuthorityFor(t, request))
}

func TestSecurityAIStagePinsPromptFocusAndReturnsSanitizedReport(t *testing.T) {
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt = request.Messages[0].Content
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "api_key=provider-secret", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSecurityReview}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	result, err := securityAIStageResult(t, context.Background(), service, false)
	if err != nil {
		t.Fatal(err)
	}
	report := result.Security
	if result.Progress.Status != AnalysisStageCompleted || report == nil || report.Status != project.SecurityStatusCompleted || len(report.Findings) != 1 || report.Findings[0].Anchor.StartLine != 5 || strings.Contains(report.Model, "provider-secret") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(prompt, "untrusted data") || !strings.Contains(prompt, "func Run") || strings.Contains(prompt, "helper secret") {
		t.Fatalf("security prompt lost its fixed boundary: %s", prompt)
	}
	data, err := os.ReadFile(filepath.Join(root, ".kasuri", "security", "files", securityCacheEntry(t, root)))
	if err != nil || strings.Contains(string(data), "provider-secret") || strings.Contains(string(data), "func Run") {
		t.Fatalf("persisted report was not source-free/redacted: %v %s", err, data)
	}
}

func TestSecurityAIStageFailsProviderEchoedSource(t *testing.T) {
	for _, echoedSource := range []string{
		`func Run() { fmt.Println(\"run\") }`,
		"func  Run ( ) { fmt . Println ( run ) }",
		"package    main",
	} {
		t.Run(echoedSource, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				output := strings.Replace(validSecurityReview, "The operation uses input without an observed validation boundary.", echoedSource, 1)
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
			}))
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			if result, err := securityAIStageResult(t, context.Background(), service, false); err != nil || result.Progress.Status != AnalysisStageFailed || result.Security != nil || result.Progress.FindingCount != nil {
				t.Fatalf("source-echoing provider response was accepted: %+v, %v", result, err)
			}
			assertNoSecurityReport(t, root)
		})
	}
}

func TestSecurityAIStageRejectsConsentAndStaleResultsWithoutPublishing(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSecurityReview}}}})
	}))
	defer server.Close()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.analyze.effective.RemoteProvider = true
	if result, err := securityAIStageResult(t, context.Background(), service, false); err == nil || calls.Load() != 0 || result.Security != nil {
		t.Fatalf("declined review = %+v, %v, calls = %d", result, err, calls.Load())
	}
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSecurityAI)
	request.ConfirmRemoteProvider = true
	authority := analysisStageAuthorityFor(t, request)
	done := make(chan error, 1)
	go func() { _, err := service.analyzeFileStage(context.Background(), request, authority); done <- err }()
	waitForTestSignal(t, started, "security review request")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	close(release)
	released = true
	if err := waitForTestError(t, done, "stale security review"); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("review error = %v", err)
	}
	assertNoSecurityReport(t, root)
}

func TestSecurityAIStageFailsMalformedOversizedAndFocusedOutOfRangeOutput(t *testing.T) {
	for _, output := range []string{
		`{"findings":[`,
		`{"findings":[{"rule":"go.input.validation","category":"input-validation","title":"Bad","source_anchor":{"path":"main.go","start_line":1,"end_line":99},"severity":"medium","confidence":"low","evidence_kind":"model_suspicion","observed_condition":"Observed.","preconditions_or_unknowns":"Unknown.","remediation":"Fix.","verification_idea":"Safely test."}]}`,
		strings.Repeat("x", project.SecurityMaxSourceBytes+1),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
		}))
		service, root := newSemanticAnalysisService(t, server.URL, 0)
		result, err := securityAIStageResult(t, context.Background(), service, false)
		server.Close()
		if err != nil || result.Progress.Status != AnalysisStageFailed || result.Security != nil {
			t.Fatalf("invalid security model response was accepted: %+v, %v", result, err)
		}
		assertNoSecurityReport(t, root)
	}
}

func TestSecurityAIStageSkipsOver64KiBSourceBeforeProviderDelivery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"findings":[]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	source := "package main\n" + strings.Repeat("// bounded source\n", 5000)
	if len(source) <= project.SecurityMaxSourceBytes {
		t.Fatal("fixture did not exceed the source limit")
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	if result, err := securityAIStageResult(t, context.Background(), service, false); err != nil || result.Progress.Status != AnalysisStageSkipped || calls.Load() != 0 {
		t.Fatalf("oversized source result = %+v, %v, provider calls = %d", result, err, calls.Load())
	}
	if _, err := service.prepareSecurityReview(securityReviewRequestFor(t, service, "")); err == nil || !strings.Contains(err.Error(), "exceeds 64 KiB") {
		t.Fatalf("oversized source preparation = %v", err)
	}
	assertNoSecurityReport(t, root)
}

func TestSecurityAIStageRollsBackCanceledOrStalePublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Service, string, context.CancelFunc)
		want   error
		status AnalysisStageStatus
	}{
		{name: "canceled", change: func(_ *Service, _ string, cancel context.CancelFunc) { cancel() }, want: context.Canceled, status: AnalysisStageCanceled},
		// The file left the analysis scope: the stage skips it without an error.
		{name: "policy", change: func(_ *Service, root string, _ context.CancelFunc) {
			_ = os.WriteFile(filepath.Join(root, ".kasuriignore"), []byte("main.go\n"), 0644)
		}, want: nil, status: AnalysisStageSkipped},
		{name: "provider", change: func(service *Service, _ string, _ context.CancelFunc) {
			service.conf().runtimes.analyze.effective.ProviderOrigin = "https://changed.example"
		}, want: project.ErrRevisionConflict, status: AnalysisStageStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSecurityReview}}}})
			}))
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service.beforeSecurityReviewPublication = func() { test.change(service, root, cancel) }
			result, err := securityAIStageResult(t, ctx, service, false)
			if !errors.Is(err, test.want) || result.Progress.Status != test.status || result.Security != nil {
				t.Fatalf("review = %+v, %v, want %v %s", result, err, test.want, test.status)
			}
			assertNoSecurityReport(t, root)
		})
	}
}

func TestSecurityAIStageCompletesEmptyIndexedFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"findings":[]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "main.go"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	result, err := securityAIStageResult(t, context.Background(), service, false)
	if err != nil || result.Progress.Status != AnalysisStageCompletedEmpty || result.Security == nil || result.Security.Status != project.SecurityStatusCompletedEmpty || len(result.Security.Findings) != 0 {
		t.Fatalf("empty-file result = %+v, %v", result, err)
	}
}

func TestSecurityAIStagePreservesSourceLinesAndReviewRejectsRuntimeTransition(t *testing.T) {
	var prompt string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		prompt = request.Messages[0].Content
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSecurityReview}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	source := "\n\npackage main\n\nfunc Run() { fmt.Println(\"run\") }\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	if result, err := securityAIStageResult(t, context.Background(), service, false); err != nil || result.Progress.Status != AnalysisStageCompleted {
		t.Fatalf("review = %+v, %v", result, err)
	}
	if !strings.Contains(prompt, "SOURCE:\n```\n"+source+"```") {
		t.Fatalf("prompt changed source line positions: %q", prompt)
	}
	snapshot, err := service.prepareSecurityReview(securityReviewRequestFor(t, service, "Run"))
	if err != nil {
		t.Fatal(err)
	}
	service.conf().runtimes.analyze.effective.RemoteProvider = true
	if _, _, err := service.executeSecurityReview(context.Background(), snapshot, nil); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 1 {
		t.Fatalf("runtime transition error = %v, provider calls = %d", err, calls.Load())
	}
}

func securityReviewRequestFor(t *testing.T, service *Service, _ string) SecurityReviewRequest {
	t.Helper()
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	file, err := service.manager.IndexedFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	return SecurityReviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, BaseFileHash: file.ContentHash, Path: file.Path}
}

func securityCacheEntry(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".kasuri", "security", "files"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("security cache entries = %v, %v", entries, err)
	}
	return entries[0].Name()
}

func assertNoSecurityReport(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".kasuri", "security", "files"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil || len(entries) != 0 {
		t.Fatalf("security reports = %v, %v", entries, err)
	}
}
