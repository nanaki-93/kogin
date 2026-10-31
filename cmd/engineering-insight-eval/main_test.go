package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/insighteval"
)

func TestRunEvaluationModeCompletesOfflineDevelopmentLifecycle(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".mini-orca/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".gitignore")
	git(t, root, "-c", "user.email=test@example.invalid", "-c", "user.name=Test", "commit", "-m", "fixture")
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	var calls int
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		_ = json.NewEncoder(writer).Encode(map[string]any{"model": "fixture-model", "choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": `{"purpose":"summary","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`}, "finish_reason": "stop"}}, "usage": map[string]int{"completion_tokens": 7}})
	}))
	defer provider.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := "model_scopes:\n  analyze: {api_base_url: " + provider.URL + ", model: fixture-model}\n  bug: {api_base_url: " + provider.URL + ", model: fixture-model}\n  code: {api_base_url: " + provider.URL + ", model: fixture-model}\n"
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	casesPath, err := filepath.Abs("../../internal/app/testdata/engineering-insight-eval/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	args := lifecycleArgs(root, configPath, casesPath, base, receiptPath)
	if err := runEvaluationMode(args); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d, want 3", calls)
	}
	if err := os.WriteFile(receiptPath, []byte("preserve-this-receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(args); err == nil || calls != 3 {
		t.Fatal("finished collection run was accepted")
	}
	if data, err := os.ReadFile(receiptPath); err != nil || string(data) != "preserve-this-receipt" {
		t.Fatal("finished collection rewrote receipt")
	}
	if err := runEvaluationMode([]string{"-mode", "handoff", "-root", root, "-run-id", "development-one"}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("handoff made a provider call")
	}
	handoff, err := insighteval.LoadEngineeringInsightScoringHandoff(root, "development-one")
	if err != nil {
		t.Fatal(err)
	}
	scores := map[string]insighteval.EngineeringInsightAttemptScore{}
	for id, response := range handoff.Responses {
		digest := sha256.Sum256([]byte(response))
		scores[id] = insighteval.EngineeringInsightAttemptScore{ResponseDigest: hex.EncodeToString(digest[:]), Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2}
	}
	scoresPath := filepath.Join(t.TempDir(), "scores.json")
	data, _ := json.Marshal(scores)
	if err := os.WriteFile(scoresPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode([]string{"-mode", "score", "-root", root, "-run-id", "development-one", "-scores", scoresPath, "-receipt", receiptPath}); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "export.json")
	if err := runEvaluationMode([]string{"-mode", "export", "-root", root, "-run-id", "development-one", "-receipt", exportPath}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("offline lifecycle made a generation call")
	}
	receipt, err := loadReceipt(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	options := evaluationOptions{caseSetPath: casesPath, candidateID: "candidate", provider: "fixture", model: "fixture-model", promptVersion: app.EngineeringInsightPromptVersion(), corpusID: "corpus", baseRevision: base, maxRequests: 6, maxOutputTokens: 4096, attemptTimeoutSeconds: 300}
	expected, err := options.expectationFor(receipt.Mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insighteval.ValidateEngineeringInsightEvaluationReceipt(receipt, expected); err != nil {
		t.Fatal(err)
	}
	badBase := append([]string(nil), args...)
	badBase[len(badBase)-1] = "wrong-base"
	if err := runEvaluationMode(badBase); err == nil {
		t.Fatal("invalid base was accepted")
	}
	badPrompt := append([]string(nil), args...)
	for index := range badPrompt {
		if badPrompt[index] == "-prompt-version" {
			badPrompt[index+1] = "wrong-prompt"
		}
	}
	if err := runEvaluationMode(badPrompt); err == nil || calls != 3 {
		t.Fatal("invalid prompt dispatched a request")
	}
	badID := append([]string(nil), args...)
	for index := range badID {
		if badID[index] == "-run-id" {
			badID[index+1] = ".."
		}
	}
	if err := runEvaluationMode(badID); err == nil || calls != 3 {
		t.Fatal("path-like run ID dispatched a request")
	}
	stateDirectory := filepath.Join(root, ".mini-orca", "autopilot", "engineering-insight-evaluation")
	campaign := filepath.Join(stateDirectory, "campaign.json")
	campaignData, err := os.ReadFile(campaign)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"...json", "..json"} {
		if err := os.WriteFile(filepath.Join(stateDirectory, name), []byte(`{"finished":true}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, runID := range []string{".", ".."} {
		for _, modeArgs := range [][]string{
			{"-mode", "handoff", "-root", root, "-run-id", runID},
			{"-mode", "score", "-root", root, "-run-id", runID, "-scores", filepath.Join(t.TempDir(), "unread-scores.json"), "-receipt", filepath.Join(t.TempDir(), "unwritten-receipt.json")},
			{"-mode", "export", "-root", root, "-run-id", runID, "-receipt", filepath.Join(t.TempDir(), "unwritten-export.json")},
			{"-mode", "discard", "-root", root, "-run-id", runID},
		} {
			if err := runEvaluationMode(modeArgs); err == nil {
				t.Fatalf("%s accepted run ID %q", modeArgs[1], runID)
			}
		}
		if data, err := os.ReadFile(campaign); err != nil || string(data) != string(campaignData) {
			t.Fatalf("run ID %q changed campaign: %v", runID, err)
		}
	}
	for _, name := range []string{"...json", "..json"} {
		if _, err := os.Stat(filepath.Join(stateDirectory, name)); err != nil {
			t.Fatalf("hostile manifest %q changed: %v", name, err)
		}
	}
}

func TestGrantDevelopmentModeRequiresOneUniqueAuthorization(t *testing.T) {
	root := t.TempDir()
	grant := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-extension-1", "-requests", "6"}
	if err := runEvaluationMode(grant); err == nil {
		t.Fatal("grant before original budget exhaustion was accepted")
	}
	state := filepath.Join(root, ".mini-orca", "autopilot", "engineering-insight-evaluation")
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":6,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(grant); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(grant); err == nil {
		t.Fatal("duplicate grant was accepted")
	}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-extension-2", "-requests", "5"}); err == nil {
		t.Fatal("non-six grant was accepted")
	}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "..", "-requests", "6"}); err == nil {
		t.Fatal("path-like authorization ID was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":12,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	recovery := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-recovery-1", "-requests", "6", "-candidate-id", "qwen38-v10-recovery-1", "-model", "qwen/qwen3.8-27b"}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-recovery-1", "-requests", "6"}); err == nil {
		t.Fatal("unbound recovery grant was accepted")
	}
	if err := runEvaluationMode(recovery); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(recovery); err == nil {
		t.Fatal("recovery grant replay was accepted")
	}
	v11 := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-schema-1", "-requests", "6", "-candidate-id", "qwen38-v11-schema-1", "-model", "qwen/qwen3.8-27b", "-prompt-version", "file-analysis-v11"}
	if err := runEvaluationMode(v11); err == nil {
		t.Fatal("v11 grant was accepted before recovery budget exhaustion")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":18,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-schema-1", "-requests", "6", "-candidate-id", "qwen38-v11-schema-1", "-model", "qwen/qwen3.8-27b"}); err == nil {
		t.Fatal("unbound v11 grant was accepted")
	}
	wrongV11Prompt := append([]string(nil), v11...)
	wrongV11Prompt[len(wrongV11Prompt)-1] = "wrong-prompt"
	if err := runEvaluationMode(wrongV11Prompt); err == nil {
		t.Fatal("wrong v11 prompt was accepted")
	}
	if err := runEvaluationMode(v11); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(v11); err == nil {
		t.Fatal("v11 grant replay was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":24,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	v12 := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "authqual05-qwen38-structured-1", "-requests", "6", "-candidate-id", "qwen38-v12-structured-1", "-model", "qwen/qwen3.8-27b", "-prompt-version", "file-analysis-v12"}
	wrongV12Prompt := append([]string(nil), v12...)
	wrongV12Prompt[len(wrongV12Prompt)-1] = "file-analysis-v11"
	if err := runEvaluationMode(wrongV12Prompt); err == nil {
		t.Fatal("wrong structured-output prompt was accepted")
	}
	if err := runEvaluationMode(v12); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(v12); err == nil {
		t.Fatal("structured-output grant replay was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":30,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	thinkingOff := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-thinking-off-1", "-requests", "6", "-candidate-id", "qwen38-v12-thinking-off-1", "-model", "qwen/qwen3.8-27b", "-prompt-version", "file-analysis-v12"}
	wrongThinkingOffPrompt := append([]string(nil), thinkingOff...)
	wrongThinkingOffPrompt[len(wrongThinkingOffPrompt)-1] = "wrong-prompt"
	if err := runEvaluationMode(wrongThinkingOffPrompt); err == nil {
		t.Fatal("wrong thinking-off prompt was accepted")
	}
	if err := runEvaluationMode(thinkingOff); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(thinkingOff); err == nil {
		t.Fatal("thinking-off grant replay was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":36,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	thinkingSchema := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-thinking-schema-1", "-requests", "6", "-candidate-id", "qwen38-v12-thinking-schema-1", "-model", "./models/qwen38-v12-thinking-schema-1", "-prompt-version", "file-analysis-v12"}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-thinking-schema-1", "-requests", "6"}); err == nil {
		t.Fatal("unbound thinking-schema CLI grant was accepted")
	}
	wrongThinkingSchemaPrompt := append([]string(nil), thinkingSchema...)
	wrongThinkingSchemaPrompt[len(wrongThinkingSchemaPrompt)-1] = "wrong-prompt"
	if err := runEvaluationMode(wrongThinkingSchemaPrompt); err == nil {
		t.Fatal("wrong thinking-schema prompt was accepted")
	}
	if err := runEvaluationMode(thinkingSchema); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(thinkingSchema); err == nil {
		t.Fatal("thinking-schema grant replay was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "campaign.json"), []byte(`{"development_requests":42,"qualification_requests":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	medium := []string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-medium-1", "-requests", "6", "-candidate-id", "qwen38-v12-medium-1", "-model", "./models/qwen38-v12-medium-1", "-prompt-version", "file-analysis-v12"}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "qual05-qwen38-medium-1", "-requests", "6"}); err == nil {
		t.Fatal("unbound medium CLI grant was accepted")
	}
	wrongMediumPrompt := append([]string(nil), medium...)
	wrongMediumPrompt[len(wrongMediumPrompt)-1] = "wrong-prompt"
	if err := runEvaluationMode(wrongMediumPrompt); err == nil {
		t.Fatal("wrong medium prompt was accepted")
	}
	if err := runEvaluationMode(medium); err != nil {
		t.Fatal(err)
	}
	if err := runEvaluationMode(medium); err == nil {
		t.Fatal("medium grant replay was accepted")
	}
	if err := runEvaluationMode([]string{"-mode", "grant-development", "-root", root, "-authorization-id", "seventh-grant", "-requests", "6", "-candidate-id", "qwen38-v12-thinking-schema-1", "-model", "./models/qwen38-v12-thinking-schema-1", "-prompt-version", "file-analysis-v12"}); err == nil {
		t.Fatal("eighth grant fallback was accepted")
	}
}

func lifecycleArgs(root, configPath, casesPath, base, receiptPath string) []string {
	return []string{"-mode", "collect", "-root", root, "-run-id", "development-one", "-receipt", receiptPath, "-cases", casesPath, "-config", configPath, "-candidate-id", "candidate", "-provider", "fixture", "-prompt-version", app.EngineeringInsightPromptVersion(), "-corpus-id", "corpus", "-base-revision", base}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestEvaluationExitStatusSeparatesValidCollectionIncompleteFailureAndPass(t *testing.T) {
	for outcome, want := range map[insighteval.EngineeringInsightEvaluationOutcome]int{
		insighteval.EngineeringInsightCollectionOutcome: 0,
		insighteval.EngineeringInsightPassedOutcome:     0,
		insighteval.EngineeringInsightIncompleteOutcome: 2,
		insighteval.EngineeringInsightFailedOutcome:     1,
	} {
		if got := evaluationExitStatus(outcome); got != want {
			t.Fatalf("exit status for %s = %d, want %d", outcome, got, want)
		}
	}
}

func TestRecoveryV2AuthorizationRejectsEveryOverrideBeforeActivation(t *testing.T) {
	for _, extra := range [][]string{
		{"-candidate-id", "other"},
		{"-run-id", "other"},
		{"-requests", "12"},
		{"-cases", "other.json"},
		{"-config", "other.yaml"},
		{"-confirm-remote-provider"},
		{"-scores", "scores.json"},
	} {
		args := append([]string{"-mode", "authorize-recovery-v2", "-root", t.TempDir()}, extra...)
		if err := runEvaluationMode(args); err == nil || !strings.Contains(err.Error(), "accepts only") {
			t.Fatalf("authorization override %v was accepted: %v", extra, err)
		}
	}
	if err := runEvaluationMode([]string{"-mode", "authorize-recovery-v2"}); err == nil || !strings.Contains(err.Error(), "accepts only") {
		t.Fatalf("authorization without explicit root was accepted: %v", err)
	}
}
