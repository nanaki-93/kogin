package insighteval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineeringInsightRunnerStopsBeforeDispatchWhenReservationPersistenceFails(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "persistence", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(filepath.Join(directory, "campaign.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
		t.Fatalf("reservation persistence failure dispatched a request: %v, calls=%d", err, client.calls.Load())
	}
}

func TestEngineeringInsightRunnerRejectsCorruptCampaignAndChangedFixtureOnResume(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "corrupt", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "campaign.json"), []byte(`{"development_requests":-1,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
		t.Fatalf("corrupt campaign was accepted: %v calls=%d", err, client.calls.Load())
	}
	if err := os.Remove(filepath.Join(directory, "campaign.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Cases[0].Source += "// changed\n"
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 3 {
		t.Fatalf("changed fixture resumed a frozen run: %v calls=%d", err, client.calls.Load())
	}
}

func TestEngineeringInsightRunnerResumesReservedAttemptAsConsumedWithoutReplay(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "resume", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := engineeringInsightRunManifest{Fingerprint: buildRunnerFingerprint(cfg), Receipt: buildRunnerReceipt(cfg)}
	for _, item := range cfg.Cases {
		manifest.Receipt.Attempts = append(manifest.Receipt.Attempts, buildUnknownReservedAttempt(item.Expected, cfg.CandidateID))
	}
	manifest.Receipt.Consumption.Requests = 3
	if err := storeRunnerJSON(filepath.Join(directory, cfg.RunID+".json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: 3}); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil || client.calls.Load() != 0 || receipt.Attempts[0].Outcome != "unknown" {
		t.Fatalf("resume replayed unknown request: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
	}
}

func TestEngineeringInsightRunnerRejectsConcurrentAndUnconfirmedRemoteRuns(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "locked", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := lockRunnerFile(filepath.Join(directory, "campaign.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRunLocked) || client.calls.Load() != 0 {
		t.Fatalf("concurrent run = %v, calls=%d", err, client.calls.Load())
	}
	lock.Close()
	cfg.Profile.APIBaseURL = "https://provider.example/v1"
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRemoteDenied) || client.calls.Load() != 0 {
		t.Fatalf("remote run = %v, calls=%d", err, client.calls.Load())
	}
}

func TestEngineeringInsightRunnerRejectsPathLikeRunIDsBeforeStateAccess(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "safe-run", EngineeringInsightCollectRunMode, 3, client)
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	campaign := filepath.Join(directory, "campaign.json")
	campaignData, err := os.ReadFile(campaign)
	if err != nil {
		t.Fatal(err)
	}
	hostileFiles := []string{"...json", "..json"}
	for _, name := range hostileFiles {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(`{"finished":true}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, runID := range []string{".", "..", "a/b", "a.b"} {
		bad := cfg
		bad.RunID = runID
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), bad); err == nil {
			t.Fatalf("run ID %q accepted", runID)
		}
		if _, err := LoadEngineeringInsightScoringHandoff(cfg.Root, runID); err == nil {
			t.Fatalf("handoff accepted %q", runID)
		}
		if _, err := StoreEngineeringInsightScores(cfg.Root, runID, nil); err == nil {
			t.Fatalf("score accepted %q", runID)
		}
		if _, err := LoadEngineeringInsightEvaluationReceipt(cfg.Root, runID); err == nil {
			t.Fatalf("export accepted %q", runID)
		}
		(&EngineeringInsightScoringHandoff{RunID: runID, Responses: map[string]string{"private": "reply"}, privateDir: directory}).Discard()
		if data, err := os.ReadFile(campaign); err != nil || string(data) != string(campaignData) {
			t.Fatalf("campaign changed for %q: %v", runID, err)
		}
		if _, err := os.Stat(filepath.Join(directory, runID+".lock")); !os.IsNotExist(err) {
			t.Fatalf("invalid ID %q created a lock: %v", runID, err)
		}
	}
	for _, name := range hostileFiles {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("hostile manifest %q changed: %v", name, err)
		}
	}
	if client.calls.Load() != 3 {
		t.Fatalf("invalid IDs dispatched calls=%d", client.calls.Load())
	}
}

func TestEngineeringInsightRunnerStopsSecondQualificationRunAtAggregateCap(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := qualificationRunnerTestConfig(t, "qualification-one", client)
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 24 {
		t.Fatalf("qualification calls=%d", client.calls.Load())
	}
	next := qualificationRunnerTestConfig(t, "qualification-two", client)
	next.Root = cfg.Root
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), next); err == nil || !strings.Contains(err.Error(), "budget") || client.calls.Load() != 24 {
		t.Fatalf("second qualification run bypassed cap: %v calls=%d", err, client.calls.Load())
	}
}
