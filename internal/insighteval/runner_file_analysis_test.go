package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
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
