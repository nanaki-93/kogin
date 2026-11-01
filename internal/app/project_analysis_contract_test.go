package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const projectReportFixture = `{"purpose":"Runs a small daemon.","architecture":"main.go holds the whole daemon.","components":[". (Daemon): Handles requests."],"walkthroughs":[{"title":"Run the daemon","summary":"The program prints and exits.","steps":[{"text":"Run prints a line.","path":"./main.go","symbol":"Run"},{"text":"The model named a declaration the file lacks.","path":"main.go","symbol":"Serve"},{"text":"The model named a file the project lacks.","path":"server/http.go","symbol":"Listen"}]},{"title":"Invented","summary":"Only files the project lacks.","steps":[{"text":"Nothing here.","path":"../outside.go","symbol":""}]}],"risks":[{"severity":"low","summary":"Add request coverage.","engineering_insight":null}],"engineering_insight":null}`

func TestAnalyzeProjectRequestsSchemaAndChecksWalkthroughsAgainstTheFiles(t *testing.T) {
	var received llm.ChatRequest
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// An unconstrained reply can contain Markdown even when the prompt asks
		// for JSON. Only the structured request establishes the output contract.
		output := "```json\n" + projectReportFixture + "\n```"
		if received.ResponseFormat != nil && received.ResponseFormat.Type == "json_schema" {
			output = projectReportFixture
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "returned-model", Choices: []llm.ChatChoice{{FinishReason: "stop", Message: llm.ChatMessage{Role: "assistant", Content: output}}}})
	}))
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)

	analysis, err := service.ImportProject(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.AIStatus != project.ProjectAnalysisStatusFresh || analysis.Report.Model != "returned-model" || analysis.Report.Architecture != "main.go holds the whole daemon." {
		t.Fatalf("project report = %+v", analysis.Report)
	}
	// Steps keep only the project's files, and a symbol only where the file
	// declares it.
	want := []project.ProjectWalkthrough{{Title: "Run the daemon", Summary: "The program prints and exits.", Steps: []project.ProjectWalkthroughStep{
		{Text: "Run prints a line.", Path: "main.go", Symbol: "Run", Line: 5},
		{Text: "The model named a declaration the file lacks.", Path: "main.go"},
	}}}
	if !reflect.DeepEqual(analysis.Report.Walkthroughs, want) {
		t.Fatalf("walkthroughs = %+v", analysis.Report.Walkthroughs)
	}
	format := received.ResponseFormat
	if format == nil || format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict || format.JSONSchema.Name == "" {
		t.Fatalf("project response format = %+v", format)
	}
	schema := compileReviewSchema(t, format.JSONSchema.Schema)
	assertReviewSchemaAccepts(t, schema, projectReportFixture, true)
	insight := `{"mechanism":"The handler delegates to the daemon.","why_it_matters_here":"Requests enter through one module.","tradeoff_or_failure_mode":"A blocked daemon delays the response.","transferable_lesson":"Block the handler in a test and verify cancellation."}`
	assertReviewSchemaAccepts(t, schema, strings.ReplaceAll(projectReportFixture, `"engineering_insight":null`, `"engineering_insight":`+insight), true)
	// A reply longer than the prompt asks for still fits the schema, which CLI
	// runners enforce whole; the parser trims it instead.
	walkthrough := `{"title":"T","summary":"S","steps":[{"text":"x","path":"main.go","symbol":""}]},`
	long := strings.Replace(projectReportFixture, `"Runs a small daemon."`, `"`+strings.Repeat("x", 1500)+`"`, 1)
	long = strings.Replace(long, `"walkthroughs":[`, `"walkthroughs":[`+strings.Repeat(walkthrough, 8), 1)
	assertReviewSchemaAccepts(t, schema, long, true)
	for _, invalid := range []string{
		strings.Replace(projectReportFixture, `"components":[". (Daemon): Handles requests."]`, `"components":[{"name":"Daemon"}]`, 1),
		strings.Replace(projectReportFixture, `"severity":"low"`, `"severity":"critical"`, 1),
		strings.Replace(projectReportFixture, `"Runs a small daemon."`, `""`, 1),
		strings.Replace(projectReportFixture, `"Runs a small daemon."`, `"`+strings.Repeat("x", 2049)+`"`, 1),
		strings.Replace(projectReportFixture, `"risks":[`, `"risks":[`+strings.Repeat(`{"severity":"low","summary":"x","engineering_insight":null},`, 32), 1),
		strings.Replace(projectReportFixture, `"symbol":"Run"`, `"symbol":"Run","line":5`, 1),
		strings.Replace(projectReportFixture, `"engineering_insight":null`, `"engineering_insight":{"mechanism":"partial"}`, 1),
		strings.Replace(projectReportFixture, `"purpose":`, `"unexpected":true,"purpose":`, 1),
		strings.Replace(projectReportFixture, `"walkthroughs":`, `"flows":`, 1),
	} {
		assertReviewSchemaAccepts(t, schema, invalid, false)
	}
	restored, err := service.RestoreProject(root)
	if err != nil || restored.AIStatus != project.ProjectAnalysisStatusFresh || restored.Report.Architecture != analysis.Report.Architecture || !reflect.DeepEqual(restored.Report.Walkthroughs, analysis.Report.Walkthroughs) || calls != 1 {
		t.Fatalf("restored report = %+v, calls = %d, error = %v", restored, calls, err)
	}
}

func TestAnalyzeProjectKeepsInventoryAndExplainsUnusableReplies(t *testing.T) {
	for _, test := range []struct {
		name, output, finish, refusal, failure string
		status                                 int
	}{
		{name: "truncated JSON", output: `{"purpose":"partial`, finish: "length", failure: "output limit"},
		{name: "complete JSON at limit", output: projectReportFixture, finish: "length", failure: "output limit"},
		{name: "refusal", output: projectReportFixture, finish: "stop", refusal: "PRIVATE_PROVIDER_REASON", failure: "declined"},
		{name: "content filter", output: projectReportFixture, finish: "content_filter", failure: "declined"},
		{name: "tool call", output: projectReportFixture, finish: "tool_calls", failure: "did not finish"},
		{name: "malformed JSON", output: `{"PRIVATE_OUTPUT":true}`, finish: "stop", failure: "structured report"},
		{name: "rejected schema", status: http.StatusBadRequest, failure: "rejected the structured report request"},
		{name: "unprocessable schema", status: http.StatusUnprocessableEntity, failure: "rejected the structured report request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if test.status != 0 {
					http.Error(w, "PRIVATE_PROVIDER_ERROR", test.status)
					return
				}
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{FinishReason: test.finish, Message: llm.ChatMessage{Role: "assistant", Content: test.output, Refusal: test.refusal}}}})
			}))
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			analysis, err := service.ImportProject(context.Background(), root, true)
			if err != nil {
				t.Fatal(err)
			}
			if analysis.AIStatus != project.ProjectAnalysisStatusFailed || analysis.FileCount != 1 || analysis.Report.Purpose != "" || !strings.Contains(analysis.Report.Failure, test.failure) || strings.Contains(analysis.Report.Failure, "PRIVATE_") || calls != 1 {
				t.Fatalf("failed analysis = %+v, calls = %d", analysis, calls)
			}
			restored, err := service.RestoreProject(root)
			if err != nil || restored.AIStatus != project.ProjectAnalysisStatusFailed || restored.Report.Failure != analysis.Report.Failure || calls != 1 {
				t.Fatalf("restored failure = %+v, calls = %d, error = %v", restored, calls, err)
			}
		})
	}
}
