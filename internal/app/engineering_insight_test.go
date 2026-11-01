package app

import (
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestOptionalInsightsDoNotRejectSemanticParents(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	output := `{"purpose":"Explains.","responsibilities":[],"dependencies":[],"side_effects":[],"engineering_insight":{"mechanism":"A cache entry must remain tied to the reviewed file.","why_it_matters_here":"A late model result otherwise looks current after the source changes.","tradeoff_or_failure_mode":"More identity inputs make stale results unavailable sooner.","transferable_lesson":"Change the file hash and verify the cache reports stale."},"risks":[{"category":"bugs","severity":"low","summary":"A conditional risk.","engineering_insight":{"mechanism":2}}],"suggestions":[{"title":"Keep identity","summary":"Retain the hash.","engineering_insight":{"mechanism":"Hashes bind advice to source.","why_it_matters_here":"Readers can see stale advice without treating it as current evidence.","tradeoff_or_failure_mode":"Additional identity comparisons can invalidate cached advice.","transferable_lesson":"Change the hash and verify the cached analysis becomes stale."}}]}`
	parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
	if err != nil || parsed.EngineeringInsight == nil || parsed.Risks[0].EngineeringInsight != nil || parsed.Suggestions[0].EngineeringInsight == nil {
		t.Fatalf("parsed semantic insight = %+v, %v", parsed, err)
	}

}

func TestFileAnalysisInsightUsesCompleteGroundedAdviceOrOmission(t *testing.T) {
	target := project.IndexFile{Path: "gate.go", Language: "Go"}
	lockSource := "package cases\n\nimport (\n\t\"context\"\n\t\"sync\"\n)\n\ntype Gate struct { mu sync.Mutex }\n\nfunc (g *Gate) Wait(ctx context.Context) {\n\tg.mu.Lock()\n\tdefer g.mu.Unlock()\n\t<-ctx.Done()\n}\n"
	complete := `{"mechanism":"Wait holds g.mu while it blocks on context cancellation.","why_it_matters_here":"If another caller needs g.mu, the lock spans <-ctx.Done() and that caller waits.","tradeoff_or_failure_mode":"Releasing g.mu before waiting would let concurrent Wait calls stop serializing; whether that serialization is needed is not shown.","transferable_lesson":"Cancel one waiter while another acquires g.mu and expect the second caller to proceed only after release."}`
	incomplete := `{"mechanism":"Wait has a mutex.","why_it_matters_here":"Use locks carefully."}`
	parent := func(insight string) string {
		return `{"purpose":"Waits for cancellation.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[],"engineering_insight":` + insight + `}`
	}

	positive, err := parseSemanticReviewResponse(parent(complete), target, lockSource)
	if err != nil || positive.EngineeringInsight == nil || positive.EngineeringInsight.TransferableLesson == "" {
		t.Fatalf("complete grounded insight = %+v, %v", positive.EngineeringInsight, err)
	}
	partial, err := parseSemanticReviewResponse(parent(incomplete), target, lockSource)
	if err != nil || partial.Purpose != "Waits for cancellation." || partial.EngineeringInsight != nil {
		t.Fatalf("incomplete optional insight did not omit cleanly = %+v, %v", partial, err)
	}
}

func TestFileAnalysisInsightDiagnosticsKeepRejectionsIsolatedByLocation(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	output := `{"purpose":"Explains.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","summary":"Conditional.","engineering_insight":{"mechanism":"risk","why_it_matters_here":"local"}}],"suggestions":[{"title":"Keep behavior","summary":"No change.","engineering_insight":{"mechanism":"  ","why_it_matters_here":"local","tradeoff_or_failure_mode":"cost","transferable_lesson":"verify"}}],"engineering_insight":{"mechanism":"top","why_it_matters_here":"local","tradeoff_or_failure_mode":"cost","transferable_lesson":"verify"}}`
	parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
	if err != nil || parsed.Purpose != "Explains." || parsed.EngineeringInsight == nil || parsed.Risks[0].EngineeringInsight != nil || parsed.Suggestions[0].EngineeringInsight != nil {
		t.Fatalf("optional rejection changed parent data: %+v, %v", parsed, err)
	}
	diagnosticRecord := parsed.Diagnostics
	diagnostics := diagnosticRecord.Insights
	if len(diagnostics) != 3 {
		t.Fatalf("diagnostic count = %d", len(diagnostics))
	}
	if diagnostics[0].Location != "top_level" || diagnostics[0].Index != nil || diagnostics[0].Reason != project.OptionalEngineeringInsightAccepted || diagnostics[0].Mechanism.Runes != 3 {
		t.Fatalf("top-level diagnostic = %+v", diagnostics[0])
	}
	if diagnostics[1].Location != "risk" || diagnostics[1].Index == nil || *diagnostics[1].Index != 0 || diagnostics[1].Reason != project.OptionalEngineeringInsightEmptyRequiredField || !diagnostics[1].Mechanism.Present || diagnostics[1].TradeoffOrFailureMode.Present {
		t.Fatalf("risk diagnostic = %+v", diagnostics[1])
	}
	if diagnostics[2].Location != "suggestion" || diagnostics[2].Index == nil || *diagnostics[2].Index != 0 || diagnostics[2].Reason != project.OptionalEngineeringInsightEmptyRequiredField || !diagnostics[2].Mechanism.Present || diagnostics[2].Mechanism.Runes != 0 {
		t.Fatalf("suggestion diagnostic = %+v", diagnostics[2])
	}
	state := parsed.Diagnostics
	if state.OptionalInsight() != "rejected" || len(state.TaskSpecDegradedRiskIndices) > 0 {
		t.Fatalf("insight rejection was not isolated: %+v", state)
	}
}

func TestEvaluationOptionalStateSeparatesSymbolAndTaskSpecDegradation(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	output := `{"purpose":"Explains.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","summary":"Conditional.","task_spec":{"schema_version":"1"}}],"suggestions":[]}`
	parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
	if err != nil || len(parsed.Risks) != 1 || parsed.Risks[0].TaskSpec != nil {
		t.Fatalf("parent optional data was not isolated: %+v, %v", parsed, err)
	}
	state := parsed.Diagnostics
	if state.OptionalInsight() != "omitted" || !state.Degraded() || len(state.TaskSpecDegradedRiskIndices) != 1 || state.TaskSpecDegradedRiskIndices[0] != 0 {
		t.Fatalf("non-insight degradation was merged into insight rejection: %+v", state)
	}
}

func TestFileAnalysisInsightPreservesQualifiedUnknownCalleeBehavior(t *testing.T) {
	target := project.IndexFile{Path: "verify.go", Language: "Go"}
	source := "package cases\n\nimport \"context\"\n\ntype Verifier interface { Check(context.Context, string) error }\n\nfunc Verify(ctx context.Context, verifier Verifier, user string) error {\n\treturn verifier.Check(ctx, user)\n}\n"
	output := `{"purpose":"Delegates verification.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[],"engineering_insight":{"mechanism":"Verify delegates to verifier.Check.","why_it_matters_here":"The selected file shows the call with ctx and user but not the verifier implementation, so authorization behavior is unknown here.","tradeoff_or_failure_mode":"Adding a local authorization check may duplicate or conflict with the callee's policy.","transferable_lesson":"Use a fake Verifier and verify the forwarded arguments; test authorization behavior where the implementation is available."}}`
	parsed, err := parseSemanticReviewResponse(output, target, source)
	if err != nil || parsed.EngineeringInsight == nil {
		t.Fatalf("qualified unknown-callee insight = %+v, %v", parsed.EngineeringInsight, err)
	}
	if strings.Contains(strings.ToLower(parsed.EngineeringInsight.WhyItMattersHere), "missing authorization") {
		t.Fatalf("qualified insight asserted an unobserved missing safeguard: %+v", parsed.EngineeringInsight)
	}
}

func TestEngineeringInsightPromptsRequireUsefulGroundedContentOrOmission(t *testing.T) {
	semantic, err := semanticPrompt("package main\nfunc Run() {}", project.Analysis{Name: "fixture", Type: "go"}, &project.ProjectIndex{}, project.IndexFile{Path: "main.go"}, project.ContextManifest{})
	if err != nil {
		t.Fatal(err)
	}
	performance, err := performancePrompt("package main\nfunc Run() {}", project.Analysis{Name: "fixture", Type: "go"}, project.IndexFile{Path: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	for name, prompt := range map[string]string{
		"file": semantic, "performance": performance,
	} {
		for _, required := range []string{"mechanism explains the concrete mechanism", "why_it_matters_here names the exact local evidence", "tradeoff_or_failure_mode names a real trade-off or failure condition", "transferable_lesson gives a reusable lesson with a concrete verification idea", "Omit trivial or generic lessons."} {
			if !strings.Contains(prompt, required) {
				t.Fatalf("%s prompt is missing %q: %s", name, required, prompt)
			}
		}
	}
	for _, requirement := range []string{
		"Ground selected-file behavior and engineering insights in TARGET_SOURCE",
		"TARGET_FACTS, PROJECT_FACTS, and CONTEXT_MANIFEST only for the identity and context they supply",
		"Apply these factual boundaries to the whole final response",
		"State visible behavior as fact only when TARGET_SOURCE demonstrates it",
		"Separate local guarantees from downstream behavior that this file cannot establish",
		"consequences demonstrated by visible local control or data flow",
		"Do not infer unshown downstream safeguards, callers, implementations, or consequences",
		"explain an observable relationship or invariant, why it matters locally, a real limitation or failure mode, and a concrete test with expected observations",
		"An engineering insight can describe a correct existing behavior",
		"does not require a proposed change or discovered defect",
		"Default to tests that characterize current behavior",
		"label it proposed-change and first state the current behavior it would change",
		"do not portray a failing proposed expectation as a present contract",
		"Intentionally omit it for trivial code without a meaningful observable relationship",
	} {
		if !strings.Contains(semantic, requirement) {
			t.Fatalf("file analysis prompt is missing explanation contract %q", requirement)
		}
	}
}

func TestSemanticPromptRequiresTypedInsightsAtEverySupportedLocation(t *testing.T) {
	prompt, err := semanticPrompt("package fixture\nfunc Process() {}", project.Analysis{Name: "fixture", Type: "go"}, &project.ProjectIndex{}, project.IndexFile{Path: "fixture.go"}, project.ContextManifest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{
		"the top level, each risks[] item, and each suggestions[] item",
		"either omit engineering_insight or use null",
		"exactly these four non-empty string fields and no other keys: mechanism, why_it_matters_here, tradeoff_or_failure_mode, and transferable_lesson",
		"Never use a string, array, or partial object",
		"Do not duplicate that lesson in risks or suggestions",
		"bugs for incorrect behavior, performance for avoidable resource costs, or security for a trust-boundary weakness",
		"State the relevant local control/data flow and conditions in summary",
		"never invent measured impact",
		"do not infer missing safeguards in an unseen callee",
		"Omit concerns unsupported by TARGET_SOURCE",
		"General cleanup, explanations and best-practice advice belong in suggestions, not risks",
	} {
		if !strings.Contains(prompt, requirement) {
			t.Fatalf("semantic prompt is missing nested insight contract %q", requirement)
		}
	}
}

func TestFileAnalysisNestedInsightSchemaPreservesParentAndCompleteness(t *testing.T) {
	target := project.IndexFile{Path: "fixture.go", Language: "Go"}
	source := "package fixture\nfunc Process() {}\n"
	topLevel := `{"mechanism":"Process delegates one local operation.","why_it_matters_here":"The selected function calls the local operation once.","tradeoff_or_failure_mode":"Changing the call order can alter observable behavior.","transferable_lesson":"Exercise the call with a recording dependency and verify its order."}`
	nested := `{"mechanism":"The suggestion changes one call site.","why_it_matters_here":"The proposed edit is scoped to Process.","tradeoff_or_failure_mode":"A different call can change the returned result.","transferable_lesson":"Run the focused behavior test after the edit."}`
	response := func(location string, includeNested bool, value string) string {
		base := `{"purpose":"Processes one item.","responsibilities":[],"dependencies":[],"side_effects":[],"engineering_insight":` + topLevel
		switch location {
		case "risk":
			if !includeNested {
				return base + `,"risks":[{"category":"bugs","severity":"low","summary":"A conditional concern."}],"suggestions":[]}`
			}
			return base + `,"risks":[{"category":"bugs","severity":"low","summary":"A conditional concern.","engineering_insight":` + value + `}],"suggestions":[]}`
		case "suggestion":
			if !includeNested {
				return base + `,"risks":[],"suggestions":[{"title":"Adjust call","summary":"Keep behavior explicit."}]}`
			}
			return base + `,"risks":[],"suggestions":[{"title":"Adjust call","summary":"Keep behavior explicit.","engineering_insight":` + value + `}]}`
		}
		return base + `,"risks":[],"suggestions":[]}`
	}

	for _, test := range []struct {
		name, location, value    string
		includeNested            bool
		nestedRetained, complete bool
		optional                 string
	}{
		{name: "risk string is rejected", location: "risk", includeNested: true, value: `"plain advice"`, optional: "rejected"},
		{name: "suggestion string is rejected", location: "suggestion", includeNested: true, value: `"plain advice"`, optional: "rejected"},
		{name: "risk object is retained", location: "risk", includeNested: true, value: nested, nestedRetained: true, complete: true, optional: "present"},
		{name: "suggestion object is retained", location: "suggestion", includeNested: true, value: nested, nestedRetained: true, complete: true, optional: "present"},
		{name: "risk null is complete", location: "risk", includeNested: true, value: `null`, complete: true, optional: "present"},
		{name: "suggestion omission is complete", location: "suggestion", complete: true, optional: "present"},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := response(test.location, test.includeNested, test.value)
			parsed, err := parseSemanticReviewResponse(content, target, source)
			if err != nil || parsed.Purpose != "Processes one item." || parsed.EngineeringInsight == nil {
				t.Fatalf("parent summary was not preserved: %+v, %v", parsed, err)
			}
			var actual *project.EngineeringInsight
			if test.location == "risk" {
				actual = parsed.Risks[0].EngineeringInsight
			} else {
				actual = parsed.Suggestions[0].EngineeringInsight
			}
			if (actual != nil) != test.nestedRetained {
				t.Fatalf("nested insight retained=%t, want %t", actual != nil, test.nestedRetained)
			}
			state := parsed.Diagnostics
			if state.OptionalInsight() != test.optional || (!state.Degraded()) != test.complete {
				t.Fatalf("optional=%q degraded=%t, want optional=%q complete=%t", state.OptionalInsight(), state.Degraded(), test.optional, test.complete)
			}
		})
	}
}

func TestFileAnalysisInsightAllowsSourceBoundedUnlockBeforeWaitGuidance(t *testing.T) {
	source := "package fixture\n\nimport (\n\t\"context\"\n\t\"sync\"\n)\n\ntype Gate struct { mu sync.Mutex }\n\nfunc (g *Gate) Wait(ctx context.Context) {\n\tg.mu.Lock()\n\tg.mu.Unlock()\n\t<-ctx.Done()\n}\n"
	target := project.IndexFile{Path: "gate.go", Language: "Go"}
	response := `{"purpose":"Waits for cancellation after releasing a mutex.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[],"engineering_insight":{"mechanism":"Gate.Wait releases g.mu before blocking on <-ctx.Done(), so the source does not show a mutex held during the cancellation wait.","why_it_matters_here":"g.mu.Unlock() appears before <-ctx.Done() and this function has no later state publication or ownership transition.","tradeoff_or_failure_mode":"Adding a revalidation step here would invent a state transition that this source does not show.","transferable_lesson":"When reviewing lock scope, trace each Lock and Unlock around the blocking operation before proposing a concurrency test."}}`
	parsed, err := parseSemanticReviewResponse(response, target, source)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EngineeringInsight == nil {
		t.Fatal("source-bounded unlock-before-wait insight was omitted")
	}
}
