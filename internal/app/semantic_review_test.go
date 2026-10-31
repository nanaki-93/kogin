package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/santhosh-tekuri/jsonschema/v6"
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

func TestSemanticStageCachesStructuredOneFileSummary(t *testing.T) {
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var gotSchema, wantSchema any
		if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema == nil || !request.ResponseFormat.JSONSchema.Strict || request.ResponseFormat.JSONSchema.Name != fileAnalysisResponseSchemaName || json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &gotSchema) != nil || json.Unmarshal([]byte(fileAnalysisResponseSchemaDocument), &wantSchema) != nil || !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Fatalf("file analysis response format = %+v", request.ResponseFormat)
		}
		prompt = request.Messages[0].Content
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "fixture-model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Runs the selected command.","responsibilities":["dispatches work"],"dependencies":["fmt"],"side_effects":["writes stdout"],"risks":[{"category":"bugs","severity":"High","title":"Missing input validation","summary":"No input validation."}],"suggestions":[{"title":"Validate input","summary":"Reject blank names.","action":"fix"}]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	stage, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || stage.Semantic == nil {
		t.Fatalf("analysis = %+v, %v", stage, err)
	}
	result := stage.Semantic
	if result.Status != project.AnalysisStatusFresh || result.Purpose == "" || result.Model != "fixture-model" || len(result.Risks) != 1 || result.Risks[0].Severity != "high" || result.Risks[0].Title != "Missing input validation" {
		t.Fatalf("analysis = %+v", result)
	}
	if strings.Contains(prompt, "helper secret") || !strings.Contains(prompt, "func Run") || !strings.Contains(prompt, "TARGET_SOURCE (the only source content supplied)") || !strings.Contains(prompt, "target_path copied exactly") || !strings.Contains(prompt, "Ground selected-file behavior and engineering insights in TARGET_SOURCE") || !strings.Contains(prompt, "Default to tests that characterize current behavior") {
		t.Fatalf("semantic prompt was not one-file scoped: %s", prompt)
	}
	if _, err := os.Stat(filepath.Join(root, ".mini-orca", "file-analysis")); err != nil {
		t.Fatalf("analysis cache not written: %v", err)
	}
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if file := findAppIndexFile(index, "main.go"); file == nil || file.AnalysisStatus != project.AnalysisStatusFresh {
		t.Fatalf("index analysis status = %+v, want fresh", file)
	}
	cached, err := semanticStageFor(t, service, "main.go", false)
	if second := cached.Semantic; err != nil || !cached.Progress.Cached || second == nil || second.Status != project.AnalysisStatusFresh || len(second.Risks) != 1 || second.Risks[0].Title != "Missing input validation" {
		t.Fatalf("cached analysis = %+v, %v", cached, err)
	}
}

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

func TestSemanticStageRefreshClassifiesLegacyCacheWithoutPassiveModelCalls(t *testing.T) {
	var calls atomic.Int32
	reply := strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"security","severity":"low","summary":"A visible trust-boundary concern."}]`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	prepared, err := service.prepareFileAnalysis("main.go")
	if err != nil {
		t.Fatal(err)
	}
	legacy := newFileAnalysis(prepared, semanticAnalysisResponse{Purpose: "Previous explanation", Risks: []project.Finding{{Severity: "low", Summary: "Historical concern."}}})
	legacy.PromptVersion = "file-analysis-v13"
	if err := prepared.cache.Store(legacy); err != nil {
		t.Fatal(err)
	}
	for read := 0; read < 2; read++ {
		cached, err := service.CachedFileAnalysis("main.go")
		if err != nil || cached.Status != project.AnalysisStatusStale || cached.Purpose != legacy.Purpose || len(cached.Risks) != 1 || cached.Risks[0].Category.Valid() || calls.Load() != 0 {
			t.Fatalf("legacy read = %+v, %v; calls=%d", cached, err, calls.Load())
		}
	}
	stage, err := semanticStageFor(t, service, "main.go", true)
	if fresh := stage.Semantic; err != nil || fresh == nil || fresh.Status != project.AnalysisStatusFresh || fresh.PromptVersion != "file-analysis-v18" || len(fresh.Risks) != 1 || fresh.Risks[0].Category != project.FindingCategorySecurity || calls.Load() != 1 {
		t.Fatalf("explicit refresh = %+v, %v; calls=%d", stage, err, calls.Load())
	}
	if _, err := service.CachedFileAnalysis("main.go"); err != nil || calls.Load() != 1 {
		t.Fatal("reading categorized cache made another model call")
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

func TestSemanticStageDoesNotRetryUnsupportedStructuredFormat(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.bug.effective.MaxRetries = 3
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSemantic)
	request.RemainingAttempts = 4
	result, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageFailed || requests != 1 {
		t.Fatalf("unsupported structured format result=%+v err=%v requests=%d", result, err, requests)
	}
}

func TestParseSemanticAnalysisRejectsInvalidJSONEscape(t *testing.T) {
	output := `{"purpose":"Summary.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[{"title":"Suggestion","summary":"Summary","action":"bad\q"}]}`
	if _, err := parseSemanticReviewResponse(output, project.IndexFile{Path: "main.go"}, "package main\n"); err == nil {
		t.Fatal("invalid JSON escape was accepted")
	}
}

func TestCachedFileAnalysisMarksV11PromptResultsStale(t *testing.T) {
	service, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	prepared, err := service.prepareFileAnalysis("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := EngineeringInsightPromptVersion(); got != "file-analysis-v18" {
		t.Fatalf("file analysis prompt version = %q, want file-analysis-v18", got)
	}
	legacy := project.FileAnalysis{
		SchemaVersion: "1", ProjectID: prepared.input.ProjectID, ProjectRevision: prepared.input.ProjectRevision,
		Path: prepared.input.Path, ContentHash: prepared.input.ContentHash, Language: prepared.input.Language,
		Status: project.AnalysisStatusFresh, Model: prepared.input.Model, ConfiguredModel: prepared.input.Model,
		Profile: prepared.input.Profile, Scope: prepared.input.Scope, ProviderOrigin: prepared.input.ProviderOrigin,
		ReasoningEffort: prepared.input.ReasoningEffort, PromptVersion: "file-analysis-v11", ContextPolicyVersion: prepared.input.ContextPolicyVersion,
	}
	if err := prepared.cache.Store(legacy); err != nil {
		t.Fatal(err)
	}
	cached, err := service.CachedFileAnalysis("main.go")
	if err != nil || cached.Status != project.AnalysisStatusStale {
		t.Fatalf("v11 cache = %+v, %v; want stale", cached, err)
	}
}

func TestSemanticStageRequiresRemoteConfirmationBeforeSendingPrompt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validSemanticAnalysis}}}})
	}))
	defer server.Close()

	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.bug.effective.RemoteProvider = true
	if _, err := semanticStageFor(t, service, "main.go", false); err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("declined remote confirmation error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("declined confirmation sent %d provider requests", requests)
	}
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSemantic)
	request.ConfirmRemoteProvider = true
	if _, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); err != nil {
		t.Fatalf("confirmed analysis error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("confirmed request count = %d, want 1", requests)
	}
}

func TestSemanticStageValidatesAndCachesOneExactBugTaskWithoutAnotherModelCall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"purpose":"Explains the file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"high","summary":"Run ignores an error.","anchor":"func Run() { fmt.Println(\"run\") }","task_spec":{"schema_version":"2","target_path":"main.go","acceptance_criteria":["Return the formatting error to the caller."],"non_goals":["Do not edit helper files."],"test_candidate":{"path":"main_test.go","name":"TestRunReturnsError","content":"package main\n\nimport \"testing\"\n\nfunc TestRunReturnsError(t *testing.T) {}\n"}}}],"suggestions":[]}`}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)

	stage, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || stage.Semantic == nil || len(stage.Semantic.Risks) != 1 {
		t.Fatalf("analysis = %+v, %v", stage, err)
	}
	task := stage.Semantic.Risks[0].TaskSpec
	if task == nil || task.TargetPath != "main.go" || task.TestCandidate == nil || task.TestCandidate.Path != "main_test.go" {
		t.Fatalf("validated task = %+v", task)
	}
	if risk := stage.Semantic.Risks[0]; risk.StartLine != 5 || risk.EndLine != 5 {
		t.Fatalf("risk lines = %d-%d", risk.StartLine, risk.EndLine)
	}
	content, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil || string(content) != "package main\n\nimport \"fmt\"\n\nfunc Run() { fmt.Println(\"run\") }\n" {
		t.Fatalf("analysis changed imported source: %q, %v", content, err)
	}
	cached, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || cached.Semantic == nil || cached.Semantic.Risks[0].TaskSpec == nil || calls != 1 {
		t.Fatalf("cached task = %+v, %v; calls = %d", cached, err, calls)
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

func TestSemanticStageHonorsTimeout(t *testing.T) {
	handlerCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		handlerCanceled <- struct{}{}
	}))
	defer server.Close()
	service, _ := newSemanticAnalysisService(t, server.URL, 1)
	result, err := semanticStageFor(t, service, "main.go", false)
	if err != nil || result.Progress.Status != AnalysisStageFailed || result.Semantic != nil {
		t.Fatalf("analysis = %+v, %v; want a failed stage after the deadline", result, err)
	}
	waitForTestSignal(t, handlerCanceled, "analysis provider cancellation")
}

const validSemanticAnalysis = `{"purpose":"Explains the file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForTestError(t *testing.T, done <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func findAppIndexFile(index *project.ProjectIndex, path string) *project.IndexFile {
	for i := range index.Files {
		if index.Files[i].Path == path {
			return &index.Files[i]
		}
	}
	return nil
}

func newSemanticAnalysisService(t *testing.T, baseURL string, analysisSeconds int) (*Service, string) {
	return newSemanticAnalysisServiceFixture(t, baseURL, analysisSeconds, false)
}

func newSemanticAnalysisServiceWithHelper(t *testing.T, baseURL string, analysisSeconds int) (*Service, string) {
	return newSemanticAnalysisServiceFixture(t, baseURL, analysisSeconds, true)
}

func newSemanticAnalysisServiceFixture(t *testing.T, baseURL string, analysisSeconds int, includeHelper bool) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc Run() { fmt.Println(\"run\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if includeHelper {
		if err := os.WriteFile(filepath.Join(root, "helper.go"), []byte("package main\n\nfunc Helper() { println(\"helper secret\") }\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root, Summary: "A compact fixture project."}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(baseURL)
	cfg.ModelScopes.Bug.Model = "analysis-model"
	cfg.Timeouts = config.TimeoutConfig{AnalysisSeconds: analysisSeconds}
	cfg.Retry = config.RetryConfig{MaxRetries: 1, BackoffBase: 1, BackoffMax: 1}
	service, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	return service, root
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
