package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

type projectAnalysisFixtureClient struct {
	output         string
	calls          int
	beforeResponse func()
}

func analyzeProjectForTest(ctx context.Context, analyzer *Analyzer, root string) (*Analysis, error) {
	snapshot, err := ScanProject(ctx, root)
	if err != nil {
		return nil, err
	}
	return analyzer.AnalyzeSnapshot(ctx, snapshot)
}

func (c *projectAnalysisFixtureClient) ChatWithJSONSchema(context.Context, []llm.ChatMessage, llm.JSONSchema) (*llm.ChatResponse, error) {
	c.calls++
	if c.beforeResponse != nil {
		c.beforeResponse()
	}
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: c.output}}}}, nil
}

func TestParseProjectAnalysisResponseRejectsMalformedUnknownAndOversizedOutput(t *testing.T) {
	valid := `{"purpose":"Explains the project.","architecture":"One Go daemon.","components":["daemon"],"walkthroughs":[{"title":"Serve","summary":"Serves.","steps":[{"text":"Starts.","path":"main.go","symbol":"main"}]}],"risks":[{"severity":"HIGH","summary":"Add a regression test."}]}`
	if report, err := parseProjectAnalysisResponse(valid); err != nil || report.Purpose != "Explains the project." || len(report.Risks) != 1 || report.Risks[0].Severity != "high" || len(report.Walkthroughs) != 1 {
		t.Fatalf("parsed report = %+v, %v", report, err)
	}
	for _, output := range []string{
		"not JSON",
		`{"purpose":"x","architecture":"y","components":[],"walkthroughs":[],"risks":[],"unexpected":true}`,
		`{"purpose":"x","architecture":"y","components":[],"flows":[],"risks":[]}`,
		`{"purpose":"","architecture":"y","components":[],"walkthroughs":[],"risks":[]}`,
		strings.Repeat("x", maxProjectAnalysisBytes+1),
	} {
		if _, err := parseProjectAnalysisResponse(output); err == nil {
			t.Fatalf("expected invalid project analysis output to fail: %q", output[:min(len(output), 32)])
		}
	}
}

func TestParseProjectAnalysisResponseOmitsInvalidOptionalInsights(t *testing.T) {
	base := `{"purpose":"Explains the project.","architecture":"One Go daemon.","components":[],"walkthroughs":[],"risks":[{"severity":"low","summary":"Add a regression test.","engineering_insight":`
	for _, insight := range []string{`"not an object"`, `{"mechanism":"m"}`, `{"mechanism":"m","why_it_matters_here":"w","unknown":true}`, `null`} {
		output := base + insight + `}],"engineering_insight":` + insight + `}`
		parsed, err := parseProjectAnalysisResponse(output)
		if err != nil || parsed.EngineeringInsight != nil || len(parsed.Risks) != 1 || parsed.Risks[0].EngineeringInsight != nil {
			t.Fatalf("optional insight %q parsed = %+v, %v", insight, parsed, err)
		}
	}
	parsed, err := parseProjectAnalysisResponse(`{"purpose":"Explains the project.","architecture":"One Go daemon.","components":[],"walkthroughs":[],"risks":[{"severity":"low","summary":"Add a regression test."}]}`)
	if err != nil || parsed.EngineeringInsight != nil || parsed.Risks[0].EngineeringInsight != nil {
		t.Fatalf("absent optional insight parsed = %+v, %v", parsed, err)
	}
}

func TestProjectAnalysisPromptRequestsUsefulInsightsOrOmission(t *testing.T) {
	prompt := projectAnalysisMessages("context")[0].Content
	for _, required := range []string{"mechanism explains the concrete mechanism", "why_it_matters_here names the exact local evidence", "tradeoff_or_failure_mode names a real trade-off or failure condition", "transferable_lesson gives a reusable lesson with a concrete verification idea", "Omit trivial or generic lessons."} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("project prompt is missing %q: %s", required, prompt)
		}
	}
}

func TestProjectAnalysisReportPersistsAndInvalidatesChangedInputs(t *testing.T) {
	root := t.TempDir()
	report := newProjectAnalysisReportWithProvenance("project", "revision-one", "model-a", "analysis", "analysis", "", "")
	report.Purpose = "Explains the project."
	report.Architecture = "One daemon."
	if err := StoreProjectAnalysisReport(root, report); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProjectAnalysisReport(root, ProjectAnalysisInput{ProjectID: "project", ProjectRevision: "revision-one", Model: "model-a", Profile: "analysis", Scope: "analysis", PromptVersion: projectAnalysisPromptVersion})
	if err != nil || loaded == nil || loaded.Status != ProjectAnalysisStatusFresh {
		t.Fatalf("fresh stored report = %+v, %v", loaded, err)
	}
	stale, err := LoadProjectAnalysisReport(root, ProjectAnalysisInput{ProjectID: "project", ProjectRevision: "revision-two", Model: "model-a", Profile: "analysis", Scope: "analysis", PromptVersion: projectAnalysisPromptVersion})
	if err != nil || stale == nil || stale.Status != ProjectAnalysisStatusStale {
		t.Fatalf("changed revision report = %+v, %v", stale, err)
	}
	if _, err := os.Stat(filepath.Join(root, projectAnalysisReportPath)); err != nil {
		t.Fatalf("structured report was not persisted: %v", err)
	}
}

func TestProjectAnalysisReportInvalidatesChangedScopeProvider(t *testing.T) {
	root := t.TempDir()
	report := newProjectAnalysisReportWithProvenance("project", "revision", "model", "analyze", "analyze", "https://provider-one.example", "high")
	report.Purpose = "Purpose"
	report.Architecture = "Architecture"
	if err := StoreProjectAnalysisReport(root, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProjectAnalysisReport(root, ProjectAnalysisInput{ProjectID: "project", ProjectRevision: "revision", Model: "model", Profile: "analyze", Scope: "analyze", ProviderOrigin: "https://provider-two.example", ReasoningEffort: "high", PromptVersion: projectAnalysisPromptVersion})
	if err != nil || loaded == nil || loaded.Status != ProjectAnalysisStatusStale {
		t.Fatalf("provider-changed report = %+v, %v", loaded, err)
	}
}

func TestProjectAnalysisReportInvalidatesChangedReasoningEffort(t *testing.T) {
	root := t.TempDir()
	report := newProjectAnalysisReportWithProvenance("project", "revision", "model", "analyze", "analyze", "https://provider.example", "high")
	report.Purpose = "Purpose"
	report.Architecture = "Architecture"
	if err := StoreProjectAnalysisReport(root, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProjectAnalysisReport(root, ProjectAnalysisInput{ProjectID: "project", ProjectRevision: "revision", Model: "model", Profile: "analyze", Scope: "analyze", ProviderOrigin: "https://provider.example", ReasoningEffort: "low", PromptVersion: projectAnalysisPromptVersion})
	if err != nil || loaded == nil || loaded.Status != ProjectAnalysisStatusStale {
		t.Fatalf("reasoning-effort-changed report = %+v, %v", loaded, err)
	}
}

func TestAnalyzerFailureKeepsInventoryAndWritesCanonicalReport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	analysis, err := analyzeProjectForTest(context.Background(), NewAnalyzerWithProvenance(&projectAnalysisFixtureClient{output: "not JSON"}, "fixture-model", "analysis", "", ""), root)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.AIStatus != ProjectAnalysisStatusFailed || analysis.FileCount != 1 || analysis.SourceFileCount != 1 || analysis.Report.Failure == "" {
		t.Fatalf("failed analysis = %+v", analysis)
	}
	if _, err := os.Stat(filepath.Join(root, ".mini-orca", "analysis.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy markdown report was generated: %v", err)
	}
	stored, err := LoadProjectAnalysisReport(root, ProjectAnalysisInput{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Model: "fixture-model", Profile: "analysis", PromptVersion: projectAnalysisPromptVersion})
	if err != nil || stored == nil || stored.Status != ProjectAnalysisStatusFailed {
		t.Fatalf("stored failed report = %+v, %v", stored, err)
	}
}

func TestAnalyzerRestoreLoadsStoredReportWithoutContactingModel(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	client := &projectAnalysisFixtureClient{output: `{"purpose":"Restored project.","architecture":"One package.","components":[],"walkthroughs":[],"risks":[]}`}
	analyzer := NewAnalyzerWithProvenance(client, "fixture-model", "analysis", "", "")
	imported, err := analyzeProjectForTest(context.Background(), analyzer, root)
	if err != nil {
		t.Fatal(err)
	}
	client.calls = 0

	restored, err := analyzer.Restore(root)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("restore contacted model %d times", client.calls)
	}
	if restored.ProjectID != imported.ProjectID || restored.Summary != imported.Summary || restored.AIStatus != ProjectAnalysisStatusFresh {
		t.Fatalf("restored analysis = %+v", restored)
	}
}

func TestAnalyzerDoesNotPublishSuccessfulReportAfterCancellation(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc main() {}\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &projectAnalysisFixtureClient{
		output:         `{"purpose":"Late report.","architecture":"One package.","components":[],"walkthroughs":[],"risks":[]}`,
		beforeResponse: cancel,
	}
	analyzer := NewAnalyzerWithProvenance(client, "fixture-model", "analyze", "", "")
	analysis, err := analyzeProjectForTest(ctx, analyzer, root)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.AIStatus != ProjectAnalysisStatusFailed || analysis.Report.Purpose != "" || !strings.Contains(analysis.Report.Failure, "canceled") || analysis.FileCount != 1 {
		t.Fatalf("canceled analysis = %+v", analysis)
	}
	restored, err := analyzer.Restore(root)
	if err != nil || restored.AIStatus != ProjectAnalysisStatusFailed || restored.Report.Purpose != "" || client.calls != 1 {
		t.Fatalf("restored analysis = %+v, calls = %d, error = %v", restored, client.calls, err)
	}
}

func TestProjectAnalysisReportRequiresGeneratedTime(t *testing.T) {
	report := newProjectAnalysisReportWithProvenance("project", "revision", "model", "analysis", "analysis", "", "")
	report.Purpose = "Purpose"
	report.Architecture = "Architecture"
	report.GeneratedAt = time.Time{}
	if validStoredProjectAnalysisReport(report) {
		t.Fatal("report without generated time is valid")
	}
}

func TestWalkthroughsSurvivePersistenceAndOldReportsRemainReadable(t *testing.T) {
	root := t.TempDir()
	walkthroughs := []ProjectWalkthrough{{Title: "Serve a request", Summary: "A request reaches storage.", Steps: []ProjectWalkthroughStep{{Text: "The API decodes it.", Path: "internal/api/handler.go", Symbol: "Handler.Serve", Line: 12}}}}
	report := newProjectAnalysisReportWithProvenance("project", "revision", "model", "analyze", "analyze", "", "")
	report.Purpose, report.Architecture, report.Components, report.Walkthroughs = "Service", "internal/api calls internal/store.", []string{"internal/api (API): Handles requests."}, walkthroughs
	if err := StoreProjectAnalysisReport(root, report); err != nil {
		t.Fatal(err)
	}
	input := ProjectAnalysisInput{ProjectID: "project", ProjectRevision: "revision", Model: "model", Profile: "analyze", Scope: "analyze", PromptVersion: projectAnalysisPromptVersion}
	stored, err := LoadProjectAnalysisReport(root, input)
	if err != nil || stored == nil || stored.Status != ProjectAnalysisStatusFresh || !reflect.DeepEqual(stored.Walkthroughs, walkthroughs) || stored.Components[0] != report.Components[0] {
		t.Fatalf("stored report = %+v, %v", stored, err)
	}
	// A report from the diagram prompt reads as stale, its diagrams and the
	// fields that prompt had are left out.
	legacy := `{"schema_version":"1","project_id":"project","project_revision":"revision","purpose":"Service","architecture":"flowchart TD\n A --> B","components":[],"entry_points":["main"],"flows":["sequenceDiagram\n A->>B: x"],"risks":[],"next_steps":["Add tests"],"status":"fresh","model":"model","profile":"analyze","scope":"analyze","prompt_version":"project-analysis-v6","generated_at":"2026-10-30T10:00:00Z"}`
	path, err := storage.MetadataPath(root, projectAnalysisReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, err = LoadProjectAnalysisReport(root, input)
	if err != nil || stored == nil || stored.Status != ProjectAnalysisStatusStale || stored.Purpose != "Service" || len(stored.Walkthroughs) != 0 {
		t.Fatalf("legacy report = %+v, %v", stored, err)
	}
}
