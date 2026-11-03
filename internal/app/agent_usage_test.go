package app

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func costFixture() (agentstate.BudgetUsage, config.AgentPriceTable) {
	in, out, cached := 2.0, 4.0, 0.5
	table := config.AgentPriceTable{Version: "fixture-v1", Currency: "USD", Rates: []config.AgentModelPrice{{Provider: "openai", Model: "fixture", InputPerMillion: &in, OutputPerMillion: &out, CachedPerMillion: &cached}}}
	group := agentstate.ProviderUsage{ModelKey: agentruntime.ProviderUsageKey("openai", "fixture"), ModelCalls: 1, ReportedCalls: 1, CacheKnownCalls: 1, InputTokens: 1000, OutputTokens: 500, ReportedInputTokens: 1000, ReportedOutputTokens: 500, CachedInputTokens: 200}
	return agentstate.BudgetUsage{Accounting: &agentstate.UsageAccounting{Version: 1, Providers: []agentstate.ProviderUsage{group}}, ModelCalls: 1, InputTokens: 1000, OutputTokens: 500}, table
}

func TestAgentCostRequiresCompleteUsageRatesAndCacheKnowledge(t *testing.T) {
	usage, table := costFixture()
	cost := agentCost(usage, table)
	if cost.Status != "estimated" || cost.Total == nil || math.Abs(*cost.Total-0.0037) > 1e-12 {
		t.Fatalf("cost=%+v", cost)
	}
	for _, unknown := range []string{"price_table_unavailable", "legacy_usage", "model_rate_unavailable", "usage_incomplete", "cache_split_unknown"} {
		usage, table := costFixture()
		switch unknown {
		case "price_table_unavailable":
			table = config.AgentPriceTable{}
		case "legacy_usage":
			usage.Accounting = nil
		case "model_rate_unavailable":
			table.Rates[0].Model = "different"
		case "usage_incomplete":
			usage.Accounting.Providers[0].UnknownUsageCalls = 1
		case "cache_split_unknown":
			usage.Accounting.Providers[0].CacheKnownCalls = 0
			usage.Accounting.Providers[0].CachedInputTokens = 0
		}
		cost := agentCost(usage, table)
		if cost.Total != nil || cost.Status != "unknown" || !slices.Contains(cost.Reasons, unknown) {
			t.Fatalf("unknown became priced: %+v", cost)
		}
		if unknown == "cache_split_unknown" && cost.KnownSubtotal != 0.002 {
			t.Fatal("unmeasured cache split was priced", cost)
		}
	}
	usage, table = costFixture()
	usage.Accounting.Providers[0].CacheKnownCalls = 0
	usage.Accounting.Providers[0].CachedInputTokens = 0
	*table.Rates[0].CachedPerMillion = *table.Rates[0].InputPerMillion
	if cost := agentCost(usage, table); cost.Total == nil || *cost.Total != 0.004 {
		t.Fatal("equal cache price requires no split", cost)
	}
}

func TestAgentReportingSettingsPreserveLegacyExecutionIdentity(t *testing.T) {
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	before, err := s.agentConfiguration(s.conf().agentRuntime)
	if err != nil {
		t.Fatal(err)
	}
	_, prices := costFixture()
	changed := s.conf().agentRuntime
	changed.Telemetry.Prices = prices
	after, err := s.agentConfiguration(changed)
	if err != nil || after != before {
		t.Fatal("pricing invalidated execution preferences", err)
	}
	legacy := struct {
		Profiles [3]config.ModelProfile
		Runtime  struct {
			Roles   config.AgentRoles
			Limits  config.AgentLimits
			Context config.AgentContext
			Skills  []string
		}
	}{Profiles: [3]config.ModelProfile{s.conf().runtimes.analyze.profile, s.conf().runtimes.bug.profile, s.conf().runtimes.function.profile}}
	legacy.Runtime.Roles, legacy.Runtime.Limits = s.conf().agentRuntime.Roles, s.conf().agentRuntime.Limits
	legacy.Runtime.Context, legacy.Runtime.Skills = s.conf().agentRuntime.Context, s.conf().agentRuntime.RetiredSkills
	data, err := json.Marshal(legacy)
	if err != nil || agentruntime.TraceIdentity(string(data)) != after {
		t.Fatal("empty telemetry changed legacy identity", err)
	}
}

func TestAgentTraceOverviewCancellationPreservesFactualInventory(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	configured, err := New(scopedTestConfig(server.URL), s.manager)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		analysis *project.Analysis
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		analysis, err := configured.ImportProject(ctx, s.manager.Root(), true)
		finished <- outcome{analysis, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	cancel()
	select {
	case result := <-finished:
		if result.err != nil || result.analysis == nil || result.analysis.AIStatus != project.ProjectAnalysisStatusFailed || result.analysis.FileCount == 0 {
			t.Fatalf("inventory lost: %+v %v", result.analysis, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled trace cleanup did not finish")
	}
}
