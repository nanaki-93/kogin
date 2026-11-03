package agentstate

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func usageFixture() BudgetUsage {
	return BudgetUsage{ModelCalls: 2, InputTokens: 100, OutputTokens: 50, UnknownUsageCalls: 1, Accounting: &UsageAccounting{Version: 1, Providers: []ProviderUsage{{ModelKey: strings.Repeat("a", 64), ModelCalls: 2, FailedCalls: 1, UnknownUsageCalls: 1, ReportedCalls: 1, PartialUsageCalls: 1, CacheKnownCalls: 2, InputTokens: 100, OutputTokens: 50, ReportedInputTokens: 20, ReportedOutputTokens: 10, CachedInputTokens: 5}}}}
}

func TestUsageJournalValidatesProjectionAndPreservesFailedWrites(t *testing.T) {
	s, _ := testStore(t)
	runtime, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	baseline := BudgetRecord{ID: "usage", Binding: Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}, Limits: runtime.Limits, Sequence: 1, Usage: usageFixture()}
	if _, err := s.Checkpoint(t.Context(), baseline, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "cache-subset", "foreign-total", "regression", "removed-projection", "duplicate", "negative", "inconsistent-calls", "arbitrary-model"} {
		changed := baseline
		changed.Sequence++
		changed.Usage = baseline.Usage.Clone()
		group := &changed.Usage.Accounting.Providers[0]
		switch kind {
		case "version":
			changed.Usage.Accounting.Version = 99
		case "cache-subset":
			group.CachedInputTokens = group.ReportedInputTokens + 1
		case "foreign-total":
			group.InputTokens++
		case "regression":
			group.ReportedInputTokens--
		case "removed-projection":
			changed.Usage.Accounting = nil
		case "duplicate":
			changed.Usage.Accounting.Providers = append(changed.Usage.Accounting.Providers, *group)
		case "negative":
			group.FailedCalls = -1
		case "inconsistent-calls":
			group.ReportedCalls++
		case "arbitrary-model":
			group.ModelKey = "PRIVATE_MODEL_NAME"
		}
		if _, err := s.Checkpoint(t.Context(), changed, nil); err == nil {
			t.Fatalf("accepted %s accounting", kind)
		}
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("disk failure") }
	next := baseline
	next.Sequence++
	if _, err := s.Checkpoint(t.Context(), next, nil); !errors.Is(err, ErrPersistence) {
		t.Fatal("lost failed checkpoint", err)
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := reopened.load()
	if err != nil || !reflect.DeepEqual(db.Budgets[baseline.ID], baseline) {
		t.Fatal("failed checkpoint altered retained accounting", err)
	}
}

func TestUsageLegacyRecordsDoNotAcquireFabricatedMeasurements(t *testing.T) {
	legacy := BudgetUsage{ModelCalls: 2, InputTokens: 100, OutputTokens: 50, UnknownUsageCalls: 1}
	if !validUsage(legacy) || legacy.Clone().Accounting != nil || monotonicAccounting(legacy, usageFixture()) {
		t.Fatal("legacy usage reinterpreted as measured")
	}
	if !validUsage(BudgetUsage{Accounting: &UsageAccounting{Version: 1}}) {
		t.Fatal("empty new accounting rejected")
	}
}
