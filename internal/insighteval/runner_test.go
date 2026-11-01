package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func runnerPromptContextManifest(t *testing.T, prompt string) project.ContextManifest {
	t.Helper()
	const prefix = "CONTEXT_MANIFEST:\n"
	start := strings.Index(prompt, prefix)
	end := strings.Index(prompt, "\n\nTARGET_FACTS:")
	if start < 0 || end < start+len(prefix) {
		t.Fatal("runner prompt has no context manifest")
	}
	var manifest project.ContextManifest
	if err := json.Unmarshal([]byte(prompt[start+len(prefix):end]), &manifest); err != nil {
		t.Fatalf("decode runner context manifest: %v", err)
	}
	return manifest
}

func TestEngineeringInsightRunnerPersistsSourceFreeSixRequestDevelopmentCampaign(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "development-one", EngineeringInsightCollectRunMode, 3, client)
	receipt, handoff, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 3 || receipt.Consumption.Requests != 3 || len(handoff.Responses) != 3 {
		t.Fatalf("calls=%d receipt=%+v handoff=%d", client.calls.Load(), receipt.Consumption, len(handoff.Responses))
	}
	productionSchema, err := app.FileAnalysisResponseSchema().Identity()
	if err != nil {
		t.Fatal(err)
	}
	runnerSchema, err := client.schema.Identity()
	if err != nil || runnerSchema != productionSchema {
		t.Fatalf("runner schema identity = %q, %v; want production %q", runnerSchema, err, productionSchema)
	}
	promptManifest := runnerPromptContextManifest(t, client.prompt)
	wantManifest := project.ContextManifest{Scope: "bug", Model: "model", ProviderOrigin: "http://127.0.0.1:9999", RemoteProvider: false, ByteLimit: 64 * 1024, TokenLimit: 16 * 1024}
	if promptManifest.Scope != wantManifest.Scope || promptManifest.Model != wantManifest.Model || promptManifest.ProviderOrigin != wantManifest.ProviderOrigin || promptManifest.RemoteProvider != wantManifest.RemoteProvider || promptManifest.ByteLimit != wantManifest.ByteLimit || promptManifest.TokenLimit != wantManifest.TokenLimit {
		t.Fatalf("runner prompt manifest = %+v, want production binding %+v", promptManifest, wantManifest)
	}
	manifest, err := os.ReadFile(filepath.Join(cfg.Root, runnerRelativeDirectory, cfg.RunID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"fixture-secret", "provider-secret", "model reply is private"} {
		if strings.Contains(string(manifest), forbidden) {
			t.Fatalf("manifest retained private data %q: %s", forbidden, manifest)
		}
	}
	reloaded, err := LoadEngineeringInsightScoringHandoff(cfg.Root, cfg.RunID)
	if err != nil || len(reloaded.Responses) != 3 {
		t.Fatalf("reload private scoring handoff: %d, %v", len(reloaded.Responses), err)
	}
	if err := os.RemoveAll(reloaded.privateDir); err != nil {
		t.Fatal(err)
	}
	handoff.Discard()
	if len(handoff.Responses) != 0 {
		t.Fatal("private scoring handoff was not discarded")
	}
	second := runnerTestConfig(t, "development-two", EngineeringInsightDevelopmentRunMode, 3, client)
	second.Root = cfg.Root
	if _, _, err = RunEngineeringInsightEvaluation(context.Background(), second); err != nil || client.calls.Load() != 6 {
		t.Fatalf("second reviewed development run failed: %v, calls=%d", err, client.calls.Load())
	}
	third := runnerTestConfig(t, "development-three", EngineeringInsightDevelopmentRunMode, 3, client)
	third.Root = cfg.Root
	_, _, err = RunEngineeringInsightEvaluation(context.Background(), third)
	if err == nil || !strings.Contains(err.Error(), "budget") || client.calls.Load() != 6 {
		t.Fatalf("aggregate campaign budget did not block a new run: %v, calls=%d", err, client.calls.Load())
	}
}

func TestEngineeringInsightRunnerParsesFinalContentWithoutReasoningPrefix(t *testing.T) {
	const reasoningContent = "private reasoning_content"
	const reasoning = "private reasoning"
	client := &fakeEngineeringInsightClient{response: &llm.ChatResponse{Model: "model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: runnerValidResponse(), ReasoningContent: reasoningContent, Reasoning: reasoning}, FinishReason: "stop"}}, Usage: llm.ChatUsage{CompletionTokens: 7}}}
	cfg := runnerTestConfig(t, "reasoning-with-final", EngineeringInsightCollectRunMode, 3, client)
	receipt, handoff, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range receipt.Attempts {
		if attempt.Outcome != "completed" || !attempt.UsableSummary || !attempt.EmittedResponse {
			t.Fatalf("final JSON was not parsed independently of reasoning: %+v", attempt)
		}
	}
	for _, response := range handoff.Responses {
		if !strings.Contains(response, reasoningContent) || !strings.Contains(response, reasoning) || !strings.Contains(response, runnerValidResponse()) {
			t.Fatalf("private scoring handoff omitted emitted material: %q", response)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(cfg.Root, runnerRelativeDirectory, cfg.RunID+".json"))
	if err != nil || strings.Contains(string(manifest), reasoningContent) || strings.Contains(string(manifest), reasoning) {
		t.Fatalf("source-free receipt retained reasoning: %s, %v", manifest, err)
	}
	handoff.Discard()
}

func TestEngineeringInsightRunnerRetainsReasoningOnlyLengthResponseForPrivateScoring(t *testing.T) {
	const reasoning = "private length-limited reasoning"
	client := &fakeEngineeringInsightClient{response: &llm.ChatResponse{Model: "model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", ReasoningContent: reasoning}, FinishReason: "length"}}, Usage: llm.ChatUsage{CompletionTokens: 31}}}
	cfg := runnerTestConfig(t, "reasoning-only-length", EngineeringInsightCollectRunMode, 3, client)
	receipt, handoff, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 3 || receipt.Consumption.Requests != 3 || receipt.Consumption.OutputTokens != 93 {
		t.Fatalf("consumption did not retain reserved attempts and usage: calls=%d receipt=%+v", client.calls.Load(), receipt.Consumption)
	}
	for _, attempt := range receipt.Attempts {
		if attempt.Outcome != "truncated" || attempt.FinishReason != "length" || attempt.OutputTokens != 31 || !attempt.EmittedResponse || attempt.UsableSummary {
			t.Fatalf("reasoning-only response lost failure metadata: %+v", attempt)
		}
	}
	schedule := make([]EngineeringInsightExpectedAttempt, 0, len(cfg.Cases))
	for _, item := range cfg.Cases {
		schedule = append(schedule, item.Expected)
	}
	if _, err := ValidateEngineeringInsightEvaluationReceipt(receipt, EngineeringInsightEvaluationExpectation{CandidateID: cfg.CandidateID, Provider: cfg.Provider, Model: cfg.Model, PromptVersion: cfg.PromptVersion, CorpusID: cfg.CorpusID, CorpusDigest: cfg.CorpusDigest, BaseRevision: cfg.BaseRevision, MaxRequests: 6, MaxOutputTokens: qualificationOutputTokenCap, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, Schedule: schedule}); err != nil {
		t.Fatalf("receipt rejected reasoning-only digest evidence: %v", err)
	}
	for _, response := range handoff.Responses {
		if response != reasoning {
			t.Fatalf("private scoring material = %q, want reasoning", response)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(cfg.Root, runnerRelativeDirectory, cfg.RunID+".json"))
	if err != nil || strings.Contains(string(manifest), reasoning) {
		t.Fatalf("source-free receipt retained private reasoning: %s, %v", manifest, err)
	}
	reloaded, err := LoadEngineeringInsightScoringHandoff(cfg.Root, cfg.RunID)
	if err != nil || len(reloaded.Responses) != 3 {
		t.Fatalf("reasoning-only private handoff did not reload: %d, %v", len(reloaded.Responses), err)
	}
	reloaded.Discard()
	handoff.Discard()
}

func TestEngineeringInsightRunnerRetainsReasoningOnlyStoppedResponseForPrivateScoring(t *testing.T) {
	const reasoning = "private stopped reasoning"
	client := &fakeEngineeringInsightClient{response: &llm.ChatResponse{Model: "model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Reasoning: reasoning}, FinishReason: "stop"}}, Usage: llm.ChatUsage{CompletionTokens: 29}}}
	cfg := runnerTestConfig(t, "reasoning-only-stop", EngineeringInsightCollectRunMode, 3, client)
	receipt, handoff, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 3 || receipt.Consumption.Requests != 3 || receipt.Consumption.OutputTokens != 87 {
		t.Fatalf("stopped reasoning-only response retried or lost consumption: calls=%d receipt=%+v", client.calls.Load(), receipt.Consumption)
	}
	digest := sha256.Sum256([]byte(reasoning))
	wantDigest := hex.EncodeToString(digest[:])
	for _, attempt := range receipt.Attempts {
		if attempt.Outcome != "malformed" || attempt.FinishReason != "stop" || attempt.OutputTokens != 29 || !attempt.EmittedResponse || attempt.ResponseDigest != wantDigest || attempt.UsableSummary {
			t.Fatalf("stopped reasoning-only response lost metadata: %+v", attempt)
		}
	}
	schedule := make([]EngineeringInsightExpectedAttempt, 0, len(cfg.Cases))
	for _, item := range cfg.Cases {
		schedule = append(schedule, item.Expected)
	}
	if _, err := ValidateEngineeringInsightEvaluationReceipt(receipt, EngineeringInsightEvaluationExpectation{CandidateID: cfg.CandidateID, Provider: cfg.Provider, Model: cfg.Model, PromptVersion: cfg.PromptVersion, CorpusID: cfg.CorpusID, CorpusDigest: cfg.CorpusDigest, BaseRevision: cfg.BaseRevision, MaxRequests: 6, MaxOutputTokens: qualificationOutputTokenCap, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, Schedule: schedule}); err != nil {
		t.Fatalf("receipt rejected stopped reasoning-only digest evidence: %v", err)
	}
	for _, response := range handoff.Responses {
		if response != reasoning {
			t.Fatalf("private scoring material = %q, want reasoning", response)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(cfg.Root, runnerRelativeDirectory, cfg.RunID+".json"))
	if err != nil || strings.Contains(string(manifest), reasoning) {
		t.Fatalf("source-free receipt retained private reasoning: %s, %v", manifest, err)
	}
	handoff.Discard()
}

func TestEngineeringInsightRunnerFingerprintBindsOptionalSamplingControls(t *testing.T) {
	cfg := runnerTestConfig(t, "sampling-fingerprint", EngineeringInsightCollectRunMode, 3, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	baseline := buildRunnerFingerprint(cfg)
	topP := float32(0)
	cfg.Profile.TopP = &topP
	withTopP := buildRunnerFingerprint(cfg)
	if baseline == withTopP {
		t.Fatal("omitted and explicit zero top_p have the same fingerprint")
	}
	topP = 0.95
	changed := buildRunnerFingerprint(cfg)
	if withTopP == changed {
		t.Fatal("top_p value change did not change fingerprint")
	}
	cloneValue := float32(0.95)
	cfg.Profile.TopP = &cloneValue
	if changed == baseline || buildRunnerFingerprint(cfg) != changed {
		t.Fatal("sampling fingerprint did not deterministically bind the configured value")
	}
}

func TestEngineeringInsightRunnerFingerprintBindsResponseSchemaIdentity(t *testing.T) {
	cfg := runnerTestConfig(t, "schema-fingerprint", EngineeringInsightCollectRunMode, 3, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	identity, err := app.FileAnalysisResponseSchema().Identity()
	if err != nil {
		t.Fatal(err)
	}
	if buildRunnerFingerprintWithSchemaIdentity(cfg, identity) == buildRunnerFingerprintWithSchemaIdentity(cfg, identity+"-changed") {
		t.Fatal("response schema identity did not change the resumable-run fingerprint")
	}
}

func TestEngineeringInsightRunnerTerminatesAfterPermanentStructuredRequestRejection(t *testing.T) {
	for _, test := range []struct {
		name                               string
		config                             func(*fakeEngineeringInsightClient) EngineeringInsightRunnerConfig
		wantDevelopment, wantQualification int
	}{
		{name: "development", config: func(client *fakeEngineeringInsightClient) EngineeringInsightRunnerConfig {
			return runnerTestConfig(t, "permanent-development", EngineeringInsightDevelopmentRunMode, 3, client)
		}, wantDevelopment: 1},
		{name: "qualification", config: func(client *fakeEngineeringInsightClient) EngineeringInsightRunnerConfig {
			return qualificationRunnerTestConfig(t, "permanent-qualification", client)
		}, wantQualification: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeEngineeringInsightClient{err: llm.ErrStructuredRequestRejected}
			cfg := test.config(client)
			receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
			if !errors.Is(err, ErrEngineeringInsightRunTerminated) || client.calls.Load() != 1 || receipt.Consumption.Requests != 1 || len(receipt.Attempts) != 1 || receipt.Attempts[0].Outcome != "failed" || receipt.Attempts[0].FinishReason != "error" {
				t.Fatalf("permanent rejection result: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
			}
			directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
			campaign, err := loadEvaluationCampaign(filepath.Join(directory, "campaign.json"))
			if err != nil || campaign.DevelopmentRequests != test.wantDevelopment || campaign.QualificationRequests != test.wantQualification {
				t.Fatalf("campaign after permanent rejection: %+v, %v", campaign, err)
			}
			manifest, exists, err := loadRunManifest(filepath.Join(directory, cfg.RunID+".json"))
			if err != nil || !exists || !manifest.Finished || manifest.TerminalReason != permanentRequestRejectionReason {
				t.Fatalf("terminal manifest: %+v exists=%t err=%v", manifest, exists, err)
			}
			if exported, err := LoadEngineeringInsightEvaluationReceipt(cfg.Root, cfg.RunID); err != nil || exported.Consumption.Requests != 1 {
				t.Fatalf("terminal receipt export: %+v, %v", exported, err)
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRunTerminated) || client.calls.Load() != 1 {
				t.Fatalf("terminal run resumed dispatch: calls=%d err=%v", client.calls.Load(), err)
			}
			campaign, err = loadEvaluationCampaign(filepath.Join(directory, "campaign.json"))
			if err != nil || campaign.DevelopmentRequests != test.wantDevelopment || campaign.QualificationRequests != test.wantQualification {
				t.Fatalf("terminal resume changed campaign: %+v, %v", campaign, err)
			}
		})
	}
}

func TestEngineeringInsightRunnerTerminatesAfterBackendValidationRejection(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"detail":"invalid max_tokens"}`))
	}))
	defer server.Close()
	cfg := runnerTestConfig(t, "backend-validation-rejection", EngineeringInsightDevelopmentRunMode, 3, nil)
	cfg.Profile.APIBaseURL = server.URL
	cfg.Client = llm.NewEvaluationClient(cfg.Profile)
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if !errors.Is(err, ErrEngineeringInsightRunTerminated) || requests.Load() != 1 || receipt.Consumption.Requests != 1 || len(receipt.Attempts) != 1 {
		t.Fatalf("backend validation result: receipt=%+v requests=%d err=%v", receipt, requests.Load(), err)
	}
	if receipt.Attempts[0].Outcome != "failed" || receipt.Attempts[0].FinishReason != "error" {
		t.Fatalf("backend validation attempt = %+v", receipt.Attempts[0])
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRunTerminated) || requests.Load() != 1 {
		t.Fatalf("backend validation rejection replayed: requests=%d err=%v", requests.Load(), err)
	}
}

func TestEngineeringInsightRunnerContinuesAfterMalformedModelContent(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: "not JSON"}
	cfg := runnerTestConfig(t, "malformed-content", EngineeringInsightCollectRunMode, 3, client)
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil || client.calls.Load() != 3 || len(receipt.Attempts) != 3 {
		t.Fatalf("malformed content collection: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
	}
	for _, attempt := range receipt.Attempts {
		if attempt.Outcome != "malformed" {
			t.Fatalf("malformed content stopped or changed outcome: %+v", attempt)
		}
	}
}
