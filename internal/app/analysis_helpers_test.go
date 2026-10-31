package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const validSemanticAnalysis = `{"purpose":"Explains the file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`

func newSemanticAnalysisService(t *testing.T, baseURL string, analysisSeconds int) (*Service, string) {
	return newSemanticAnalysisServiceFixture(t, baseURL, analysisSeconds, false)
}

func newSemanticAnalysisServiceWithHelper(t *testing.T, baseURL string, analysisSeconds int) (*Service, string) {
	return newSemanticAnalysisServiceFixture(t, baseURL, analysisSeconds, true)
}

func newSemanticAnalysisServiceFixture(t *testing.T, baseURL string, analysisSeconds int, includeHelper bool) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc Run() { fmt.Println(\"run\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if includeHelper {
		if err := os.WriteFile(filepath.Join(root, "helper.go"), []byte("package main\n\nfunc Helper() { println(\"helper secret\") }\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root, Summary: "A compact fixture project."}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(baseURL)
	cfg.ModelScopes.Bug.Model = "analysis-model"
	cfg.Timeouts = config.TimeoutConfig{AnalysisSeconds: analysisSeconds}
	cfg.Retry = config.RetryConfig{MaxRetries: 1, BackoffBase: 1, BackoffMax: 1}
	service, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	return service, root
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForTestError(t *testing.T, done <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func analysisReply(stage AnalysisStage) string {
	switch stage {
	case AnalysisStageSemantic:
		return strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"bugs","severity":"low","summary":"A conditional correctness concern."},{"category":"security","severity":"high","summary":"A conditional trust-boundary concern."}]`, 1)
	case AnalysisStagePerformance:
		return validPerformanceReview
	default:
		return validSecurityReview
	}
}

func emptyAnalysisReply(stage AnalysisStage) string {
	if stage == AnalysisStageSemantic {
		return validSemanticAnalysis
	}
	return `{"findings":[]}`
}

func analysisResponseServer(t *testing.T, reply func(AnalysisStage) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	return analysisModelResponseServer(t, "", reply)
}

func analysisModelResponseServer(t *testing.T, model string, reply func(AnalysisStage) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		// The docs refresh at a run's end is answered here but not counted:
		// the tests count the analysis stages' requests.
		if reply, ok := docsRefreshReply(request); ok {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: model, Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: reply}, FinishReason: "stop"}}})
			return
		}
		stage := AnalysisStageSecurityAI
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			stage = AnalysisStageSemantic
		}
		if strings.HasPrefix(request.Messages[0].Content, "Review exactly one supplied") {
			stage = AnalysisStagePerformance
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: model, Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: reply(stage)}, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func analysisBlockingServer(t *testing.T) (*httptest.Server, *atomic.Int32, chan struct{}, func()) {
	t.Helper()
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return
		}
		if reply, ok := docsRefreshReply(request); ok {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
			return
		}
		call := calls.Add(1)
		if call == 1 {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		reply := `{"findings":[]}`
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			reply = validSemanticAnalysis
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); server.Close() })
	return server, &calls, started, unblock
}

func analysisStageRequestFor(t *testing.T, s *Service, path string, stage AnalysisStage) analysisFileStageRequest {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.manager.IndexedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return analysisFileStageRequest{Run: AnalysisRunIdentity{AnalysisQueueIdentity: AnalysisQueueIdentity{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}, ID: "run", Generation: "generation"}, File: AnalysisFileIdentity{Path: file.Path, ContentHash: file.ContentHash, Language: file.Language}, Stage: stage, RemainingAttempts: 2, SecurityReview: true}
}

func analysisStageAuthorityFor(t *testing.T, request analysisFileStageRequest) analysisFileStageAuthority {
	t.Helper()
	var mu sync.Mutex
	check := func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
		if run != request.Run || file != request.File || stage != request.Stage {
			return project.ErrRevisionConflict
		}
		return nil
	}
	return analysisFileStageAuthority{
		Check: func(ctx context.Context, run AnalysisRunIdentity) error {
			mu.Lock()
			defer mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			if run != request.Run {
				return project.ErrRevisionConflict
			}
			return nil
		},
		BeforeAttempt: func(ctx context.Context, run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
			mu.Lock()
			defer mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return check(run, file, stage)
		},
		Publish: func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
			mu.Lock()
			defer mu.Unlock()
			if err := check(run, file, stage); err != nil {
				return err
			}
			return write()
		},
	}
}

func analysisRunPreviewFor(t *testing.T, s *Service, limits AnalysisRunLimits, resume *AnalysisRunIdentity) *AnalysisRunPreview {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: limits, ResumeRun: resume})
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func analysisStartFor(preview *AnalysisRunPreview) AnalysisRunStartRequest {
	confirmations := AnalysisRunConfirmations{SecurityReview: true}
	for _, provider := range preview.Providers {
		confirmations.ProviderIDs = append(confirmations.ProviderIDs, provider.ID)
	}
	return AnalysisRunStartRequest{Models: cloneAnalysisModels(preview.Models), Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits, RetryStaleFailed: preview.RetryStaleFailed, StaleOnly: preview.StaleOnly, StalePath: preview.StalePath, RecoverIncomplete: preview.RecoverIncomplete, Refresh: preview.Refresh, ClearResults: preview.ClearResults, Confirmations: confirmations}
}

func waitAnalysisWindow(t *testing.T, s *Service) {
	t.Helper()
	c := s.analysisRun
	c.mu.Lock()
	done, cancel := c.done, c.cancel
	deadline := c.windowStart.Add(c.windowBudget + 5*time.Second)
	c.mu.Unlock()
	if done == nil {
		return
	}
	// Durable stages can legitimately outlast the generic single-signal helper.
	// Keep the domain deadline and allow bounded journal cleanup, then join the
	// canceled worker before TempDir cleanup can race its final writes.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	case <-t.Context().Done():
	}
	if cancel != nil {
		cancel()
	}
	cleanup := time.NewTimer(5 * time.Second)
	defer cleanup.Stop()
	select {
	case <-done:
	case <-cleanup.C:
		t.Fatal("analysis worker did not join after cancellation")
	}
	t.Fatal("analysis window exceeded its admitted deadline and journal cleanup allowance")
}

func TestAnalysisWindowWaitUsesAdmittedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		s := &Service{analysisRun: &analysisRunController{done: done, windowStart: time.Now(), windowBudget: 10 * time.Second}}
		go func() { time.Sleep(4 * time.Second); close(done) }()
		waitAnalysisWindow(t, s)
	})
}

func completedAnalysisRun(t *testing.T, s *Service) *AnalysisRun {
	t.Helper()
	waitAnalysisWindow(t, s)
	run, err := s.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return run
}
