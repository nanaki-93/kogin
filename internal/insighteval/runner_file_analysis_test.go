package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/config"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

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
		{"http://127.0.0.1:1234/v1", "http://127.0.0.1:1234", false, "e0c19871895e34f11702db2783c26645622694f20e81eec3765885cd4d199354"},
		{"http://localhost:1234/v1", "http://localhost:1234", false, "197cf61ddd85c55d46c64f67007f683e136118e1e01d54fe2943e44e09eac26e"},
		{"http://[::1]:1234/v1", "http://[::1]:1234", false, "884a1b1a0ce3bdbaf90287f349b201f27c35d4912087243a4a0ccb06fe41f585"},
		{"http://127.25.0.1/v1", "http://127.25.0.1", false, "c93642ef21415a932f39bdcf67928511775864415aa6e6f207e7b5e08d7344a9"},
		{"https://user:credential@example.com/private-path?token=secret#private-fragment", "https://example.com", true, "7d6174dd60cc74dbb4027ce5fd1ccddd23849c1b6f460ffa4ad0627966ed1212"},
		{"http://localhost.example/v1", "http://localhost.example", true, "d0346d3dd12a93620f3672c712d8e98d3370b46cf13e28e05bb2cc23f6ca8736"},
		{"invalid endpoint", "", true, "ae611ff72c92d420414696d2b8e83899e51760262296544e67e751175bb64e21"},
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
				t.Fatalf("evaluation prompt differs from reviewed v19 bytes: digest=%s err=%v", digest, err)
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
