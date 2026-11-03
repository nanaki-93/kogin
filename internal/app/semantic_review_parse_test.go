package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestSemanticAnalysisRejectsUnknownParentFields(t *testing.T) {
	target := project.IndexFile{Path: "main.go"}
	for _, output := range []string{
		`{"purpose":"Delegates work.","risks":[{"category":"bugs","severity":"invented","summary":"Invalid risk."}]}`,
		`{"purpose":""}`,
		`{"purpose":"Delegates work.","unexpected":true}`,
		`{"purpose":"Delegates work.","symbol_explanations":{"Run":"Legacy field."}}`,
	} {
		if _, err := parseSemanticReviewResponse(output, target, ""); err == nil {
			t.Fatal("accepted an invalid parent")
		}
	}
}

func TestSemanticCategoriesKeepGroundedTargetsAndOptionalDegradation(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	for _, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		t.Run(string(category), func(t *testing.T) {
			for _, path := range []string{"main.go", "other.go"} {
				output := strings.Replace(validSemanticAnalysis, `"risks":[]`, fmt.Sprintf(`"risks":[{"category":%q,"severity":"low","summary":"Conditional finding.","task_spec":{"schema_version":"2","target_path":%q,"acceptance_criteria":["Preserve local behavior."],"non_goals":[]},"engineering_insight":false}]`, category, path), 1)
				parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
				if err != nil || len(parsed.Risks) != 1 {
					t.Fatalf("parse = %+v, %v", parsed, err)
				}
				risk := parsed.Risks[0]
				if risk.Category != category || risk.EngineeringInsight != nil || parsed.Diagnostics.OptionalInsight() != "rejected" {
					t.Fatalf("optional degradation lost category: %+v", parsed)
				}
				if path == "main.go" && (risk.TaskSpec == nil || risk.TaskSpec.TargetPath != target.Path) {
					t.Fatalf("lost grounded target: %+v", risk.TaskSpec)
				}
				if path != "main.go" && (risk.TaskSpec != nil || len(parsed.Diagnostics.TaskSpecDegradedRiskIndices) != 1) {
					t.Fatal("category granted cross-file target authority")
				}
			}
		})
	}
}

func TestSemanticRiskTitlesPreserveLegacyContent(t *testing.T) {
	for _, title := range []string{"", "Missing validation", "  Missing\n validation  "} {
		output := strings.Replace(validSemanticAnalysis, `"risks":[]`, fmt.Sprintf(`"risks":[{"category":"bugs","severity":"high","title":%q,"summary":"Run accepts unchecked input."},{"category":"bugs","severity":"medium","summary":"Run ignores write errors."}]`, title), 1)
		parsed, err := parseSemanticReviewResponse(output, project.IndexFile{Path: "main.go"}, "package main")
		if err != nil || len(parsed.Risks) != 2 {
			t.Fatalf("parsed risks = %+v, %v", parsed.Risks, err)
		}
		findings := project.SuggestedFindingsForFile(project.FileAnalysis{Status: project.AnalysisStatusFresh, Risks: parsed.Risks})
		wantTitle := "Missing validation"
		if title == "" {
			wantTitle = "Run accepts unchecked input."
		}
		if findings[0].Title != wantTitle || findings[0].Message != "Run accepts unchecked input." || findings[1].Title != "Run ignores write errors." {
			t.Fatalf("adapted titles lost content: %+v", findings)
		}
	}
}

func TestFileAnalysisOverLimitInsightsPreserveParents(t *testing.T) {
	field := strings.Repeat("界", fileAnalysisInsightMaxChars+1)
	overAggregateLimit := `{"mechanism":"` + field + `","why_it_matters_here":"` + field + `","tradeoff_or_failure_mode":"` + field + `","transferable_lesson":"` + field + `"}`
	output := `{"purpose":"Summarizes one file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","title":"Conditional concern","summary":"Conditional concern.","engineering_insight":` + overAggregateLimit + `}],"suggestions":[{"title":"Clarify behavior","summary":"Keep the call explicit.","engineering_insight":` + overAggregateLimit + `}],"engineering_insight":` + overAggregateLimit + `}`

	parsed, err := parseSemanticReviewResponse(output, project.IndexFile{Path: "main.go"}, "package main")
	if err != nil || parsed.Purpose != "Summarizes one file." || len(parsed.Risks) != 1 || parsed.Risks[0].Summary != "Conditional concern." || len(parsed.Suggestions) != 1 || parsed.Suggestions[0].Title != "Clarify behavior" {
		t.Fatalf("over-limit insight changed parent data: %+v, %v", parsed, err)
	}
	if parsed.EngineeringInsight != nil || parsed.Risks[0].EngineeringInsight != nil || parsed.Suggestions[0].EngineeringInsight != nil {
		t.Fatalf("over-limit insights were retained: %+v", parsed)
	}
}

func TestParseSemanticAnalysisRejectsInvalidJSONEscape(t *testing.T) {
	output := `{"purpose":"Summary.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[{"title":"Suggestion","summary":"Summary","action":"bad\q"}]}`
	if _, err := parseSemanticReviewResponse(output, project.IndexFile{Path: "main.go"}, "package main\n"); err == nil {
		t.Fatal("invalid JSON escape was accepted")
	}
}

func TestSemanticBugTaskDropsUntrustedTargetsAndInvalidOptionalTests(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	valid := func(task string) string {
		return `{"purpose":"Explains.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"high","summary":"Risk.","task_spec":` + task + `}],"suggestions":[]}`
	}
	spec := func(path string) string {
		return `{"schema_version":"2","target_path":"` + path + `","acceptance_criteria":["Handle the error."],"non_goals":[]}`
	}
	for _, output := range []string{
		valid(spec("other.go")),
		valid(`{"schema_version":"2","target_path":"main.go","acceptance_criteria":["` + strings.Repeat("x", project.MaxBugTaskItemBytes+1) + `"],"non_goals":[]}`),
		valid(`{"schema_version":"2","symbol":"Run","acceptance_criteria":["Handle the error."],"non_goals":[]}`),
	} {
		parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
		if err != nil || parsed.Risks[0].TaskSpec != nil {
			t.Fatalf("untrusted task was retained: %+v, %v", parsed, err)
		}
	}

	for _, candidate := range []string{
		`{"path":"main_test.go","name":"TestRun","content":"package other\nfunc TestRun() {}"}`,
		`{"path":"main_test.go","name":"TestRun","content":"package main\nfunc TestRun( {"}`,
		`{"path":"sub/main_test.go","name":"TestRun","content":"package main\nfunc TestRun() {}"}`,
		`{"path":"main.go","name":"TestRun","content":"package main\nfunc TestRun() {}"}`,
		`{"path":"run_test.py","name":"test_run","content":"def test_run():\n    pass\n"}`,
	} {
		output := valid(`{"schema_version":"2","target_path":"main.go","acceptance_criteria":["Handle the error."],"non_goals":[],"test_candidate":` + candidate + `}`)
		parsed, err := parseSemanticReviewResponse(output, target, "package main\nfunc Run() {}")
		if err != nil || parsed.Risks[0].TaskSpec == nil || parsed.Risks[0].TaskSpec.TestCandidate != nil {
			t.Fatalf("invalid optional test = %+v, %v", parsed.Risks[0].TaskSpec, err)
		}
	}
}

func TestSemanticDiagnosticsPreserveRejectedParentClassifications(t *testing.T) {
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	base := `{"purpose":"Explains.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","summary":"Conditional.","task_spec":{},"engineering_insight":"private insight"}],"suggestions":[],"engineering_insight":null}`
	for _, test := range []struct {
		name, output, wantError string
		count                   int
	}{
		{"missing purpose", strings.Replace(base, `"Explains."`, `""`, 1), "incomplete or exceeds limits", 2},
		{"invalid risk", strings.Replace(base, `"low"`, `"critical"`, 1), "invalid risk", 2},
		{"oversized parent", strings.Replace(base, `"Explains."`, `"`+strings.Repeat("x", maxSemanticAnalysisBytes)+`"`, 1), "empty or too large", 2},
		{"excess risks", strings.Replace(base, `"risks":[`, `"risks":[`+strings.Repeat(`{"category":"bugs","severity":"low","summary":"risk"},`, 32), 1), "incomplete or exceeds limits", 34},
		{"invalid JSON", base + ` trailing`, "must contain one object", 0},
		{"unknown parent field", strings.Replace(base, `"purpose":`, `"unknown":true,"purpose":`, 1), "unknown field", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseSemanticReviewResponse(test.output, target, "package main")
			if err == nil || !strings.Contains(err.Error(), test.wantError) || parsed.Purpose != "" || len(parsed.Risks) != 0 {
				t.Fatalf("parent failure changed: parsed=%+v error=%v", parsed, err)
			}
			assessment := AssessFileAnalysisEvaluation(test.output, target, "package main")
			if assessment.UsableSummary {
				t.Fatal("evaluation accepted rejected parent")
			}
			diagnostics := assessment.Diagnostics
			if len(diagnostics.Insights) != test.count {
				t.Fatalf("diagnostic count=%d, want %d", len(diagnostics.Insights), test.count)
			}
			if test.count > 0 {
				last := diagnostics.Insights[test.count-1]
				if diagnostics.Insights[0].Reason != project.OptionalEngineeringInsightNull || last.Reason != project.OptionalEngineeringInsightInvalidShape || last.Index == nil || *last.Index != test.count-2 || !reflect.DeepEqual(diagnostics.TaskSpecDegradedRiskIndices, []int{test.count - 2}) {
					t.Fatalf("decoded parent diagnostics changed: %+v", diagnostics)
				}
			}
			encoded, err := json.Marshal(diagnostics)
			if err != nil || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "Explains") {
				t.Fatalf("diagnostics contain response text: %s, %v", encoded, err)
			}
		})
	}
}

func TestSemanticDiagnosticsKeepAcceptedInsightsBeyondDisplayLimit(t *testing.T) {
	insight := `{"mechanism":"One operation.","why_it_matters_here":"A local call.","tradeoff_or_failure_mode":"Changes its result.","transferable_lesson":"Verify its result."}`
	output := `{"purpose":"Explains.","risks":[{"category":"bugs","severity":"low","summary":"First.","engineering_insight":` + insight + `},{"category":"bugs","severity":"low","summary":"Second.","engineering_insight":` + insight + `}],"suggestions":[{"title":"Keep behavior","engineering_insight":` + insight + `}],"engineering_insight":` + insight + `}`
	parsed, err := parseSemanticReviewResponse(output, project.IndexFile{}, "")
	if err != nil || parsed.EngineeringInsight == nil || parsed.Risks[0].EngineeringInsight == nil || parsed.Risks[1].EngineeringInsight == nil || parsed.Suggestions[0].EngineeringInsight != nil {
		t.Fatalf("display retention changed: %+v, %v", parsed, err)
	}
	if len(parsed.Diagnostics.Insights) != 4 || parsed.Diagnostics.OptionalInsight() != "present" || parsed.Diagnostics.Degraded() {
		t.Fatalf("display omission changed assessment: %+v", parsed.Diagnostics)
	}
	for _, diagnostic := range parsed.Diagnostics.Insights {
		if diagnostic.Reason != project.OptionalEngineeringInsightAccepted {
			t.Fatalf("accepted raw insight was reclassified: %+v", diagnostic)
		}
	}
}
