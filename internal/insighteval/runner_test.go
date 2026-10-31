package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestEngineeringInsightRunnerMetadataReplacesCompactPrivateJSON(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	for _, requests := range []int{1, 2} {
		if err := storeRunnerJSON(path, engineeringInsightCampaign{DevelopmentRequests: requests}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"development_requests":2,"qualification_requests":0}` {
		t.Fatalf("metadata = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("metadata permissions = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "campaign.json" {
		t.Fatalf("metadata directory = %v, %v", entries, err)
	}
}

type failingRunnerMetadata struct{ err error }

func (value failingRunnerMetadata) MarshalJSON() ([]byte, error) { return nil, value.err }

func TestEngineeringInsightRunnerMetadataMarshalFailureRetainsPriorData(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	prior := `{"development_requests":1,"qualification_requests":0}`
	if err := os.WriteFile(path, []byte(prior), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("private source and provider response")
	err := storeRunnerJSON(path, failingRunnerMetadata{err: failure})
	if err == nil || err.Error() != "write evaluation run" || fmt.Sprintf("%+v", err) != "write evaluation run" {
		t.Fatalf("public write error = %v", err)
	}
	if !errors.Is(err, failure) || !strings.Contains(errors.Unwrap(err).Error(), "marshal evaluation metadata") {
		t.Fatalf("missing internal marshal context: %v", errors.Unwrap(err))
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != prior {
		t.Fatalf("prior metadata = %q, %v", data, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("metadata directory after failure = %v, %v", entries, readErr)
	}
}

func TestEngineeringInsightRunnerMetadataRequiresExistingDirectory(t *testing.T) {
	for _, parentIsFile := range []bool{false, true} {
		t.Run(fmt.Sprintf("parentIsFile=%t", parentIsFile), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private-evaluation-directory")
			if parentIsFile {
				if err := os.WriteFile(directory, []byte("prior"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{})
			if err == nil || err.Error() != "write evaluation run" {
				t.Fatalf("public directory error = %v", err)
			}
			if parentIsFile {
				data, readErr := os.ReadFile(directory)
				if readErr != nil || string(data) != "prior" {
					t.Fatalf("prior file = %q, %v", data, readErr)
				}
			} else {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing internal directory error: %v", errors.Unwrap(err))
				}
				if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
					t.Fatalf("missing directory was created: %v", statErr)
				}
			}
		})
	}
}

func TestEngineeringInsightRunnerMetadataReplacementFailureCleansTemp(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "campaign.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	priorPath := filepath.Join(path, "prior.json")
	if err := os.WriteFile(priorPath, []byte("prior"), 0600); err != nil {
		t.Fatal(err)
	}
	err := storeRunnerJSON(path, engineeringInsightCampaign{})
	if err == nil || err.Error() != "write evaluation run" {
		t.Fatalf("public replacement error = %v", err)
	}
	if cause := errors.Unwrap(err); cause == nil || !strings.Contains(cause.Error(), "replace metadata") {
		t.Fatalf("missing internal replacement context: %v", cause)
	}
	data, readErr := os.ReadFile(priorPath)
	if readErr != nil || string(data) != "prior" {
		t.Fatalf("prior destination content = %q, %v", data, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != "campaign.json" {
		t.Fatalf("metadata directory after failure = %v, %v", entries, readErr)
	}
}

func TestEngineeringInsightRunnerPrivateArtifactCollisionRetainsPriorData(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "response.json")
	if err := storePrivateArtifact(path, []byte("prior response")); err != nil {
		t.Fatal(err)
	}
	if err := storePrivateArtifact(path, []byte("next response")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("immutable artifact collision = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "prior response" {
		t.Fatalf("prior artifact = %q, %v", data, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "response.json" {
		t.Fatalf("artifact directory after collision = %v, %v", entries, err)
	}
}

type fakeEngineeringInsightClient struct {
	calls    atomic.Int32
	reply    string
	response *llm.ChatResponse
	err      error
	prompt   string
	schema   llm.JSONSchema
}

func (c *fakeEngineeringInsightClient) ChatWithJSONSchema(_ context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	c.calls.Add(1)
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "TARGET_SOURCE") {
		return nil, errors.New("runner did not use the selected-file production prompt")
	}
	c.prompt = messages[0].Content
	c.schema = schema
	if c.err != nil {
		return nil, c.err
	}
	if c.response != nil {
		return c.response, nil
	}
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: c.reply}, FinishReason: "stop"}}, Usage: llm.ChatUsage{CompletionTokens: 7}}, nil
}

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

func TestEngineeringInsightRunnerStoresOnlyDigestBoundScores(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "scores", EngineeringInsightCollectRunMode, 3, client)
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := buildExpectedAttemptID(cfg.Cases[0].Expected)
	score := EngineeringInsightAttemptScore{ResponseDigest: receipt.Attempts[0].ResponseDigest, Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2}
	updated, err := StoreEngineeringInsightScores(cfg.Root, cfg.RunID, map[string]EngineeringInsightAttemptScore{id: score})
	if err != nil || updated.Attempts[0].Score == nil {
		t.Fatalf("store score = %+v, %v", updated, err)
	}
	score.ResponseDigest = strings.Repeat("0", 64)
	if _, err := StoreEngineeringInsightScores(cfg.Root, cfg.RunID, map[string]EngineeringInsightAttemptScore{id: score}); err == nil {
		t.Fatal("mismatched score digest was accepted")
	}
	data, err := os.ReadFile(filepath.Join(cfg.Root, runnerRelativeDirectory, cfg.RunID+".json"))
	if err != nil || !json.Valid(data) || strings.Contains(string(data), "model reply is private") {
		t.Fatalf("scored manifest is not source-free: %v %s", err, data)
	}
}

func TestEngineeringInsightRunnerPersistsPrivateOptionalDiagnosticsWithoutOverwrite(t *testing.T) {
	const privateText = "private optional insight prose"
	response := `{"purpose":"model reply is private","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","summary":"Conditional.","task_spec":{"schema_version":"1"},"engineering_insight":{"mechanism":"` + privateText + `","why_it_matters_here":"local"}}],"suggestions":[]}`
	client := &fakeEngineeringInsightClient{reply: response}
	cfg := runnerTestConfig(t, "optional-diagnostics", EngineeringInsightCollectRunMode, 3, client)
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	id := buildExpectedAttemptID(cfg.Cases[0].Expected)
	path := buildPrivateOptionalDiagnosticsPath(buildPrivateOptionalDiagnosticsDirectory(directory, cfg.RunID), id)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var artifact engineeringInsightOptionalDiagnostics
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.RunID != cfg.RunID || artifact.AttemptID != id || artifact.ResponseDigest != receipt.Attempts[0].ResponseDigest || len(artifact.Insights) != 2 || len(artifact.TaskSpecDegradedRiskIndices) != 1 || artifact.TaskSpecDegradedRiskIndices[0] != 0 {
		t.Fatalf("diagnostic artifact identity = %+v", artifact)
	}
	if artifact.Insights[0].Location != "top_level" || artifact.Insights[0].Reason != project.OptionalEngineeringInsightAbsent || artifact.Insights[1].Location != "risk" || artifact.Insights[1].Index == nil || *artifact.Insights[1].Index != 0 || artifact.Insights[1].Reason != project.OptionalEngineeringInsightEmptyRequiredField || !artifact.Insights[1].Mechanism.RuneCountKnown || artifact.Insights[1].Mechanism.Runes != len(privateText) {
		t.Fatalf("diagnostic artifact details = %+v", artifact.Insights)
	}
	for _, forbidden := range []string{privateText, "model reply is private", "fixture-secret", "provider-secret"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("diagnostic artifact retained private text %q: %s", forbidden, data)
		}
	}
	if err := storePrivateOptionalDiagnostics(directory, engineeringInsightOptionalDiagnostics{RunID: cfg.RunID, AttemptID: id + "-invalid", ResponseDigest: receipt.Attempts[0].ResponseDigest, Insights: []app.FileAnalysisInsightDiagnostic{{Location: "top_level", Reason: project.OptionalEngineeringInsightReason(privateText), Presence: project.OptionalEngineeringInsightValuePresence}}}); err == nil {
		t.Fatal("diagnostic artifact accepted arbitrary model text as a reason")
	}
	if err := storePrivateOptionalDiagnostics(directory, engineeringInsightOptionalDiagnostics{RunID: cfg.RunID, AttemptID: id, ResponseDigest: receipt.Attempts[0].ResponseDigest, Insights: nil}); err == nil {
		t.Fatal("diagnostic artifact overwrite was accepted")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(data) {
		t.Fatalf("diagnostic artifact changed after overwrite: %s, %v", unchanged, err)
	}
	if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); !errors.Is(err, ErrEngineeringInsightRunFinished) || client.calls.Load() != 3 {
		t.Fatalf("completed run dispatched or rewrote diagnostics: calls=%d err=%v", client.calls.Load(), err)
	}
}

func TestEngineeringInsightRunnerDoesNotReplaceDiagnosticsForAnInterruptedUnknownReservation(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "diagnostic-resume", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	first := cfg.Cases[0].Expected
	manifest := engineeringInsightRunManifest{Fingerprint: buildRunnerFingerprint(cfg), Receipt: buildRunnerReceipt(cfg)}
	manifest.Receipt.Attempts = append(manifest.Receipt.Attempts, buildUnknownReservedAttempt(first, cfg.CandidateID))
	manifest.Receipt.Consumption.Requests = 1
	if err := storeRunnerJSON(filepath.Join(directory, cfg.RunID+".json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := storeRunnerJSON(filepath.Join(directory, "campaign.json"), engineeringInsightCampaign{DevelopmentRequests: 1}); err != nil {
		t.Fatal(err)
	}
	id := buildExpectedAttemptID(first)
	priorResponse := "private interrupted response"
	digest := sha256.Sum256([]byte(priorResponse))
	responseDirectory := buildPrivateResponseDirectory(directory, cfg.RunID)
	diagnosticsDirectory := buildPrivateOptionalDiagnosticsDirectory(directory, cfg.RunID)
	if err := storePrivateResponse(responseDirectory, id, priorResponse); err != nil {
		t.Fatal(err)
	}
	prior := engineeringInsightOptionalDiagnostics{RunID: cfg.RunID, AttemptID: id, ResponseDigest: hex.EncodeToString(digest[:]), Insights: []app.FileAnalysisInsightDiagnostic{{Location: "top_level", Reason: project.OptionalEngineeringInsightAbsent, Presence: project.OptionalEngineeringInsightAbsentPresence}}}
	if err := storePrivateOptionalDiagnostics(directory, prior); err != nil {
		t.Fatal(err)
	}
	diagnosticPath := buildPrivateOptionalDiagnosticsPath(diagnosticsDirectory, id)
	before, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{responseDirectory, diagnosticsDirectory} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private directory mode %q = %v, %v", path, info.Mode(), err)
		}
	}
	for _, artifactPath := range []string{buildPrivateResponsePath(responseDirectory, id), diagnosticPath} {
		info, err := os.Stat(artifactPath)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private artifact mode %q = %v, %v", artifactPath, info.Mode(), err)
		}
	}

	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil || client.calls.Load() != 2 || len(receipt.Attempts) != 3 || receipt.Attempts[0].Outcome != "unknown" {
		t.Fatalf("resume dispatched interrupted attempt: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
	}
	after, err := os.ReadFile(diagnosticPath)
	if err != nil || string(after) != string(before) || strings.Contains(string(after), priorResponse) {
		t.Fatalf("resume replaced or exposed interrupted diagnostic: %s, %v", after, err)
	}
	storedResponse, err := loadPrivateResponse(responseDirectory, id)
	if err != nil || storedResponse != priorResponse {
		t.Fatalf("resume replaced interrupted response: %q, %v", storedResponse, err)
	}
	var restored engineeringInsightOptionalDiagnostics
	if err := json.Unmarshal(after, &restored); err != nil || restored.RunID != cfg.RunID || restored.AttemptID != id || restored.ResponseDigest != hex.EncodeToString(digest[:]) {
		t.Fatalf("interrupted diagnostic binding = %+v, %v", restored, err)
	}
}

func TestEngineeringInsightRunnerLeavesAnUnknownReservationWhenDiagnosticPublishingFails(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := runnerTestConfig(t, "diagnostic-first-failure", EngineeringInsightCollectRunMode, 3, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	first := cfg.Cases[0].Expected
	id := buildExpectedAttemptID(first)
	digest := sha256.Sum256([]byte(runnerValidResponse()))
	prior := engineeringInsightOptionalDiagnostics{RunID: cfg.RunID, AttemptID: id, ResponseDigest: hex.EncodeToString(digest[:]), Insights: []app.FileAnalysisInsightDiagnostic{{Location: "top_level", Reason: project.OptionalEngineeringInsightAbsent, Presence: project.OptionalEngineeringInsightAbsentPresence}}}
	if err := storePrivateOptionalDiagnostics(directory, prior); err != nil {
		t.Fatal(err)
	}
	diagnosticPath := buildPrivateOptionalDiagnosticsPath(buildPrivateOptionalDiagnosticsDirectory(directory, cfg.RunID), id)
	before, err := os.ReadFile(diagnosticPath)
	if err != nil {
		t.Fatal(err)
	}

	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err == nil || client.calls.Load() != 1 || len(receipt.Attempts) != 1 || receipt.Attempts[0].Outcome != "unknown" {
		t.Fatalf("diagnostic write failure did not preserve the unknown reservation: receipt=%+v calls=%d err=%v", receipt, client.calls.Load(), err)
	}
	if _, err := os.Stat(buildPrivateResponsePath(buildPrivateResponseDirectory(directory, cfg.RunID), id)); !os.IsNotExist(err) {
		t.Fatalf("response was published before its diagnostic: %v", err)
	}
	after, err := os.ReadFile(diagnosticPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed diagnostic publish overwrote evidence: %s, %v", after, err)
	}

	resumed, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil || client.calls.Load() != 3 || len(resumed.Attempts) != 3 || resumed.Attempts[0].Outcome != "unknown" {
		t.Fatalf("resume replayed the failed reservation: receipt=%+v calls=%d err=%v", resumed, client.calls.Load(), err)
	}
	after, err = os.ReadFile(diagnosticPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("resume overwrote failed-reservation diagnostics: %s, %v", after, err)
	}
}

func TestEngineeringInsightRunnerRejectsIncompleteFileInsightsAcrossOptionalLocations(t *testing.T) {
	target := project.IndexFile{Path: "fixture.go", Language: "Go"}
	source := "package fixture\nfunc Run() {}\n"
	response := func(location, insight string) string {
		switch location {
		case "top-level":
			return `{"purpose":"Summarizes.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[],"engineering_insight":` + insight + `}`
		case "risk":
			return `{"purpose":"Summarizes.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","summary":"Conditional.","engineering_insight":` + insight + `}],"suggestions":[]}`
		case "suggestion":
			return `{"purpose":"Summarizes.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[{"title":"Keep behavior","summary":"No change.","engineering_insight":` + insight + `}]}`
		default:
			return ""
		}
	}

	for name, insight := range map[string]string{
		"missing trade-off":    `{"mechanism":"Run returns.","why_it_matters_here":"The local function returns.","transferable_lesson":"Call Run and expect its result."}`,
		"missing verification": `{"mechanism":"Run returns.","why_it_matters_here":"The local function returns.","tradeoff_or_failure_mode":"Changing it alters the returned value."}`,
	} {
		for _, location := range []string{"top-level", "risk", "suggestion"} {
			t.Run(name+"/"+location, func(t *testing.T) {
				content := response(location, insight)
				assessment := app.AssessFileAnalysisEvaluation(content, target, source)
				if !assessment.UsableSummary {
					t.Fatal("valid parent rejected")
				}
				state := assessment.Diagnostics
				if state.OptionalInsight() != "rejected" || !state.Degraded() {
					t.Fatalf("incomplete %s insight state = %q, degraded=%t", location, state.OptionalInsight(), state.Degraded())
				}
			})
		}
	}

	client := &fakeEngineeringInsightClient{reply: response("top-level", `{"mechanism":"Run returns.","why_it_matters_here":"The local function returns.","transferable_lesson":"Call Run and expect its result."}`)}
	cfg := runnerTestConfig(t, "incomplete-control", EngineeringInsightCollectRunMode, 3, client)
	receipt, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	control := receipt.Attempts[2]
	if !control.UsableSummary || control.CompleteSummary || !control.OptionalSectionDegraded || control.OptionalInsight != "rejected" {
		t.Fatalf("control attempt treated incomplete insight as complete or omitted: %+v", control)
	}
	report := EngineeringInsightEvaluationReport{}
	buildScoreCounts(&report, cfg.Cases[2].Expected, control)
	if report.OmittedControls != 0 {
		t.Fatalf("incomplete control insight counted as intentional omission: %+v", report)
	}
}

func runnerTestConfig(t *testing.T, runID, mode string, count int, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	cases := make([]EngineeringInsightRunnerCase, 0, count)
	for index := 0; index < count; index++ {
		caseIndex := index % 3
		intent := "substantive"
		if caseIndex == 2 {
			intent = "control"
		}
		cases = append(cases, EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: "case-" + string(rune('a'+caseIndex)), Partition: "development", Intent: intent, Repetition: index/3 + 1, Attempt: 1}, Source: "package fixture\n\n// fixture-secret\nfunc Run() {}\n"})
	}
	return EngineeringInsightRunnerConfig{Root: t.TempDir(), RunID: runID, Mode: mode, CandidateID: "candidate", Provider: "configured", Model: "model", PromptVersion: app.EngineeringInsightPromptVersion(), CorpusID: "corpus", CorpusDigest: strings.Repeat("a", 64), BaseRevision: "base", Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: "http://127.0.0.1:9999", APIKey: "provider-secret", Model: "model", ContextMaxTokens: 32000}, Cases: cases, Client: client}
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

func qualificationRunnerTestConfig(t *testing.T, runID string, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	cases := make([]EngineeringInsightRunnerCase, 0, 24)
	for repetition := 1; repetition <= 2; repetition++ {
		for index := 0; index < 12; index++ {
			intent := "substantive"
			if index >= 8 {
				intent = "control"
			}
			cases = append(cases, EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: "qualification-" + string(rune('a'+index)), Partition: "qualification", Intent: intent, Repetition: repetition, Attempt: 1}, Source: "package fixture\nfunc Run() {}\n"})
		}
	}
	return EngineeringInsightRunnerConfig{Root: t.TempDir(), RunID: runID, Mode: EngineeringInsightQualificationRunMode, CandidateID: "candidate", Provider: "configured", Model: "model", PromptVersion: app.EngineeringInsightPromptVersion(), CorpusID: "corpus", CorpusDigest: strings.Repeat("a", 64), BaseRevision: "base", Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: "http://127.0.0.1:9999", Model: "model", ContextMaxTokens: 32000}, Cases: cases, Client: client}
}

func runnerValidResponse() string {
	return `{"purpose":"model reply is private","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`
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

func TestEngineeringInsightRecoveryV2BindsCompleteHistoricalEvidenceSet(t *testing.T) {
	if len(recoveryV2PredecessorFiles) != 19 {
		t.Fatalf("predecessor evidence count = %d, want campaign, grant ledger, and 17 receipts", len(recoveryV2PredecessorFiles))
	}
	for name, digest := range recoveryV2PredecessorFiles {
		if !isValidDigest(digest) {
			t.Fatalf("predecessor %q has invalid digest %q", name, digest)
		}
	}
}

// Historical accounting remains testable without reauthorizing v13 dispatch.
func TestEngineeringInsightRecoveryV2ReservesExactlyTwelveDevelopmentCases(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	for _, item := range cfg.Cases {
		if reserved, err := reserveRecoveryV2Attempt(directory, cfg, item.Expected); err != nil || !reserved {
			t.Fatalf("reservation = %t, %v", reserved, err)
		}
	}
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if period.DevelopmentRequests != 12 || period.CumulativeDevelopmentRequests != 60 || len(period.DevelopmentSchedule) != 12 || len(period.Reservations) != 12 {
		t.Fatalf("v2 accounting = %+v", period)
	}
	if reserved, err := reserveRecoveryV2Attempt(directory, cfg, cfg.Cases[0].Expected); err != nil || reserved {
		t.Fatalf("replayed reservation = %t, %v", reserved, err)
	}
	if err := consumeRecoveryV2Request(&period, EngineeringInsightDevelopmentRunMode); err == nil {
		t.Fatal("exceeded twelve-request budget")
	}
	manifest := engineeringInsightRunManifest{Receipt: buildRunnerReceipt(cfg)}
	if err := validateRunnerManifest(manifest, cfg); err != nil {
		t.Fatal(err)
	}
	manifest.Receipt.ProtocolVersion = ""
	if err := validateRunnerManifest(manifest, cfg); err == nil {
		t.Fatal("v2 manifest accepted a historical protocol identity")
	}
}

func TestEngineeringInsightRecoveryV2RejectsRetiredPromptWithoutConsumption(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	path := buildRecoveryV2PeriodPath(directory)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{recoveryV2PromptVersion, app.EngineeringInsightPromptVersion()} {
		cfg.PromptVersion = version
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
			t.Fatalf("retired campaign dispatched under %s: %v", version, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatal("rejection changed historical accounting")
		}
		if _, err := os.Stat(filepath.Join(directory, cfg.RunID+".json")); !os.IsNotExist(err) {
			t.Fatalf("rejection created a manifest: %v", err)
		}
	}
}

func TestEngineeringInsightRecoveryV2RejectsConcurrentReservationOwner(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	lock, err := lockRunnerFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	second, err := lockRunnerFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.lock"))
	if second != nil {
		defer second.Close()
	}
	if !errors.Is(err, ErrEngineeringInsightRunLocked) {
		t.Fatalf("concurrent reservation owner = %v", err)
	}
}

func TestEngineeringInsightRecoveryV2PreservesChargedReservationWithoutReplacement(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	period.DevelopmentSchedule = buildRecoveryV2ScheduleIDs(cfg.Cases)
	period.DevelopmentRequests = 1
	period.CumulativeDevelopmentRequests = 49
	period.Reservations[period.DevelopmentSchedule[0]] = true
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	for index, item := range cfg.Cases {
		reserved, err := reserveRecoveryV2Attempt(directory, cfg, item.Expected)
		if err != nil || reserved != (index != 0) {
			t.Fatalf("charged attempt %d reserved=%t, %v", index, reserved, err)
		}
	}
	period, err = loadRecoveryV2Period(directory, cfg.Root)
	if err != nil || period.DevelopmentRequests != 12 || period.CumulativeDevelopmentRequests != 60 {
		t.Fatalf("charged accounting = %+v, %v", period, err)
	}
	for _, runID := range []string{cfg.RunID, "replacement-run"} {
		cfg.RunID = runID
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
			t.Fatalf("retired/replacement run dispatched: %v", err)
		}
	}
}

func TestEngineeringInsightRecoveryV2RejectsIdentityCorpusAndScheduleDrift(t *testing.T) {
	base := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	for name, mutate := range map[string]func(*EngineeringInsightRunnerConfig){
		"candidate": func(cfg *EngineeringInsightRunnerConfig) { cfg.CandidateID = "wrong" },
		"model":     func(cfg *EngineeringInsightRunnerConfig) { cfg.Model = "wrong" },
		"provider":  func(cfg *EngineeringInsightRunnerConfig) { cfg.Provider = "wrong" },
		"endpoint":  func(cfg *EngineeringInsightRunnerConfig) { cfg.Profile.APIBaseURL = "http://127.0.0.1:1234/v1" },
		"reasoning": func(cfg *EngineeringInsightRunnerConfig) { cfg.Profile.ReasoningEffort = "low" },
		"base":      func(cfg *EngineeringInsightRunnerConfig) { cfg.BaseRevision = strings.Repeat("b", 40) },
		"run":       func(cfg *EngineeringInsightRunnerConfig) { cfg.RunID = "replacement" },
		"source":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Source += "\n// drift" },
		"name":      func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.CaseName = "other" },
		"intent":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.Intent = "control" },
		"repeat":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.Repetition = 2 },
		"bytes": func(cfg *EngineeringInsightRunnerConfig) {
			cfg.CorpusJSON = append(append([]byte(nil), cfg.CorpusJSON...), '\n')
		},
		"extra sampler": func(cfg *EngineeringInsightRunnerConfig) {
			value := float32(0)
			cfg.Profile.MinP = &value
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.Cases = append([]EngineeringInsightRunnerCase(nil), base.Cases...)
			cfg.CorpusJSON = append([]byte(nil), base.CorpusJSON...)
			mutate(&cfg)
			client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
			cfg.Client = client
			if isRecoveryV2RunnerConfig(cfg) && validateRecoveryV2RunnerConfig(cfg) == nil && cfg.BaseRevision == base.BaseRevision {
				t.Fatal("historical identity/corpus/schedule guard accepted drift")
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
				t.Fatalf("drift dispatched: %v, calls=%d", err, client.calls.Load())
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2RejectsPredecessorAndCleanHeadDrift(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, EngineeringInsightRunnerConfig){
		"predecessor": func(t *testing.T, cfg EngineeringInsightRunnerConfig) {
			if err := os.WriteFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.json"), []byte(`{"development_requests":47,"qualification_requests":24}`), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"head": func(t *testing.T, cfg EngineeringInsightRunnerConfig) {
			path := filepath.Join(cfg.Root, "changed.txt")
			if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("git", "-C", cfg.Root, "add", "changed.txt")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("stage changed head: %v: %s", err, output)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
			cfg := recoveryV2DevelopmentTestConfig(t, client)
			mutate(t, cfg)
			if reserved, err := reserveRecoveryV2Attempt(filepath.Join(cfg.Root, runnerRelativeDirectory), cfg, cfg.Cases[0].Expected); err == nil || reserved {
				t.Fatalf("historical %s boundary accepted drift: %v", name, err)
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
				t.Fatalf("%s drift dispatched: %v, calls=%d", name, err, client.calls.Load())
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2PeriodRejectsUnboundAndMalformedReservations(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	period.DevelopmentSchedule = buildRecoveryV2ScheduleIDs(cfg.Cases)
	period.DevelopmentRequests = 1
	period.CumulativeDevelopmentRequests = 49
	period.Reservations["junk\x00development\x00junk\x001\x001"] = true
	if isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("unbound reservation ID was accepted")
	}
	period.Reservations = map[string]bool{period.DevelopmentSchedule[0]: true}
	if !isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("bound reservation ID was rejected")
	}
	period.DevelopmentSchedule[0] = "junk\x00development\x00substantive\x001\x001"
	if isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("schedule with a reservation outside the verified corpus was accepted")
	}
}

func TestEngineeringInsightRecoveryV2ReservesFixedQualificationScheduleOnce(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	development := v2DevelopmentExpectation()
	period.DevelopmentSchedule = make([]string, len(development.Schedule))
	period.DevelopmentRequests = recoveryV2DevelopmentRequests
	period.CumulativeDevelopmentRequests = recoveryV2DevelopmentLimit
	for index, attempt := range development.Schedule {
		id := buildExpectedAttemptID(attempt)
		period.DevelopmentSchedule[index] = id
		period.Reservations[id] = true
	}
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	manifest := engineeringInsightRunManifest{Finished: true, Receipt: v2Receipt(development, EngineeringInsightCollectionMode)}
	manifest.Receipt.RunID = recoveryV2DevelopmentRunID
	manifest.Receipt.BaseRevision = period.BaseRevision
	qualification := v2QualificationExpectation()
	cfg.Mode = EngineeringInsightQualificationRunMode
	cfg.RunID = recoveryV2QualificationRunID
	cfg.CorpusDigest = recoveryV2QualificationDigest
	cfg.Cases = make([]EngineeringInsightRunnerCase, len(qualification.Schedule))
	for index, attempt := range qualification.Schedule {
		cfg.Cases[index] = EngineeringInsightRunnerCase{Expected: attempt, Source: "package fixture\n"}
	}
	manifestPath := filepath.Join(directory, recoveryV2DevelopmentRunID+".json")
	manifest.Receipt.Attempts[0].Score = nil
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0]); err == nil || dispatch {
		t.Fatalf("qualification reserved against unscored development: dispatch=%v err=%v", dispatch, err)
	}
	manifest.Receipt.Attempts[0].Score = &EngineeringInsightAttemptScore{ResponseDigest: manifest.Receipt.Attempts[0].ResponseDigest, Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2, CriticalFalseClaim: true}
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0]); err == nil || dispatch {
		t.Fatalf("qualification reserved against failed development: dispatch=%v err=%v", dispatch, err)
	}
	manifest.Receipt.Attempts[0].Score.CriticalFalseClaim = false
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	for index, attempt := range qualification.Schedule {
		dispatch, err := reserveRecoveryV2Attempt(directory, cfg, attempt)
		if err != nil || !dispatch {
			t.Fatalf("qualification reservation %d: dispatch=%v err=%v", index, dispatch, err)
		}
	}
	dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0])
	if err != nil || dispatch {
		t.Fatalf("qualification replay reserved again: dispatch=%v err=%v", dispatch, err)
	}
	period, err = loadRecoveryV2Period(directory, cfg.Root)
	if err != nil || period.QualificationRequests != 24 || period.CumulativeQualificationRequests != 48 || len(period.QualificationSchedule) != 24 {
		t.Fatalf("qualification accounting = %+v, %v", period, err)
	}
}

func TestEngineeringInsightRecoveryV2PeriodJSONIsStrict(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	path := buildRecoveryV2PeriodPath(directory)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"malformed":         []byte(`{"version":`),
		"extra key":         []byte(strings.Replace(string(valid), `{"version"`, `{"extra":true,"version"`, 1)),
		"null journal":      []byte(strings.Replace(string(valid), `"reservations":{}`, `"reservations":null`, 1)),
		"negative counter":  []byte(strings.Replace(string(valid), `"development_requests":0`, `"development_requests":-1`, 1)),
		"wrong schema":      []byte(strings.Replace(string(valid), `"schema_identity":"`, `"schema_identity":"wrong-`, 1)),
		"wrong runtime cap": []byte(strings.Replace(string(valid), `"runtime_context_tokens":119552`, `"runtime_context_tokens":119551`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRecoveryV2Period(directory, cfg.Root); err == nil {
				t.Fatal("invalid period JSON was accepted")
			}
			if err := os.WriteFile(path, valid, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2CoordinatorBoundaryIsExact(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".mini-orca", "autopilot", "coordinator")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	tasks := `"RCV-01":{"phase":"complete"},"RCV-02":{"phase":"complete"},"RCV-03":{"phase":"complete"},"RCV-04":{"phase":"complete"},"RCV-05":{"phase":"complete"},"RCV-06":{"phase":"complete"}`
	path := filepath.Join(directory, "RCV-recovery.json")
	for name, document := range map[string]string{
		"valid":         `{"phase":"RCV-07","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
		"wrong phase":   `{"phase":"RCV-06","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
		"active":        `{"phase":"RCV-07","scheduler_status":"ACTIVE","tasks":{` + tasks + `}}`,
		"incomplete":    `{"phase":"RCV-07","scheduler_status":"PAUSED","tasks":{"RCV-01":{"phase":"complete"}}}`,
		"duplicate key": `{"phase":"RCV-07","phase":"RCV-06","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			err := validateRecoveryCoordinatorBoundary(root)
			if name == "valid" && err != nil {
				t.Fatalf("valid coordinator boundary: %v", err)
			}
			if name != "valid" && err == nil {
				t.Fatal("invalid coordinator boundary was accepted")
			}
		})
	}
}

func recoveryV2DevelopmentTestConfig(t *testing.T, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	root, base := recoveryV2TestRoot(t)
	data, err := os.ReadFile("../app/testdata/engineering-insight-eval/v2-development.json")
	if err != nil {
		t.Fatal(err)
	}
	corpus, ok := decodeRecoveryV2Corpus(data)
	if !ok {
		t.Fatal("decode v2 development corpus")
	}
	cases := make([]EngineeringInsightRunnerCase, len(corpus))
	for index, item := range corpus {
		cases[index] = EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: item.Name, Partition: item.Partition, Intent: item.Intent, Repetition: 1, Attempt: 1}, Source: item.Source}
	}
	topP := float32(.95)
	topK := 20
	return EngineeringInsightRunnerConfig{Root: root, RunID: recoveryV2DevelopmentRunID, Mode: EngineeringInsightDevelopmentRunMode, CandidateID: recoveryV2CandidateID, Provider: recoveryV2Provider, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion, CorpusID: recoveryV2CorpusID, CorpusDigest: recoveryV2DevelopmentDigest, CorpusJSON: data, BaseRevision: base, Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: recoveryV2Endpoint, Model: recoveryV2Model, ReasoningEffort: mediumDevelopmentReasoningEffort, Temperature: 1, TopP: &topP, TopK: &topK, MaxTokens: qualificationOutputTokenCap, ContextMaxTokens: 16384}, Cases: cases, Client: client}
}

func recoveryV2TestRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	initCommand := exec.Command("git", "-C", root, "init")
	if output, err := initCommand.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	rootCommand := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	output, err := rootCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve git root: %v: %s", err, output)
	}
	root = strings.TrimSpace(string(output))
	gitCommand := func(args ...string) string {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	gitCommand("checkout", "-b", "codex/autopilot")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".mini-orca/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand("add", ".gitignore")
	gitCommand("-c", "user.email=test@example.invalid", "-c", "user.name=Test", "commit", "-m", "fixture")
	base := gitCommand("rev-parse", "HEAD")
	directory := filepath.Join(root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	campaign := []byte(`{"development_requests":48,"qualification_requests":24}`)
	if err := os.WriteFile(filepath.Join(directory, "campaign.json"), campaign, 0600); err != nil {
		t.Fatal(err)
	}
	originalPredecessors := recoveryV2PredecessorFiles
	sum := sha256.Sum256(campaign)
	recoveryV2PredecessorFiles = map[string]string{"campaign.json": hex.EncodeToString(sum[:])}
	t.Cleanup(func() { recoveryV2PredecessorFiles = originalPredecessors })
	schema, err := app.FileAnalysisResponseSchema().Identity()
	if err != nil {
		t.Fatal(err)
	}
	period := engineeringInsightRecoveryV2Period{Version: "v2", PeriodID: recoveryV2PeriodID, CanonicalRoot: root, Provider: recoveryV2Provider, Endpoint: recoveryV2Endpoint, CandidateID: recoveryV2CandidateID, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion, CorpusID: recoveryV2CorpusID, DevelopmentCorpusDigest: recoveryV2DevelopmentDigest, QualificationCorpusDigest: recoveryV2QualificationDigest, BaseRevision: base, SchemaIdentity: schema, MaxOutputTokens: qualificationOutputTokenCap, InputContextTokens: 16384, RuntimeContextTokens: recoveryV2RuntimeContextTokens, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, ReasoningEffort: mediumDevelopmentReasoningEffort, Temperature: 1, TopP: .95, TopK: 20, Lanes: 1, CumulativeDevelopmentRequests: 48, CumulativeQualificationRequests: 24, DevelopmentLimit: recoveryV2DevelopmentLimit, QualificationLimit: recoveryV2QualificationLimit, PredecessorFiles: buildRecoveryV2PredecessorFiles(), DevelopmentSchedule: []string{}, QualificationSchedule: []string{}, Reservations: map[string]bool{}}
	if !isValidRecoveryV2Period(period, root) {
		canonical, _ := loadCanonicalEvaluationRoot(root)
		t.Fatalf("invalid test period: root=%q canonical=%q identity=%v canonicalIdentity=%v profile=%v accounting=%v predecessors=%+v", root, canonical, hasSameRecoveryV2PeriodIdentity(period, root), hasSameRecoveryV2PeriodIdentity(period, canonical), hasSameRecoveryV2PeriodProfile(period, schema), isValidRecoveryV2PeriodAccounting(period), period.PredecessorFiles)
	}
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	return root, base
}

// Prompt digests bind the reviewed v17 file-analysis prompt and provider metadata.
func TestFileAnalysisEvaluationAdapterPreservesPromptAndProvider(t *testing.T) {
	source := "package fixture\nfunc Run() {}\n"
	target := project.IndexFile{Path: "sample.go", Language: "Go", SizeBytes: int64(len(source)), ContentHash: "source-hash"}
	analysis := project.Analysis{Name: "fixture", Type: "go", Summary: "Synthetic project."}
	index := &project.ProjectIndex{Files: []project.IndexFile{target}}
	for _, test := range []struct {
		endpoint, origin string
		remote           bool
		promptDigest     string
	}{
		{"http://127.0.0.1:1234/v1", "http://127.0.0.1:1234", false, "a6f15a45d04adda4b74759d64439c2fac1c8883b6b3c87ece1e7920b0b46e728"},
		{"http://localhost:1234/v1", "http://localhost:1234", false, "9de40a4ab860009fa71123bd3b6ebcb256d15d0dcbf88885e3dfea57843567b7"},
		{"http://[::1]:1234/v1", "http://[::1]:1234", false, "6660bcd1b3c8fd4e94ae873a252a31b97cee73f08fbbaabb03485b2fb32cc228"},
		{"http://127.25.0.1/v1", "http://127.25.0.1", false, "e9ecd68a1b03e5fc0fa88b32ddd069742d179ff47bc8398f19b4e0883e7fb300"},
		{"https://user:credential@example.com/private-path?token=secret#private-fragment", "https://example.com", true, "af848a375e33f3fad084f47ae2b8565f53c0f12a7a7fe1b70fc3df4e9721ea77"},
		{"http://localhost.example/v1", "http://localhost.example", true, "98e383181460944696f9924a94b29044949a6939f8a5e9aa3f524376cfe216fe"},
		{"invalid endpoint", "", true, "80c081d0e9486a89408cfac6f0b4eeb7dc6c305403e83a1043ac52026205e756"},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			profile := config.ModelProfile{Scope: config.BugModelScope, Model: "fixture-model", APIBaseURL: test.endpoint, APIKey: "private-api-key", ContextMaxTokens: 16384}
			provider := app.FileAnalysisEvaluationProvider(profile)
			if provider.ProviderOrigin != test.origin || provider.RemoteProvider != test.remote {
				t.Fatalf("provider=%+v, want origin=%q remote=%t", provider, test.origin, test.remote)
			}
			encoded, err := json.Marshal(provider)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"credential", "private-path", "token=secret", "private-fragment", "private-api-key"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("provider metadata leaked %q", secret)
				}
			}
			got, err := app.PrepareFileAnalysisEvaluation(source, analysis, index, target, profile)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(got)))
			if err != nil || digest != test.promptDigest {
				t.Fatalf("evaluation prompt differs from reviewed v18 bytes: digest=%s err=%v", digest, err)
			}
		})
	}
	if _, err := app.PrepareFileAnalysisEvaluation(strings.Repeat("x", 64*1024+1), analysis, index, target, config.ModelProfile{}); err == nil {
		t.Fatal("evaluation prompt bypassed production source limit")
	}
}

func TestFileAnalysisEvaluationAssessmentMatchesRunner(t *testing.T) {
	target := project.IndexFile{Path: "sample.go", Language: "Go"}
	for _, test := range []struct {
		name, content, optional string
		usable, degraded        bool
	}{
		{"omission", `{"purpose":"Explains."}`, "omitted", true, false},
		{"explicit null", `{"purpose":"Explains.","engineering_insight":null}`, "omitted", true, false},
		{"accepted", `{"purpose":"Explains.","engineering_insight":{"mechanism":"One operation.","why_it_matters_here":"A local call.","tradeoff_or_failure_mode":"Changes its result.","transferable_lesson":"Verify its result."}}`, "present", true, false},
		{"rejected", `{"purpose":"Explains.","engineering_insight":false}`, "rejected", true, true},
		{"task degradation", `{"purpose":"Explains.","risks":[{"category":"bugs","severity":"low","summary":"Risk.","task_spec":{}}]}`, "omitted", true, true},
		{"category rejected", `{"purpose":"Explains.","risks":[{"severity":"low","summary":"Legacy.","engineering_insight":false}]}`, "rejected", false, true},
		{"parent rejected", `{"purpose":"","engineering_insight":false}`, "rejected", false, true},
		{"not JSON", `private response`, "omitted", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			assessment := app.AssessFileAnalysisEvaluation(test.content, target, "package fixture")
			if assessment.UsableSummary != test.usable || assessment.Diagnostics.OptionalInsight() != test.optional || assessment.Diagnostics.Degraded() != test.degraded {
				t.Fatalf("unexpected assessment=%+v", assessment)
			}
			cfg := EngineeringInsightRunnerConfig{Model: "fixture", CandidateID: "synthetic", Client: &fakeEngineeringInsightClient{reply: test.content}}
			prompt, promptErr := app.PrepareFileAnalysisEvaluation("package fixture", project.Analysis{}, &project.ProjectIndex{}, target, cfg.Profile)
			if promptErr != nil {
				t.Fatal(promptErr)
			}
			attempt, _, diagnostics, dispatchErr := dispatchRunnerPrompt(context.Background(), cfg, EngineeringInsightRunnerCase{Source: "package fixture"}, prompt, &target)
			if dispatchErr != nil || !reflect.DeepEqual(diagnostics, assessment.Diagnostics) || attempt.UsableSummary != test.usable {
				t.Fatalf("runner differs from assessment: attempt=%+v diagnostics=%+v err=%v", attempt, diagnostics, dispatchErr)
			}
			if test.usable && (attempt.OptionalInsight != test.optional || attempt.OptionalSectionDegraded != test.degraded || attempt.CompleteSummary == test.degraded) {
				t.Fatalf("runner classification changed: %+v", attempt)
			}
		})
	}
}
