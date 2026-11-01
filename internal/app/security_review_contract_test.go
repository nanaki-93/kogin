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

	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSecurityAIRequestsStructuredOutputForTextAndGoFiles(t *testing.T) {
	for _, path := range []string{".gitignore", "README.md", "main.go"} {
		t.Run(path, func(t *testing.T) {
			var received llm.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				// A prompt alone allowed the live provider to wrap empty results in Markdown.
				output := "```json\n{\"findings\":[]}\n```"
				if received.ResponseFormat != nil {
					output = `{"findings":[]}`
					if path == "main.go" {
						output = validSecurityReview
					}
				}
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
			}))
			defer server.Close()
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			if path != "main.go" {
				if err := os.WriteFile(filepath.Join(root, path), []byte("project notes\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			request := analysisStageRequestFor(t, s, path, AnalysisStageSecurityAI)
			result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
			want := AnalysisStageCompletedEmpty
			if path == "main.go" {
				want = AnalysisStageCompleted
			}
			if err != nil || result.Progress.Status != want || result.Progress.Attempts != 1 || result.Security == nil {
				t.Fatalf("security stage = %+v, %v", result, err)
			}
			format := received.ResponseFormat
			if format == nil || format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict {
				t.Fatalf("security response format = %+v", format)
			}
			schema := compileReviewSchema(t, format.JSONSchema.Schema)
			output := `{"findings":[]}`
			if path == "main.go" {
				output = validSecurityReview
			}
			assertReviewSchemaAccepts(t, schema, output, true)
		})
	}
}

func TestSecurityReviewSchemaConstrainsFindingFieldsAndFileAnchors(t *testing.T) {
	s, _ := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	snapshot, err := s.prepareSecurityReview(securityReviewRequestFor(t, s, ""))
	if err != nil {
		t.Fatal(err)
	}
	response, err := securityReviewResponseSchema(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	schema := compileReviewSchema(t, response.Schema)
	assertReviewSchemaAccepts(t, schema, validSecurityReview, true)
	assertReviewSchemaAccepts(t, schema, `{"findings":[]}`, true)
	for _, test := range []struct{ name, from, to string }{
		{"uppercase rule", "go.input.validation", "VULN_SCAN_GENERIC"},
		{"uppercase category", "input-validation", "Input Validation"},
		{"legacy symbol field", `"end_line":5`, `"end_line":5,"symbol":"Run"`},
		{"wrong file", `"path":"main.go"`, `"path":"other.go"`},
		{"zero line", `"start_line":5`, `"start_line":0`},
		{"past file", `"end_line":5`, `"end_line":9999`},
		{"wrong evidence", "model_suspicion", "verified"},
		{"empty prose", "Input reaches an operation", ""},
		{"long prose", "Input reaches an operation", strings.Repeat("x", 513)},
		{"unknown field", `"rule":`, `"unknown":true,"rule":`},
		{"invalid cwe", `"rule":`, `"cwe":"CWE-0","rule":`},
		{"unsafe reference", `"rule":`, `"reference":"http://example.test","rule":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertReviewSchemaAccepts(t, schema, strings.Replace(validSecurityReview, test.from, test.to, 1), false)
		})
	}
	for _, output := range []string{`{}`, `{"findings":null}`, `{"findings":{}}`, `{"findings":[],"summary":"none"}`} {
		assertReviewSchemaAccepts(t, schema, output, false)
	}
	var report map[string][]json.RawMessage
	if err := json.Unmarshal([]byte(validSecurityReview), &report); err != nil {
		t.Fatal(err)
	}
	assertReviewSchemaAccepts(t, schema, `{"findings":[`+strings.TrimSuffix(strings.Repeat(string(report["findings"][0])+",", 6), ",")+`]}`, false)
}

func TestSecurityReviewSchemaPinsFocusAndSupportsFilesWithoutSymbols(t *testing.T) {
	for _, test := range []struct {
		name    string
		file    project.IndexFile
		allowed bool
	}{
		{"within file", project.IndexFile{Path: "main.go", LineCount: 8}, true},
		{"past file line count", project.IndexFile{Path: "main.go", LineCount: 4}, false},
		{"empty file", project.IndexFile{Path: "main.go"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := securityReviewResponseSchema(securityReviewSnapshot{file: test.file})
			if err != nil {
				t.Fatal(err)
			}
			schema := compileReviewSchema(t, response.Schema)
			assertReviewSchemaAccepts(t, schema, validSecurityReview, test.allowed)
			assertReviewSchemaAccepts(t, schema, `{"findings":[]}`, true)
			withSymbol := strings.Replace(validSecurityReview, `"end_line":5`, `"end_line":5,"symbol":"Run"`, 1)
			assertReviewSchemaAccepts(t, schema, withSymbol, false)
		})
	}
}

func TestSecurityAIRejectsInvalidResponsesWithoutPublishing(t *testing.T) {
	for _, output := range []string{
		"```json\n{\"findings\":[]}\n```",
		strings.Replace(validSecurityReview, "go.input.validation", "VULN_SCAN_GENERIC", 1),
		strings.Replace(validSecurityReview, `"end_line":5`, `"end_line":999`, 1),
		strings.Replace(validSecurityReview, "The operation uses input without an observed validation boundary.", "package main", 1),
	} {
		server, _ := analysisResponseServer(t, func(AnalysisStage) string { return output })
		s, root := newSemanticAnalysisService(t, server.URL, 0)
		request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityAI)
		result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
		if err != nil || result.Progress.Status != AnalysisStageFailed || result.Progress.FindingCount != nil || result.Progress.Attempts != 1 {
			t.Fatalf("invalid response stage = %+v, %v", result, err)
		}
		assertNoSecurityReport(t, root)
	}
}

func TestSecurityAIRequiresFreshReportAfterPromptChange(t *testing.T) {
	server, calls := analysisResponseServer(t, analysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityAI)
	authority := analysisStageAuthorityFor(t, request)
	first, err := s.analyzeFileStage(context.Background(), request, authority)
	if err != nil || first.Security == nil {
		t.Fatalf("initial report = %+v, %v", first, err)
	}
	cache, err := project.NewSecurityReportCache(root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.manager.IndexedFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	legacy := *first.Security
	legacy.PromptVersion = "security-file-v1"
	if err := cache.Store(legacy, *file); err != nil {
		t.Fatal(err)
	}
	result, err := s.analyzeFileStage(context.Background(), request, authority)
	if err != nil || result.Progress.Cached || result.Security == nil || result.Security.PromptVersion != project.SecurityPromptVersion || calls.Load() != 2 {
		t.Fatalf("refreshed report = %+v, calls = %d, %v", result, calls.Load(), err)
	}
}

func TestSecurityAIStopsWhenProviderRejectsStructuredRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unsupported response format", http.StatusBadRequest)
	}))
	defer server.Close()
	s, root := newSemanticAnalysisService(t, server.URL, 3)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityAI)
	result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageFailed || result.Progress.FindingCount != nil || result.Progress.Attempts != 1 || calls != 1 {
		t.Fatalf("rejected schema stage = %+v, calls = %d, %v", result, calls, err)
	}
	assertNoSecurityReport(t, root)
}

func compileReviewSchema(t *testing.T, raw json.RawMessage) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("security.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("security.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func assertReviewSchemaAccepts(t *testing.T, schema *jsonschema.Schema, output string, want bool) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); (err == nil) != want {
		t.Fatalf("schema validation = %v, want accepted = %t", err, want)
	}
}
