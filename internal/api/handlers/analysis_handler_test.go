package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/api"
	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func newAnalysisHandlerFixture(t *testing.T) (*AnalysisHandler, *ProjectHandler, *project.Analysis, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		reply := `{"findings":[]}`
		schema := ""
		if request.ResponseFormat != nil && request.ResponseFormat.JSONSchema != nil {
			schema = request.ResponseFormat.JSONSchema.Name
		}
		// The docs refresh that ends a run is answered but not counted.
		switch {
		case schema == "module_summary":
			reply = `{"summary":"Runs the fixture.","responsibilities":[]}`
		case schema == "project_analysis_response":
			reply = `{"purpose":"Runs the fixture.","architecture":"One package.","components":[],"entry_points":[],"flows":[],"risks":[],"next_steps":[],"engineering_insight":null}`
		case schema == "feature_suggestions":
			calls.Add(1)
			reply = `{"suggestions":[]}`
		case strings.HasPrefix(request.Messages[0].Content, "You summarize"):
			calls.Add(1)
			reply = `{"purpose":"Explains this file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]}`
		default:
			calls.Add(1)
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "resolved-model-version", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	writeProjectHandlerFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := app.New(scopedHandlerConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	analysis, _ := manager.Analysis()
	return NewAnalysisHandler(service), NewProjectHandler(manager, service), analysis, &calls
}

func analysisHandlerRequest(t *testing.T, handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if value, ok := body.(string); ok {
		data = []byte(value)
	} else if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestAnalysisHandlerStaleOnlyAdmission(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	request := app.AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: app.AnalysisRunScopeProject, StaleOnly: true, StalePath: "main.go", Limits: app.AnalysisRunLimits{BatchFiles: 10, BudgetSeconds: 30, MaxAttemptsPerStage: 1}}
	w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	var preview app.AnalysisRunPreview
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil || !preview.StaleOnly || preview.StalePath != "main.go" || scopedAnalysisFiles(preview) != 0 || preview.ExpectedModelRequests != 0 {
		t.Fatalf("stale-only preview = %d %s", w.Code, w.Body)
	}
	start := app.AnalysisRunStartRequest{Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits, StaleOnly: true, StalePath: "main.go"}
	for _, endpoint := range []string{"preview", "start"} {
		for _, path := range []string{"../outside.go", "/absolute.go", "./main.go", "main.go\n", "main.go"} {
			var payload map[string]any
			value, handler := any(request), h.Preview
			if endpoint == "start" {
				value, handler = start, h.Start
			}
			data, _ := json.Marshal(value)
			_ = json.Unmarshal(data, &payload)
			payload["stale_path"] = path
			payload["stale_only"] = path != "main.go"
			if w := analysisHandlerRequest(t, handler, "POST", "/analysis/"+endpoint, payload); w.Code != http.StatusBadRequest {
				t.Fatalf("%s with invalid target %q = %d %s", endpoint, path, w.Code, w.Body)
			}
		}
	}
	for _, flag := range []string{"refresh", "clear_results", "retry_stale_failed", "recover_incomplete", "include_features"} {
		for _, endpoint := range []string{"preview", "start"} {
			var payload map[string]any
			value, handler := any(request), h.Preview
			if endpoint == "start" {
				value, handler = start, h.Start
			}
			data, _ := json.Marshal(value)
			_ = json.Unmarshal(data, &payload)
			payload[flag] = true
			if w := analysisHandlerRequest(t, handler, "POST", "/analysis/"+endpoint, payload); w.Code != http.StatusBadRequest {
				t.Fatalf("%s with %s = %d %s", endpoint, flag, w.Code, w.Body)
			}
		}
	}
	if w := analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start); w.Code != http.StatusAccepted {
		t.Fatalf("stale-only start = %d %s", w.Code, w.Body)
	}
	run := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunUnavailable)
	if run.Status != app.AnalysisRunUnavailable || !run.Plan.StaleOnly || run.Plan.StalePath != "main.go" || calls.Load() != 0 {
		t.Fatalf("empty stale-only run did not settle without requests: status=%s calls=%d", run.Status, calls.Load())
	}
}

func TestAnalysisHandlerPausedRunRejectsReplacementWithRecoveryMessage(t *testing.T) {
	h, projectHandler, analysis, calls := newAnalysisHandlerFixture(t)
	writeProjectHandlerFixture(t, analysis.Path, "helper.go", "package main\nfunc Help() {}\n")
	if _, err := projectHandler.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	analysis, _ = projectHandler.manager.Analysis()
	request := app.AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: app.AnalysisRunScopeProject, Limits: app.AnalysisRunLimits{BatchFiles: 1, BudgetSeconds: 30, MaxAttemptsPerStage: 2}}
	preview := func() app.AnalysisRunPreview {
		t.Helper()
		w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
		var result app.AnalysisRunPreview
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("preview=%d %s", w.Code, w.Body)
		}
		return result
	}
	plan := preview()
	start := app.AnalysisRunStartRequest{Identity: plan.Identity, PreviewID: plan.PreviewID, Limits: plan.Limits, Confirmations: app.AnalysisRunConfirmations{SecurityReview: true}}
	if w := analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start); w.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", w.Code, w.Body)
	}
	paused := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunPaused)
	if paused.Status != app.AnalysisRunPaused {
		t.Fatalf("status=%s, want paused", paused.Status)
	}
	before := calls.Load()
	request.Limits.BudgetSeconds = 1800
	plan = preview()
	start.PreviewID, start.Identity, start.Limits = plan.PreviewID, plan.Identity, plan.Limits
	w := analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	var failure api.AppError
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &failure) != nil {
		t.Fatalf("replacement=%d %s", w.Code, w.Body)
	}
	if !strings.Contains(failure.UserMessage, "Continue or cancel") || strings.Contains(failure.UserMessage, "project changed") {
		t.Errorf("misleading paused-run recovery: %s", failure.UserMessage)
	}
	retained := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunPaused)
	if retained.Identity != paused.Identity || calls.Load() != before {
		t.Fatalf("replacement changed the saved run or dispatched: calls=%d want=%d", calls.Load(), before)
	}
	request.ResumeRun, request.Limits = &paused.Identity, paused.Plan.Limits
	plan = preview()
	control := app.AnalysisRunControlRequest{Identity: paused.Identity, Action: app.AnalysisRunResume, PreviewID: plan.PreviewID, Confirmations: &start.Confirmations}
	if w := analysisHandlerRequest(t, h.Control, "POST", "/analysis/run/control", control); w.Code != http.StatusOK {
		t.Fatalf("resume=%d %s", w.Code, w.Body)
	}
	completed := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunCompletedEmpty)
	if completed.Status != app.AnalysisRunCompletedEmpty || completed.Identity.ID != paused.Identity.ID || calls.Load() <= before {
		t.Fatalf("continuation status=%s calls=%d", completed.Status, calls.Load())
	}
}

func TestAnalysisHandlerModelsRequireMatchingAdmission(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	models := &app.AnalysisModels{Code: "code", Performance: "bug", Security: "code"}
	request := app.AnalysisPreviewRequest{Models: &app.AnalysisModels{Code: "code", Performance: "bug", Security: "code", Features: "analyze"},
		ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Scope: "project", Limits: app.AnalysisRunLimits{BatchFiles: 100, BudgetSeconds: 30, MaxAttemptsPerStage: 2}}
	w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	if w.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("retired features model preview=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	assertStructuredError(t, w)
	request.Models = models
	w = analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	if w.Code != http.StatusOK || calls.Load() != 0 {
		t.Fatalf("model preview=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	var preview app.AnalysisRunPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Models == nil || *preview.Models != *models {
		t.Fatalf("model plan=%+v %v", preview, err)
	}
	start := app.AnalysisRunStartRequest{Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits,
		Models:        &app.AnalysisModels{Code: "code", Performance: "bug", Security: "analyze"},
		Confirmations: app.AnalysisRunConfirmations{SecurityReview: true}}
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatalf("retargeted model=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	assertStructuredError(t, w)
	start.Models.Security = "unknown"
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("invalid model=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	assertStructuredError(t, w)
	start.Models = models
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != http.StatusAccepted {
		t.Fatalf("model admission=%d %s", w.Code, w.Body)
	}
	run := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunCompletedEmpty)
	if run.Plan.Models == nil || *run.Plan.Models != *models || calls.Load() != 3 {
		t.Fatalf("model run=%+v calls=%d", run.Plan.Models, calls.Load())
	}
}

func TestAnalysisHandlerRetrySelectionRequiresMatchingAdmission(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	request := app.AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: "project", RetryStaleFailed: true, Limits: app.AnalysisRunLimits{BatchFiles: 100, BudgetSeconds: 30, MaxAttemptsPerStage: 2}}
	w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatalf("retry preview=%d %s", w.Code, w.Body)
	}
	var preview app.AnalysisRunPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.RetryStaleFailed || scopedAnalysisFiles(preview) != 0 || preview.ExpectedModelRequests != 0 {
		t.Fatalf("missing file included: %+v", preview)
	}
	start := app.AnalysisRunStartRequest{Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits}
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 409 {
		t.Fatalf("broader start=%d %s", w.Code, w.Body)
	}
	assertStructuredError(t, w)
	request.Refresh = true
	w = analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	if w.Code != 400 {
		t.Fatalf("refresh retry=%d %s", w.Code, w.Body)
	}
	assertStructuredError(t, w)
	start.RetryStaleFailed, start.Refresh = true, true
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 400 {
		t.Fatalf("refresh start=%d %s", w.Code, w.Body)
	}
	assertStructuredError(t, w)
	start.Refresh = false
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 200 && w.Code != 202 {
		t.Fatalf("retry start=%d %s", w.Code, w.Body)
	}
	run := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunUnavailable)
	if !run.Plan.RetryStaleFailed || run.Status != app.AnalysisRunUnavailable || calls.Load() != 0 {
		t.Fatalf("empty run=%+v calls=%d", run, calls.Load())
	}
}

func TestAnalysisHandlerRecoveryRequiresExplicitCompatibleAdmission(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	selectionTarget := "/analysis/selection?" + url.Values{"project_id": {analysis.ProjectID}, "project_revision": {analysis.ProjectRevision}}.Encode()
	readRecovery := func() app.AnalysisRecoverySummary {
		t.Helper()
		w := analysisHandlerRequest(t, h.Selection, "GET", selectionTarget, nil)
		var selection app.AnalysisFileSelection
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &selection) != nil || !strings.Contains(w.Body.String(), `"recovery":{"state":`) {
			t.Fatalf("selection=%d %s", w.Code, w.Body)
		}
		return selection.Recovery
	}
	if recovery := readRecovery(); recovery.State != app.AnalysisRecoveryNotStarted || recovery.FileCount != 1 || recovery.StageCount != 4 {
		t.Fatalf("initial recovery=%+v", recovery)
	}
	limits := `"limits":{"batch_files":100,"budget_seconds":30,"max_attempts_per_stage":2}`
	identity := `"project_id":"` + analysis.ProjectID + `","project_revision":"` + analysis.ProjectRevision + `","scope":"project"`
	for _, option := range []string{`"refresh":true`, `"refresh":true,"clear_results":true`, `"refresh":false,"clear_results":true`, `"refresh":false,"retry_stale_failed":true`, `"refresh":false,"include_features":true`} {
		w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", `{`+identity+`,`+limits+`,"recover_incomplete":true,`+option+`}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("recovery with %s => %d %s", option, w.Code, w.Body)
		}
		assertStructuredError(t, w)
	}
	w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", `{`+identity+`,`+limits+`,"refresh":false,"recover_incomplete":true}`)
	if w.Code != http.StatusOK || calls.Load() != 0 {
		t.Fatalf("recovery preview=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	var preview app.AnalysisRunPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || !preview.RecoverIncomplete || preview.Features != nil || len(preview.Files) != 1 || preview.ExpectedModelRequests != 3 {
		t.Fatalf("recovery plan=%+v %v", preview, err)
	}
	start := app.AnalysisRunStartRequest{Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits, Confirmations: app.AnalysisRunConfirmations{SecurityReview: true}}
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != http.StatusConflict || calls.Load() != 0 {
		t.Fatalf("start without recovery=%d %s", w.Code, w.Body)
	}
	assertStructuredError(t, w)
	start.RecoverIncomplete = true
	for _, combine := range []func(*app.AnalysisRunStartRequest){
		func(r *app.AnalysisRunStartRequest) { r.Refresh = true },
		func(r *app.AnalysisRunStartRequest) { r.ClearResults = true },
		func(r *app.AnalysisRunStartRequest) { r.RetryStaleFailed = true },
	} {
		combined := start
		combine(&combined)
		w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", combined)
		if w.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("combined start=%d %s", w.Code, w.Body)
		}
		assertStructuredError(t, w)
	}
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != http.StatusAccepted {
		t.Fatalf("recovery start=%d %s", w.Code, w.Body)
	}
	run := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunCompletedEmpty)
	if !run.Plan.RecoverIncomplete || run.Features != nil || calls.Load() != 3 {
		t.Fatalf("recovery run=%+v calls=%d", run, calls.Load())
	}
	// The worker publishes the terminal status before it releases the run, and
	// recovery reports it as running until then.
	recovery := readRecovery()
	for deadline := time.Now().Add(10 * time.Second); recovery.State == app.AnalysisRecoveryRunning && time.Now().Before(deadline); recovery = readRecovery() {
		time.Sleep(10 * time.Millisecond)
	}
	if recovery.State != app.AnalysisRecoveryComplete || recovery.FileCount != 0 || recovery.StageCount != 0 {
		t.Fatalf("completed recovery=%+v", recovery)
	}
}

func TestAnalysisHandlerPreflightStartAndControls(t *testing.T) {
	h, projectHandler, analysis, calls := newAnalysisHandlerFixture(t)
	request := app.AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: "project", Limits: app.AnalysisRunLimits{BatchFiles: 100, BudgetSeconds: 30, MaxAttemptsPerStage: 2}}
	w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", request)
	if w.Code != 200 || calls.Load() != 0 {
		t.Fatalf("preview=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	var preview app.AnalysisRunPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	start := app.AnalysisRunStartRequest{Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits}
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 400 || calls.Load() != 0 {
		t.Fatalf("missing Security intent=%d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	start.Confirmations.SecurityReview = true
	start.Identity.QueueID = "old"
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 409 || calls.Load() != 0 {
		t.Fatalf("changed queue=%d %s", w.Code, w.Body)
	}
	start.Identity = preview.Identity
	w = analysisHandlerRequest(t, h.Start, "POST", "/analysis/run", start)
	if w.Code != 202 {
		t.Fatalf("start=%d %s", w.Code, w.Body)
	}
	run := waitHandlerAnalysis(t, h, analysis, app.AnalysisRunCompletedEmpty)
	if run.Status != app.AnalysisRunCompletedEmpty || calls.Load() != 3 {
		t.Fatalf("run=%+v calls=%d", run, calls.Load())
	}
	w = analysisHandlerRequest(t, projectHandler.Overview, "GET", "/overview?project_revision="+url.QueryEscape(analysis.ProjectRevision), nil)
	var overview app.ProjectOverview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 || overview.Coverage != (app.AnalysisCoverage{Total: 1, Fresh: 1}) {
		t.Fatalf("overview=%d %s error=%v", w.Code, w.Body, err)
	}
	identity := run.Identity
	identity.Generation = "previous"
	w = analysisHandlerRequest(t, h.Control, "POST", "/analysis/run/control", app.AnalysisRunControlRequest{Identity: identity, Action: app.AnalysisRunCancel})
	if w.Code != 409 || calls.Load() != 3 {
		t.Fatalf("late control=%d %s", w.Code, w.Body)
	}
	source, err := os.ReadFile(filepath.Join(analysis.Path, "main.go"))
	if err != nil || string(source) != "package main\nfunc Run() {}\n" {
		t.Fatalf("source changed: %s %v", source, err)
	}
}

func TestAnalysisHandlerRejectsMalformedBodiesAndAmbiguousGuards(t *testing.T) {
	h, _, analysis, calls := newAnalysisHandlerFixture(t)
	for _, body := range []string{`{}`, `{"scope":"file"}`, `{"unknown":"field"}`, `{} {}`, `{"limits":{"batch_files":501}}`, `{"scope":"project","path":"main.go"}`} {
		w := analysisHandlerRequest(t, h.Preview, "POST", "/analysis/preview", body)
		if w.Code != 400 {
			t.Fatalf("body %s => %d", body, w.Code)
		}
		assertStructuredError(t, w)
	}
	for _, query := range []string{"", "project_id=x", "project_id=x&project_id=y&project_revision=z", "project_id=x&project_revision=z&path=main.go", "project_id=%ZZ&project_revision=z"} {
		w := analysisHandlerRequest(t, h.Current, "GET", "/analysis/run?"+query, nil)
		if w.Code != 400 {
			t.Fatalf("query %s => %d", query, w.Code)
		}
	}
	q := url.Values{"project_id": {analysis.ProjectID}, "project_revision": {analysis.ProjectRevision}}
	if w := analysisHandlerRequest(t, h.Current, "GET", "/analysis/run?"+q.Encode(), nil); w.Code != 204 {
		t.Fatalf("absent=%d %s", w.Code, w.Body)
	}
	q.Set("project_id", "replacement")
	if w := analysisHandlerRequest(t, h.Current, "GET", "/analysis/run?"+q.Encode(), nil); w.Code != 409 {
		t.Fatalf("wrong project=%d %s", w.Code, w.Body)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request dispatched")
	}
}

func waitHandlerAnalysis(t *testing.T, h *AnalysisHandler, analysis *project.Analysis, status app.AnalysisRunStatus) app.AnalysisRun {
	t.Helper()
	query := url.Values{"project_id": {analysis.ProjectID}, "project_revision": {analysis.ProjectRevision}}
	var run app.AnalysisRun
	// A hang guard sized for the race detector, not a timing assertion.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := analysisHandlerRequest(t, h.Current, "GET", "/analysis/run?"+query.Encode(), nil)
		if w.Code != 200 {
			t.Fatalf("current=%d %s", w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.Status == status {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return run
}

// scopedAnalysisFiles counts the files a scoped preview selects; the others are retained.
func scopedAnalysisFiles(preview app.AnalysisRunPreview) int {
	selected := 0
	for _, file := range preview.Files {
		if !file.Retained {
			selected++
		}
	}
	return selected
}
