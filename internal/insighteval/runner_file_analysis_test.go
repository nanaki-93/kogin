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

// Prompt digests bind the reviewed v20 file-analysis prompt and provider metadata.
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
		{"http://127.0.0.1:1234/v1", "http://127.0.0.1:1234", false, "3cc00caea8c64c852d9e2100fb69c9a90154ee884cd35b6b9cfa58f342975574"},
		{"http://localhost:1234/v1", "http://localhost:1234", false, "1cd2dc7fd2fd90300440be5a6c799cd76ce6c83425caa77b81950e22df8c1b23"},
		{"http://[::1]:1234/v1", "http://[::1]:1234", false, "8309e186bf72ceae61b445bf2cbc0c3079121798d286c3c2e5e4c23f5e563218"},
		{"http://127.25.0.1/v1", "http://127.25.0.1", false, "868da1e87590700245d37a3c57041192c5e2084ea16c577011eed1c9c86f0a18"},
		{"https://user:credential@example.com/private-path?token=secret#private-fragment", "https://example.com", true, "3e0359832f8c20de8e95d7129d30cbf36dc81e331d4d92b98d01ab418033a86b"},
		{"http://localhost.example/v1", "http://localhost.example", true, "b3b61aa3a97a8705a168d5d94f81e2cc6b66b97d86a8cb28ea206b22ea59736a"},
		{"invalid endpoint", "", true, "7929b0293be5675b8013b212dbb4bc1bc860d8e15c619c181604e4250fe10410"},
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
				t.Fatalf("evaluation prompt differs from reviewed v20 bytes: digest=%s err=%v", digest, err)
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
