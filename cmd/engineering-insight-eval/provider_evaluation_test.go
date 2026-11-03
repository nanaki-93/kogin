package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/insighteval"
)

func TestEvaluationBugProfilePreservesExplicitSamplingControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := `model_scopes:
  analyze: {api_base_url: http://127.0.0.1:1234/v1, model: analyze}
  bug:
    api_base_url: http://127.0.0.1:1234/v1
    model: qwen/qwen3.8-27b
    reasoning_effort: low
    temperature: 1
    top_p: 0.95
    top_k: 20
    min_p: 0
    presence_penalty: 0
    repeat_penalty: 1
  code: {api_base_url: http://127.0.0.1:1234/v1, model: code}
`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := evaluationBugProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxTokens != 4096 || profile.ReasoningEffort != "low" || profile.Temperature != 1 || profile.TopP == nil || *profile.TopP != 0.95 || profile.TopK == nil || *profile.TopK != 20 || profile.MinP == nil || *profile.MinP != 0 || profile.PresencePenalty == nil || *profile.PresencePenalty != 0 || profile.RepeatPenalty == nil || *profile.RepeatPenalty != 1 {
		t.Fatalf("evaluation profile = %+v", profile)
	}
}

func TestRunnerUsesActualDevelopmentCorpusOnceWithItsControl(t *testing.T) {
	data, err := os.ReadFile("../../internal/app/testdata/engineering-insight-eval/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := runnerCasesForMode(data, insighteval.EngineeringInsightDevelopmentRunMode, "engineering-insight-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3 {
		t.Fatalf("development cases = %d, want 3", len(cases))
	}
	controls := 0
	for _, item := range cases {
		if item.Expected.Intent == "control" {
			controls++
		}
		if item.Expected.Repetition != 1 {
			t.Fatalf("unexpected repetition: %+v", item.Expected)
		}
	}
	if controls != 1 {
		t.Fatalf("controls = %d, want 1", controls)
	}
}

func TestRecoveryV2CommandSelectsUniqueDevelopmentAndQualificationSchedules(t *testing.T) {
	developmentData, err := os.ReadFile("../../internal/app/testdata/engineering-insight-eval/v2-development.json")
	if err != nil {
		t.Fatal(err)
	}
	development, err := runnerCasesForMode(developmentData, insighteval.EngineeringInsightDevelopmentRunMode, "engineering-insight-v2")
	if err != nil || len(development) != 12 {
		t.Fatalf("v2 development schedule = %d, %v", len(development), err)
	}
	seen := map[string]bool{}
	for _, item := range development {
		if seen[item.Expected.CaseName] || item.Expected.Partition != "development" || item.Expected.Repetition != 1 || item.Expected.Attempt != 1 {
			t.Fatalf("invalid v2 development attempt: %+v", item.Expected)
		}
		seen[item.Expected.CaseName] = true
	}

	qualificationDocuments := make([]string, 24)
	for index := range qualificationDocuments {
		intent := "substantive"
		if index >= 16 {
			intent = "control"
		}
		qualificationDocuments[index] = fmt.Sprintf(`{"name":"qualification-%02d","partition":"qualification","intent":"%s","source":"package fixture\n"}`, index, intent)
	}
	qualificationData := []byte("[" + strings.Join(qualificationDocuments, ",") + "]")
	qualification, err := runnerCasesForMode(qualificationData, insighteval.EngineeringInsightQualificationRunMode, "engineering-insight-v2")
	if err != nil || len(qualification) != 24 {
		t.Fatalf("v2 qualification schedule = %d, %v", len(qualification), err)
	}
	if _, err := uniqueSchedule(qualificationData, "qualification", 24, 16); err != nil {
		t.Fatalf("valid source-free qualification schedule rejected: %v", err)
	}
	duplicate := strings.Replace(string(qualificationData), `"qualification-01"`, `"qualification-00"`, 1)
	if _, err := uniqueSchedule([]byte(duplicate), "qualification", 24, 16); err == nil {
		t.Fatal("duplicate v2 qualification IDs were accepted")
	}
}
