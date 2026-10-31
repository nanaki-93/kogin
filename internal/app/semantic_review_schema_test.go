package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSemanticAnalysisRequiresExplicitCategoryInSchemaAndParser(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(fileAnalysisResponseSchemaDocument))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("categories.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("categories.json")
	if err != nil {
		t.Fatal(err)
	}
	target := project.IndexFile{Path: "main.go", Language: "Go"}
	for _, test := range []struct {
		name, field string
		want        project.FindingCategory
	}{
		{"bugs", `"category":"bugs",`, project.FindingCategoryBugs},
		{"performance", `"category":"performance",`, project.FindingCategoryPerformance},
		{"security", `"category":"security",`, project.FindingCategorySecurity},
		{"missing", "", ""},
		{"null", `"category":null,`, ""},
		{"empty", `"category":"",`, ""},
		{"unknown", `"category":"reliability",`, ""},
		{"singular", `"category":"bug",`, ""},
		{"case", `"category":"Bugs",`, ""},
		{"padded", `"category":" bugs ",`, ""},
		{"multiple", `"category":["bugs","security"],`, ""},
		{"extra property", `"category":"bugs","confidence":"verified",`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Identical prose cannot supply a missing or different classification.
			output := strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{`+test.field+`"severity":"high","title":"Conditional concern","summary":"Slow insecure bug text does not classify itself."}]`, 1)
			instance, err := jsonschema.UnmarshalJSON(strings.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(instance); (err == nil) != test.want.Valid() {
				t.Fatalf("schema error = %v", err)
			}
			parsed, parseErr := parseSemanticReviewResponse(output, target, "package main")
			assessment := AssessFileAnalysisEvaluation(output, target, "package main")
			if (parseErr == nil) != test.want.Valid() || assessment.UsableSummary != test.want.Valid() {
				t.Fatalf("category validation differs: parsed=%+v err=%v assessment=%+v", parsed, parseErr, assessment)
			}
			if test.want.Valid() && (len(parsed.Risks) != 1 || parsed.Risks[0].Category != test.want || parsed.Risks[0].Severity != "high") {
				t.Fatalf("lost explicit category: %+v", parsed.Risks)
			}
			if !test.want.Valid() && len(parsed.Risks) != 0 {
				t.Fatal("invalid parent published risks")
			}
		})
	}
}

func TestFileAnalysisResponseSchemaRejectsInvalidOptionalObjects(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(fileAnalysisResponseSchemaDocument))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("file-analysis-schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("file-analysis-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"purpose":"Summarizes one file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","title":"Conditional concern","summary":"Conditional concern.","task_spec":{"schema_version":"2","target_path":"main.go","acceptance_criteria":["Keep behavior."],"non_goals":[]},"engineering_insight":null}],"suggestions":[{"title":"Clarify behavior","summary":"Keep the call explicit.","action":"Use \\\"quoted\\\" text.","engineering_insight":{"mechanism":"Run calls one helper.","why_it_matters_here":"Run is the selected entry point.","tradeoff_or_failure_mode":"Changing call order can alter behavior.","transferable_lesson":"Exercise Run and verify call order."}}]}`
	invalid := map[string]string{
		"missing risk title":    strings.Replace(valid, `"title":"Conditional concern",`, ``, 1),
		"empty risk title":      strings.Replace(valid, `"title":"Conditional concern"`, `"title":""`, 1),
		"non-string risk title": strings.Replace(valid, `"title":"Conditional concern"`, `"title":false`, 1),
		"long risk title":       strings.Replace(valid, `"title":"Conditional concern"`, `"title":"`+strings.Repeat("界", 61)+`"`, 1),
		"scalar insight":        strings.Replace(valid, `"engineering_insight":null`, `"engineering_insight":"advice"`, 1),
		"partial insight":       strings.Replace(valid, `"mechanism":"Run calls one helper.","why_it_matters_here":"Run is the selected entry point.","tradeoff_or_failure_mode":"Changing call order can alter behavior.","transferable_lesson":"Exercise Run and verify call order."`, `"mechanism":"partial"`, 1),
		"wrong nested type":     strings.Replace(valid, `"action":"Use \\\"quoted\\\" text."`, `"action":false`, 1),
		"extra nested property": strings.Replace(valid, `"transferable_lesson":"Exercise Run and verify call order."`, `"transferable_lesson":"Exercise Run and verify call order.","extra":"no"`, 1),
		"legacy symbol field":   strings.Replace(valid, `"purpose":`, `"symbol_explanations":{},"purpose":`, 1),
	}
	if instance, err := jsonschema.UnmarshalJSON(strings.NewReader(valid)); err != nil || schema.Validate(instance) != nil {
		t.Fatalf("valid file-analysis schema instance rejected: %v", err)
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			instance, err := jsonschema.UnmarshalJSON(strings.NewReader(value))
			if err != nil || schema.Validate(instance) == nil {
				t.Fatalf("invalid schema instance accepted: decode=%v", err)
			}
		})
	}
}

func TestFileAnalysisResponseSchemaBoundsInsightFieldsAtEveryLocation(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(fileAnalysisResponseSchemaDocument))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("file-analysis-schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("file-analysis-schema.json")
	if err != nil {
		t.Fatal(err)
	}

	insight := func(length int) string {
		field := strings.Repeat("界", length)
		return `{"mechanism":"` + field + `","why_it_matters_here":"` + field + `","tradeoff_or_failure_mode":"` + field + `","transferable_lesson":"` + field + `"}`
	}
	response := func(topLevel, risk, suggestion string) string {
		return `{"purpose":"Summarizes one file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"low","title":"Conditional concern","summary":"Conditional concern.","engineering_insight":` + risk + `}],"suggestions":[{"title":"Clarify behavior","summary":"Keep the call explicit.","engineering_insight":` + suggestion + `}],"engineering_insight":` + topLevel + `}`
	}

	atLimit := insight(fileAnalysisInsightMaxChars)
	overLimit := insight(fileAnalysisInsightMaxChars + 1)
	for _, test := range []struct {
		name     string
		response string
		valid    bool
	}{
		{name: "exact Unicode boundary", response: response(atLimit, atLimit, atLimit), valid: true},
		{name: "top level exceeds boundary", response: response(overLimit, atLimit, atLimit)},
		{name: "risk exceeds boundary", response: response(atLimit, overLimit, atLimit)},
		{name: "suggestion exceeds boundary", response: response(atLimit, atLimit, overLimit)},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance, err := jsonschema.UnmarshalJSON(strings.NewReader(test.response))
			if err != nil {
				t.Fatal(err)
			}
			if gotValid := schema.Validate(instance) == nil; gotValid != test.valid {
				t.Fatalf("schema validation = %t, want %t", gotValid, test.valid)
			}
		})
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(fileAnalysisResponseSchemaDocument), &raw); err != nil {
		t.Fatal(err)
	}
	defs := raw["$defs"].(map[string]any)
	insightText := defs["insightText"].(map[string]any)
	ceiling := int(insightText["maxLength"].(float64))
	if ceiling != fileAnalysisInsightMaxChars || ceiling*fileAnalysisInsightFieldCount != project.MaxEngineeringInsightRunes {
		t.Fatalf("insight schema ceiling = %d; aggregate = %d", ceiling, project.MaxEngineeringInsightRunes)
	}
	properties := defs["insight"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"mechanism", "why_it_matters_here", "tradeoff_or_failure_mode", "transferable_lesson"} {
		if reference := properties[field].(map[string]any)["$ref"]; reference != "#/$defs/insightText" {
			t.Fatalf("%s schema reference = %q", field, reference)
		}
	}
}
