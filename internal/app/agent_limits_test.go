package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestAgentRetriesConsumeSharedBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(server.URL)
	cfg.AgentRuntime.MaxModelCalls = 2
	cfg.Retry.BackoffBase, cfg.Retry.BackoffMax = 1, 1
	s, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop, err := s.agentRunContext(context.Background(), s.conf().runtimes.bug)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, err = s.retryRequest(ctx, s.conf().runtimes.bug, []llm.ChatMessage{{Role: "user", Content: "request"}}, nil)
	if !errors.Is(err, agentruntime.ErrBudgetExceeded) || calls.Load() != 2 {
		t.Fatalf("retry error=%v calls=%d", err, calls.Load())
	}
	usage := agentruntime.BudgetFromContext(ctx).Snapshot()
	if usage.ModelCalls != 2 || usage.UnknownUsageCalls != 2 || usage.ActiveCalls != 0 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestAgentRoleDefaultsAreFrozenAndExplicitSelectionsWin(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.AgentRuntime.Roles = config.AgentRoles{Creation: config.CodeModelScope, Testing: config.BugModelScope, Review: config.AnalyzeModelScope}
	s, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentRuntime.Roles.Creation = config.AnalyzeModelScope
	if got := s.resolveWorkflowModels(t.Context(), ChangeWorkflowModels{}); got != (ChangeWorkflowModels{"code", "bug", "analyze"}) {
		t.Fatalf("configuration was not frozen: %+v", got)
	}
	if got := s.resolveWorkflowModels(t.Context(), ChangeWorkflowModels{Create: "bug", Review: "code"}); got != (ChangeWorkflowModels{"bug", "bug", "code"}) {
		t.Fatalf("explicit selections lost: %+v", got)
	}
}

func TestAnalysisAdmissionGrantsOnlyAvailableCapturedProviders(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Default(), manager)
	if err != nil {
		t.Fatal(err)
	}
	preview := &AnalysisRunPreview{Providers: []AnalysisProviderRequirement{{Stages: []AnalysisStage{AnalysisStageSemantic}}}}
	ctx, cancel, err := s.analysisAgentContext(context.Background(), preview)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, err := agentruntime.WithPolicy(ctx, s.conf().runtimes.analyze.client); !errors.Is(err, agentruntime.ErrPolicyDenied) {
		t.Fatalf("uncaptured destination granted: %v", err)
	}
	s.conf().runtimes.bug.client = nil
	missing, stop, err := s.analysisAgentContext(context.Background(), preview)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err := agentruntime.WithPolicy(missing, s.conf().runtimes.function.client); !errors.Is(err, agentruntime.ErrPolicyDenied) {
		t.Fatalf("missing destination acquired a fallback: %v", err)
	}
}
