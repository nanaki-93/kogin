package insighteval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEngineeringInsightRunnerRequiresExplicitDevelopmentBudgetGrantForSecondSixRequests(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	first := runnerTestConfig(t, "development-one", EngineeringInsightCollectRunMode, 3, client)
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := runnerTestConfig(t, "development-two", EngineeringInsightDevelopmentRunMode, 3, client)
	second.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), second); err != nil || client.calls.Load() != 6 {
		t.Fatalf("original development budget = %v, calls=%d", err, client.calls.Load())
	}
	third := runnerTestConfig(t, "development-three", EngineeringInsightDevelopmentRunMode, 3, client)
	third.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), third); err == nil || client.calls.Load() != 6 {
		t.Fatalf("ungranted extension dispatched: %v, calls=%d", err, client.calls.Load())
	}
	if err := GrantEngineeringInsightDevelopmentBudget(first.Root, "extension-one", 6); err != nil {
		t.Fatal(err)
	}
	if err := GrantEngineeringInsightDevelopmentBudget(first.Root, "extension-one", 6); err == nil {
		t.Fatal("duplicate development budget grant was accepted")
	}
	campaign, err := loadEvaluationCampaign(filepath.Join(first.Root, runnerRelativeDirectory, "campaign.json"))
	if err != nil || campaign.DevelopmentRequests != 6 || campaign.QualificationRequests != 0 {
		t.Fatalf("grant changed campaign counters: %+v, %v", campaign, err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), third); err != nil || client.calls.Load() != 9 {
		t.Fatalf("first granted run = %v, calls=%d", err, client.calls.Load())
	}
	fourth := runnerTestConfig(t, "development-four", EngineeringInsightDevelopmentRunMode, 3, client)
	fourth.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), fourth); err != nil || client.calls.Load() != 12 {
		t.Fatalf("second granted run = %v, calls=%d", err, client.calls.Load())
	}
	fifth := runnerTestConfig(t, "development-five", EngineeringInsightDevelopmentRunMode, 3, client)
	fifth.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), fifth); err == nil || client.calls.Load() != 12 {
		t.Fatalf("development requests exceeded twelve: %v, calls=%d", err, client.calls.Load())
	}
	campaign, err = loadEvaluationCampaign(filepath.Join(first.Root, runnerRelativeDirectory, "campaign.json"))
	if err != nil || campaign.DevelopmentRequests != 12 || campaign.QualificationRequests != 0 {
		t.Fatalf("extended campaign counters: %+v, %v", campaign, err)
	}
}

func TestEngineeringInsightRunnerAppliesBoundQwenRecoveryGrantOnlyAfterTwelveRequests(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	first := runnerTestConfig(t, "development-one", EngineeringInsightCollectRunMode, 3, client)
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := runnerTestConfig(t, "development-two", EngineeringInsightDevelopmentRunMode, 3, client)
	second.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := GrantEngineeringInsightDevelopmentBudget(first.Root, "extension-one", 6); err != nil {
		t.Fatal(err)
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(first.Root, recoveryDevelopmentAuthorizationID, 6, recoveryDevelopmentCandidateID, recoveryDevelopmentModel); err == nil {
		t.Fatal("second recovery grant was accepted before twelve requests")
	}
	for _, runID := range []string{"development-three", "development-four"} {
		cfg := runnerTestConfig(t, runID, EngineeringInsightDevelopmentRunMode, 3, client)
		cfg.Root = first.Root
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err != nil {
			t.Fatalf("complete first recovery %q: %v", runID, err)
		}
	}
	if client.calls.Load() != 12 {
		t.Fatalf("first two development grants dispatched %d requests, want 12", client.calls.Load())
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(first.Root, recoveryDevelopmentAuthorizationID, 6, "wrong-candidate", recoveryDevelopmentModel); err == nil {
		t.Fatal("recovery grant accepted the wrong candidate")
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(first.Root, recoveryDevelopmentAuthorizationID, 6, recoveryDevelopmentCandidateID, "wrong-model"); err == nil {
		t.Fatal("recovery grant accepted the wrong model")
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(first.Root, recoveryDevelopmentAuthorizationID, 6, recoveryDevelopmentCandidateID, recoveryDevelopmentModel); err != nil {
		t.Fatal(err)
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(first.Root, recoveryDevelopmentAuthorizationID, 6, recoveryDevelopmentCandidateID, recoveryDevelopmentModel); err == nil {
		t.Fatal("recovery grant replay was accepted")
	}

	wrongCandidate := recoveryRunnerTestConfig(t, "recovery-wrong-candidate", client)
	wrongCandidate.Root = first.Root
	wrongCandidate.CandidateID = "another-candidate"
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), wrongCandidate); err == nil || client.calls.Load() != 12 {
		t.Fatalf("wrong recovery candidate dispatched: %v, calls=%d", err, client.calls.Load())
	}
	wrongModel := recoveryRunnerTestConfig(t, "recovery-wrong-model", client)
	wrongModel.Root = first.Root
	wrongModel.Profile.Model = "another-model"
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), wrongModel); err == nil || client.calls.Load() != 12 {
		t.Fatalf("wrong recovery model dispatched: %v, calls=%d", err, client.calls.Load())
	}
	wrongDestination := recoveryRunnerTestConfig(t, "recovery-wrong-destination", client)
	wrongDestination.Root = first.Root
	wrongDestination.Profile.APIBaseURL = "https://provider.example/v1"
	wrongDestination.ConfirmRemoteProvider = true
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), wrongDestination); err == nil || client.calls.Load() != 12 {
		t.Fatalf("remote recovery destination dispatched: %v, calls=%d", err, client.calls.Load())
	}
	for _, runID := range []string{"recovery-one", "recovery-two"} {
		cfg := recoveryRunnerTestConfig(t, runID, client)
		cfg.Root = first.Root
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err != nil {
			t.Fatalf("bound recovery %q: %v", runID, err)
		}
	}
	final := recoveryRunnerTestConfig(t, "recovery-exhausted", client)
	final.Root = first.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), final); err == nil || client.calls.Load() != 18 {
		t.Fatalf("recovery cap was not exhausted: %v, calls=%d", err, client.calls.Load())
	}
	campaign, err := loadEvaluationCampaign(filepath.Join(first.Root, runnerRelativeDirectory, "campaign.json"))
	if err != nil || campaign.DevelopmentRequests != 18 || campaign.QualificationRequests != 0 {
		t.Fatalf("recovery campaign counters: %+v, %v", campaign, err)
	}
}

func TestEngineeringInsightRunnerRejectsRecoveryAuthorizationAsFirstGrant(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: defaultDevelopmentRequestCap}); err != nil {
		t.Fatal(err)
	}
	if err := GrantEngineeringInsightDevelopmentBudget(root, recoveryDevelopmentAuthorizationID, developmentGrantRequestCount); err == nil {
		t.Fatal("unbound recovery authorization was accepted as first grant")
	}
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(root, recoveryDevelopmentAuthorizationID, developmentGrantRequestCount, recoveryDevelopmentCandidateID, recoveryDevelopmentModel); err == nil {
		t.Fatal("bound recovery authorization was accepted as first grant")
	}
	if _, exists, err := loadDevelopmentGrantLedger(directory); err != nil || exists {
		t.Fatalf("recovery authorization created a ledger: exists=%t err=%v", exists, err)
	}
}

func TestEngineeringInsightRunnerRejectsUnboundRecoveryAuthorizationInPersistedLedger(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(buildDevelopmentGrantLedgerPath(directory), []byte(`{"grants":[{"authorization_id":"qual05-qwen38-recovery-1","requests":6}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadDevelopmentGrantLedger(directory); err == nil {
		t.Fatal("persisted recovery authorization was accepted as an unbound first grant")
	}
}

func TestEngineeringInsightRunnerDoesNotReplayInterruptedBoundRecovery(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryRunnerTestConfig(t, "interrupted-recovery", client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := engineeringInsightDevelopmentGrantLedger{Grants: []engineeringInsightDevelopmentGrant{
		{AuthorizationID: "extension-one", Requests: 6},
		{AuthorizationID: recoveryDevelopmentAuthorizationID, Requests: 6, CandidateID: recoveryDevelopmentCandidateID, Model: recoveryDevelopmentModel},
	}}
	if err := storeRunnerJSON(buildDevelopmentGrantLedgerPath(directory), ledger); err != nil {
		t.Fatal(err)
	}
	manifest := engineeringInsightRunManifest{Fingerprint: buildRunnerFingerprint(cfg), Receipt: buildRunnerReceipt(cfg)}
	for _, item := range cfg.Cases {
		manifest.Receipt.Attempts = append(manifest.Receipt.Attempts, buildUnknownReservedAttempt(item.Expected, cfg.CandidateID))
	}
	manifest.Receipt.Consumption.Requests = len(cfg.Cases)
	if err := storeRunnerJSON(filepath.Join(directory, cfg.RunID+".json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: 15}); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil || client.calls.Load() != 0 || receipt.Attempts[0].Outcome != "unknown" {
		t.Fatalf("interrupted bound recovery replayed: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
	}
	campaign, err := loadEvaluationCampaign(filepath.Join(directory, "campaign.json"))
	if err != nil || campaign.DevelopmentRequests != 15 || campaign.QualificationRequests != 0 {
		t.Fatalf("interrupted recovery campaign changed: %+v, %v", campaign, err)
	}
}

func TestEngineeringInsightRunnerLocksBoundRecoveryGrantAndDispatchTogether(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryRunnerTestConfig(t, "concurrent-recovery", client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := engineeringInsightDevelopmentGrantLedger{Grants: []engineeringInsightDevelopmentGrant{{AuthorizationID: "extension-one", Requests: 6}}}
	if err := storeRunnerJSON(buildDevelopmentGrantLedgerPath(directory), ledger); err != nil {
		t.Fatal(err)
	}
	if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: 12}); err != nil {
		t.Fatal(err)
	}
	lock, err := lockRunnerFile(filepath.Join(directory, "campaign.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := GrantEngineeringInsightRecoveryDevelopmentBudget(cfg.Root, recoveryDevelopmentAuthorizationID, 6, recoveryDevelopmentCandidateID, recoveryDevelopmentModel); !errors.Is(err, ErrEngineeringInsightRunLocked) {
		t.Fatalf("concurrent recovery grant = %v", err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRunLocked) || client.calls.Load() != 0 {
		t.Fatalf("concurrent recovery dispatch = %v, calls=%d", err, client.calls.Load())
	}
}

func TestEngineeringInsightRunnerRejectsCorruptDevelopmentGrantLedgerBeforeDispatch(t *testing.T) {
	for name, ledger := range map[string]string{
		"unknown field":           `{"grants":[{"authorization_id":"extension-one","requests":6}],"unexpected":true}`,
		"duplicate field":         `{"grants":[],"grants":[{"authorization_id":"extension-one","requests":6}]}`,
		"empty grants":            `{"grants":[]}`,
		"wrong requests":          `{"grants":[{"authorization_id":"extension-one","requests":5}]}`,
		"v11 out of order":        `{"grants":[{"authorization_id":"extension-one","requests":6},{"authorization_id":"qual05-qwen38-schema-1","requests":6,"candidate_id":"qwen38-v11-schema-1","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v11"}]}`,
		"unbound v11":             `{"grants":[{"authorization_id":"extension-one","requests":6},{"authorization_id":"qual05-qwen38-recovery-1","requests":6,"candidate_id":"qwen38-v10-recovery-1","model":"qwen/qwen3.8-27b"},{"authorization_id":"qual05-qwen38-schema-1","requests":6,"candidate_id":"qwen38-v11-schema-1","model":"qwen/qwen3.8-27b"}]}`,
		"unbound v12":             `{"grants":[{"authorization_id":"extension-one","requests":6},{"authorization_id":"qual05-qwen38-recovery-1","requests":6,"candidate_id":"qwen38-v10-recovery-1","model":"qwen/qwen3.8-27b"},{"authorization_id":"qual05-qwen38-schema-1","requests":6,"candidate_id":"qwen38-v11-schema-1","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v11"},{"authorization_id":"authqual05-qwen38-structured-1","requests":6,"candidate_id":"wrong-candidate","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v12"}]}`,
		"unbound thinking schema": `{"grants":[{"authorization_id":"extension-one","requests":6},{"authorization_id":"qual05-qwen38-recovery-1","requests":6,"candidate_id":"qwen38-v10-recovery-1","model":"qwen/qwen3.8-27b"},{"authorization_id":"qual05-qwen38-schema-1","requests":6,"candidate_id":"qwen38-v11-schema-1","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v11"},{"authorization_id":"authqual05-qwen38-structured-1","requests":6,"candidate_id":"qwen38-v12-structured-1","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v12"},{"authorization_id":"qual05-qwen38-thinking-off-1","requests":6,"candidate_id":"qwen38-v12-thinking-off-1","model":"qwen/qwen3.8-27b","prompt_version":"file-analysis-v12","reasoning_effort":"none"},{"authorization_id":"qual05-qwen38-thinking-schema-1","requests":6,"candidate_id":"qwen38-v12-thinking-schema-1","model":"./models/qwen38-v12-thinking-schema-1","prompt_version":"file-analysis-v12"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
			cfg := runnerTestConfig(t, "corrupt-grant", EngineeringInsightCollectRunMode, 3, client)
			directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: 6}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(buildDevelopmentGrantLedgerPath(directory), []byte(ledger), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
				t.Fatalf("corrupt grant ledger dispatched: %v, calls=%d", err, client.calls.Load())
			}
			if err := GrantEngineeringInsightDevelopmentBudget(cfg.Root, "extension-two", 6); err == nil {
				t.Fatal("corrupt grant ledger accepted another grant")
			}
		})
	}
}

func recoveryRunnerTestConfig(t *testing.T, runID string, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	cfg := runnerTestConfig(t, runID, EngineeringInsightDevelopmentRunMode, 3, client)
	cfg.CandidateID = recoveryDevelopmentCandidateID
	cfg.Model = recoveryDevelopmentModel
	cfg.Profile.Model = recoveryDevelopmentModel
	return cfg
}

func v12RunnerTestConfig(t *testing.T, runID string, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	cfg := runnerTestConfig(t, runID, EngineeringInsightDevelopmentRunMode, 3, client)
	cfg.CandidateID = v12DevelopmentCandidateID
	cfg.Model = v12DevelopmentModel
	cfg.Profile.Model = v12DevelopmentModel
	cfg.PromptVersion = v12DevelopmentPromptVersion
	return cfg
}

func TestEngineeringInsightRunnerRejectsHistoricalPromptDispatch(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := v12RunnerTestConfig(t, "historical-v12-readonly", client)
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
		t.Fatalf("historical v12 prompt dispatched: %v, calls=%d", err, client.calls.Load())
	}
	ledger := engineeringInsightDevelopmentGrantLedger{Grants: []engineeringInsightDevelopmentGrant{{AuthorizationID: "extension-one", Requests: 6}, {AuthorizationID: recoveryDevelopmentAuthorizationID, Requests: 6, CandidateID: recoveryDevelopmentCandidateID, Model: recoveryDevelopmentModel}, {AuthorizationID: v11DevelopmentAuthorizationID, Requests: 6, CandidateID: v11DevelopmentCandidateID, Model: v11DevelopmentModel, PromptVersion: v11DevelopmentPromptVersion}, {AuthorizationID: v12DevelopmentAuthorizationID, Requests: 6, CandidateID: v12DevelopmentCandidateID, Model: v12DevelopmentModel, PromptVersion: v12DevelopmentPromptVersion}}}
	if !isValidDevelopmentGrantLedger(ledger) {
		t.Fatal("historical grant ledger no longer validates")
	}
}
