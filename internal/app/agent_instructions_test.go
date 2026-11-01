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

	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestAnalysisAgentsUseGuidesAndInvalidateChangedGuidance(t *testing.T) {
	for _, stage := range []AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityAI} {
		t.Run(string(stage), func(t *testing.T) {
			var calls atomic.Int32
			prompts := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request llm.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				calls.Add(1)
				prompts <- request.Messages[0].Content
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: analysisReply(stage)}}}})
			}))
			defer server.Close()
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			guide := filepath.Join(root, "AGENTS.md")
			write := func(content string) {
				t.Helper()
				if err := os.WriteFile(guide, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			write("Preserve the cancellation boundary.")
			request := analysisStageRequestFor(t, s, "main.go", stage)
			run := func() analysisFileStageResult {
				t.Helper()
				result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			run()
			if prompt := <-prompts; !strings.Contains(prompt, "Preserve the cancellation boundary.") || !strings.Contains(prompt, project.InstructionPromptGuidance) {
				t.Fatal("applicable guide did not reach the agent")
			}
			run()
			if calls.Load() != 1 {
				t.Fatal("unchanged guidance did not reuse the report")
			}
			write("Preserve error propagation instead.")
			run()
			if calls.Load() != 2 {
				t.Fatal("changed guidance reused old analysis")
			}
			if prompt := <-prompts; !strings.Contains(prompt, "Preserve error propagation instead.") || strings.Contains(prompt, "Preserve the cancellation boundary.") {
				t.Fatal("refreshed analysis used outdated guidance")
			}
			if err := os.WriteFile(filepath.Join(root, ".kasuriignore"), []byte("AGENTS.md\n"), 0644); err != nil {
				t.Fatal(err)
			}
			run()
			if prompt := <-prompts; strings.Contains(prompt, "Preserve error propagation instead.") {
				t.Fatal("excluded guide reached provider")
			}
		})
	}
}

func TestAnalysisAgentsRejectGuidanceChangesDuringGeneration(t *testing.T) {
	for _, stage := range []AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityAI} {
		t.Run(string(stage), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: analysisReply(stage)}}}})
			}))
			defer server.Close()
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			request := analysisStageRequestFor(t, s, "main.go", stage)
			done := make(chan error, 1)
			go func() {
				_, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
				done <- err
			}()
			waitForTestSignal(t, started, "analysis agent request")
			err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("New guidance."), 0644)
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if err := waitForTestError(t, done, "analysis agent result"); !errors.Is(err, project.ErrRevisionConflict) {
				t.Fatalf("changed guidance was accepted: %v", err)
			}
			if stage == AnalysisStagePerformance {
				assertNoPerformanceReport(t, root)
			}
			if stage == AnalysisStageSecurityAI {
				assertNoSecurityReport(t, root)
			}
			if stage == AnalysisStageSemantic {
				cached, err := s.CachedFileAnalysis("main.go")
				if err != nil || cached.Status != project.AnalysisStatusMissing {
					t.Fatalf("stale report persisted: %+v, %v", cached, err)
				}
			}
		})
	}
}
