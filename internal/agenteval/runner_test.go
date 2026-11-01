package agenteval

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func TestReplayComparisonRetainsCorpusBudgetsAndNegativeOutcomes(t *testing.T) {
	first, err := Run(t.Context(), Options{Limits: DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(t.Context(), Options{Limits: first.Limits})
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != "replay" || second.Mode != "replay" || first.CorpusDigest != second.CorpusDigest || first.CorpusVersion != second.CorpusVersion || first.ModelKey != second.ModelKey || first.PromptVersion != second.PromptVersion || first.Scoring != second.Scoring || first.Limits != second.Limits {
		t.Fatal("replay changed its comparison inputs")
	}
	if len(first.Cases) != len(second.Cases) {
		t.Fatal("comparison dropped cases")
	}
	for i, before := range first.Cases {
		after := second.Cases[i]
		// Local harness timing is deliberately not a model-quality measurement.
		before.ElapsedMillis, after.ElapsedMillis = nil, nil
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("deterministic task/control changed: %s", before.ID)
		}
		if before.Control && before.State == "passed" {
			t.Fatal("negative control became a successful coding task")
		}
	}
}

func TestReplayExercisesNativeTasksControlsRepairAndAccounting(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("private.env", []byte("PRIVATE_LOCAL_SOURCE"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := Run(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range receipt.Cases {
		if item.ExpectationMet == nil || !*item.ExpectationMet {
			t.Errorf("case %s: %+v", item.ID, item)
		}
	}
	if receipt.State != "passed" || len(receipt.Cases) != 9 || receipt.Usage.ModelCalls != 22 || receipt.Usage.ToolCalls != 12 || receipt.Usage.Repairs != 1 || receipt.Usage.UnknownUsageCalls != 1 {
		t.Fatalf("campaign: %+v", receipt)
	}
	last := receipt.Cases[len(receipt.Cases)-1]
	if last.State != "canceled" || !last.LatencyCensored || last.Metrics.RegressionPassed != nil {
		t.Fatal("cancellation became completion", last)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Calculate", "calc.go", "private.env", "PRIVATE_", "a + b", "go test", "127.0.0.1", "http://"} {
		if strings.Contains(string(data), private) {
			t.Fatal("private data escaped receipt", private)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "private.env" {
		t.Fatal("evaluation wrote workspace data", entries, err)
	}
}

func TestCampaignBudgetStopsLaterCasesWithoutInventingResults(t *testing.T) {
	limits := DefaultLimits()
	limits.ModelCalls = 1
	receipt, err := Run(t.Context(), Options{Limits: limits})
	if err != nil || receipt.State != "incomplete" || receipt.Usage.ModelCalls != 1 {
		t.Fatal(receipt, err)
	}
	for i, item := range receipt.Cases {
		if i == 0 {
			if item.State != "incomplete" || !contains(item.Failures, "budget_exhausted") {
				t.Fatal(item)
			}
		} else if item.State != "not_run" || item.ElapsedMillis != nil || item.Metrics.RegressionPassed != nil {
			t.Fatal("unrun result fabricated", item)
		}
	}
}

func contains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func TestLiveRequiresGrantAndBudgetsAndUsesTheSameGuardedPath(t *testing.T) {
	document, err := decodeCorpus(corpusData)
	if err != nil {
		t.Fatal(err)
	}
	replay := &replayProvider{}
	replay.selectCase(document.Cases[0], func() {})
	server := httptest.NewServer(replay)
	defer server.Close()
	profile := config.ModelProfile{Provider: config.OpenAIProvider, APIBaseURL: server.URL, APIKey: "PRIVATE_TOKEN", Model: "replay", MaxTokens: 512, ContextMaxTokens: 16000}
	options := Options{Mode: "live", CaseID: "addition", Profile: profile, Limits: DefaultLimits()}
	if _, err := Run(t.Context(), options); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	if replay.next != 0 {
		t.Fatal("unconfirmed request sent")
	}
	options.Confirmed, options.Limits = true, config.AgentLimits{}
	if _, err := Run(t.Context(), options); err == nil {
		t.Fatal("missing budget accepted")
	}
	options.Limits = DefaultLimits()
	receipt, err := Run(t.Context(), options)
	if err != nil || receipt.State != "passed" || len(receipt.Cases) != 1 || receipt.Cases[0].ExpectationMet != nil {
		t.Fatal(receipt, err)
	}
	data, _ := json.Marshal(receipt)
	if strings.Contains(string(data), "PRIVATE_TOKEN") || strings.Contains(string(data), server.URL) {
		t.Fatal("provider details escaped receipt")
	}
	options.CaseID = "scope-control"
	if _, err := Run(t.Context(), options); err == nil {
		t.Fatal("live negative control selected")
	}
}

func TestCanceledCampaignRetainsUnrunCasesWithoutDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	receipt, err := Run(ctx, Options{})
	if err != nil || receipt.State != "incomplete" || receipt.Usage.ModelCalls != 0 || len(receipt.Cases) != 9 {
		t.Fatal(receipt, err)
	}
	for _, item := range receipt.Cases {
		if item.State != "not_run" {
			t.Fatal(item)
		}
	}
}

func TestReplayRejectsAccidentalLiveOptionsAndUnknownCases(t *testing.T) {
	for _, options := range []Options{{Mode: "other"}, {Profile: config.ModelProfile{Model: "PRIVATE_MODEL"}}, {Confirmed: true}, {CaseID: "missing"}} {
		if _, err := Run(t.Context(), options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
