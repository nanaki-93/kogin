package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

var validPerformanceLineReview = strings.Replace(validPerformanceReview, `,"symbol":"Run"`, "", 1)

func TestPerformanceRequestsStructuredOutputWithExactSourceLines(t *testing.T) {
	for _, path := range []string{"main.go", "notes.md", "empty.go"} {
		t.Run(path, func(t *testing.T) {
			var received llm.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				output := "```json\n{\"findings\":[]}\n```"
				if received.ResponseFormat != nil {
					output = `{"findings":[]}`
					if path == "main.go" {
						output = validPerformanceLineReview
					}
				}
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
			}))
			defer server.Close()
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			source := "\n\nproject notes\n"
			if path == "empty.go" {
				source = ""
			}
			if path != "main.go" {
				if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			request := analysisStageRequestFor(t, s, path, AnalysisStagePerformance)
			result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
			want := AnalysisStageCompletedEmpty
			if path == "main.go" {
				want = AnalysisStageCompleted
			}
			if err != nil || result.Progress.Status != want || result.Performance == nil || result.Progress.Attempts != 1 {
				t.Fatalf("stage=%+v err=%v", result, err)
			}
			if path == "main.go" && (result.Performance.Findings[0].StartLine != 5 || result.Performance.Findings[0].EndLine != 5) {
				t.Fatal("missing source line anchor")
			}
			format := received.ResponseFormat
			if format == nil || format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict {
				t.Fatalf("format=%+v", format)
			}
			schema := compileReviewSchema(t, format.JSONSchema.Schema)
			assertReviewSchemaAccepts(t, schema, `{"findings":[]}`, true)
			assertReviewSchemaAccepts(t, schema, validPerformanceLineReview, path == "main.go")
			if path == "notes.md" && !strings.HasSuffix(received.Messages[0].Content, "SOURCE:\n```\n"+source+"\n```") {
				t.Fatal("prompt changed source line numbering")
			}
		})
	}
}

func TestPerformanceSchemaConstrainsFindingsAndIndexedAnchors(t *testing.T) {
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	snapshot, err := s.preparePerformanceReview("main.go")
	if err != nil {
		t.Fatal(err)
	}
	response := performanceReviewResponseSchema(snapshot)
	schema := compileReviewSchema(t, response.Schema)
	assertReviewSchemaAccepts(t, schema, validPerformanceLineReview, true)
	assertReviewSchemaAccepts(t, schema, `{"findings":[]}`, true)
	for _, test := range []struct{ name, from, to string }{
		{"model symbol", `"title":`, `"symbol":"Run","title":`},
		{"zero line", `"start_line":5`, `"start_line":0`},
		{"past file", `"end_line":5`, `"end_line":999`},
		{"missing explanation", "Measure before changing the formatting path.", ""},
		{"unknown field", `"title":`, `"private-field":true,"title":`},
		{"unsupported category", `"category":"cpu"`, `"category":"network"`},
		{"long prose", "Measure before changing the formatting path.", strings.Repeat("x", 1001)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertReviewSchemaAccepts(t, schema, strings.Replace(validPerformanceLineReview, test.from, test.to, 1), false)
		})
	}
	insight := `{"mechanism":"Repeated calls do work.","why_it_matters_here":"Calls may repeat.","tradeoff_or_failure_mode":"Caching retains data.","transferable_lesson":"Measure repeated calls."}`
	for _, value := range []string{insight, "null"} {
		assertReviewSchemaAccepts(t, schema, strings.Replace(validPerformanceLineReview, `"title":`, `"engineering_insight":`+value+`,"title":`, 1), true)
	}
	var report map[string][]json.RawMessage
	if err := json.Unmarshal([]byte(validPerformanceLineReview), &report); err != nil {
		t.Fatal(err)
	}
	assertReviewSchemaAccepts(t, schema, `{"findings":[`+strings.TrimSuffix(strings.Repeat(string(report["findings"][0])+",", 6), ",")+`]}`, false)
}

func TestPerformanceFailuresRetainSafeSpecificReasonsAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		name, output string
		failure      project.PerformanceReviewFailure
	}{
		{"fenced empty", "```json\n{\"findings\":[]}\n```", project.PerformanceInvalidJSON},
		{"fenced finding", "```json\n" + validPerformanceReview + "\n```", project.PerformanceInvalidJSON},
		{"unescaped quotes", strings.Replace(validPerformanceReview, "Avoid repeated formatting", `Format "quoted" data`, 1), project.PerformanceInvalidJSON},
		{"zero start line", strings.Replace(validPerformanceReview, `"start_line":5`, `"start_line":0`, 1), project.PerformanceInvalidAnchors},
		{"inverted range", strings.Replace(validPerformanceReview, `"start_line":5,"end_line":5`, `"start_line":5,"end_line":4`, 1), project.PerformanceInvalidAnchors},
		{"past file end", strings.Replace(validPerformanceReview, `"end_line":5`, `"end_line":99`, 1), project.PerformanceInvalidAnchors},
		{"missing explanation", strings.Replace(validPerformanceReview, "Measure before changing the formatting path.", "", 1), project.PerformanceInvalidFields},
		{"invalid value", strings.Replace(validPerformanceReview, `"category":"cpu"`, `"category":"network"`, 1), project.PerformanceInvalidValues},
		{"missing findings", `{}`, project.PerformanceInvalidShape},
		{"untrusted field", `{"findings":[],"private-provider-text":"sensitive"}`, project.PerformanceInvalidJSON},
		{"oversized", strings.Repeat(" ", 64*1024+1), project.PerformanceOutputTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
				if stage == AnalysisStagePerformance {
					return test.output
				}
				return emptyAnalysisReply(stage)
			})
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			run := completedAnalysisRun(t, s)
			progress := run.Files[0].Stages[1]
			if progress.Status != AnalysisStageFailed || progress.Reason != test.failure.Error() || progress.FindingCount != nil {
				t.Fatalf("failure=%+v", progress)
			}
			assertNoPerformanceReport(t, root)
			s.analysisRun = &analysisRunController{}
			selection := readSelectionFor(t, s)
			if selection.Files[0].Stages[1].Reason != test.failure.Error() {
				t.Fatalf("lost explanation: %+v", selection)
			}
			retry := retryPreviewFor(t, s, nil)
			if retry.ExpectedModelRequests != 1 || calls.Load() != 3 {
				t.Fatalf("retry=%+v calls=%d", retry, calls.Load())
			}
		})
	}
}

func TestPerformanceStopsWhenProviderRejectsStructuredRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "private provider details", http.StatusBadRequest)
	}))
	defer server.Close()
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	result, err := reviewPerformanceStageForTest(t, s, context.Background(), nil)
	if err != nil || result.Progress.Status != AnalysisStageFailed || calls != 1 || result.Progress.Reason != "The provider rejected the Performance response format." {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
	assertNoPerformanceReport(t, root)
}
