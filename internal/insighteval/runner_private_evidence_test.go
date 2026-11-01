package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

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
