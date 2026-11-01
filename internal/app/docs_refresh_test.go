package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// docsModel answers the docs refresh's requests: a summary per component,
// named after the component, and a project overview. Other requests fail.
type docsModel struct {
	mu        sync.Mutex
	summaries map[string]int
	overviews int
	other     int
	// overviewFails makes the overview requests fail.
	overviewFails bool
}

var componentLine = regexp.MustCompile(`(?m)^Component: (\S+)`)

func newDocsModelServer(t *testing.T) (*httptest.Server, *docsModel) {
	t.Helper()
	model := &docsModel{summaries: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 || request.ResponseFormat == nil || request.ResponseFormat.JSONSchema == nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		model.mu.Lock()
		defer model.mu.Unlock()
		var content string
		switch request.ResponseFormat.JSONSchema.Name {
		case "module_summary":
			match := componentLine.FindStringSubmatch(request.Messages[1].Content)
			if match == nil {
				http.Error(w, "no component", http.StatusBadRequest)
				return
			}
			model.summaries[match[1]]++
			// A model that ignores the schema's limits is cut to them.
			content = `{"summary":"Summary of ` + match[1] + `.","responsibilities":["Owns ` + match[1] + `","b","c","d","e"]}`
		case "project_analysis_response":
			model.overviews++
			if model.overviewFails {
				http.Error(w, "model unavailable", http.StatusInternalServerError)
				return
			}
			context := request.Messages[1].Content
			if !strings.Contains(context, "Component summaries") || !strings.Contains(context, "Files and what each does") {
				http.Error(w, "the overview did not read the component and file summaries", http.StatusBadRequest)
				return
			}
			content = `{"purpose":"Keeps items.","architecture":"store keeps model items.","components":["store (Store): Saves items."],"walkthroughs":[{"title":"Keep an item","summary":"An item lands in the store.","steps":[{"text":"The model defines the item.","path":"model/model.go","symbol":"Item"},{"text":"The store holds it.","path":"store/store.go","symbol":"Items"}]}],"risks":[],"engineering_insight":null}`
		default:
			model.other++
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "docs-model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: content}, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	return server, model
}

// docsRefreshReply answers a docs refresh request with a valid reply.
func docsRefreshReply(request llm.ChatRequest) (string, bool) {
	if request.ResponseFormat == nil || request.ResponseFormat.JSONSchema == nil {
		return "", false
	}
	switch request.ResponseFormat.JSONSchema.Name {
	case "module_summary":
		return `{"summary":"Runs the fixture.","responsibilities":[]}`, true
	case "project_analysis_response":
		return `{"purpose":"Runs the fixture.","architecture":"One package.","components":[],"walkthroughs":[],"risks":[],"engineering_insight":null}`, true
	}
	return "", false
}

func (m *docsModel) counts() (map[string]int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	summaries := map[string]int{}
	for path, count := range m.summaries {
		summaries[path] = count
	}
	return summaries, m.overviews
}

// writeDocsFixture adds a store package that imports a model package.
func writeDocsFixture(t *testing.T, root string) {
	t.Helper()
	for name, content := range map[string]string{
		"go.mod":         "module example.test/fixture\n\ngo 1.22\n",
		"store/store.go": "package store\n\nimport \"example.test/fixture/model\"\n\nvar Items []model.Item\n",
		"model/model.go": "package model\n\ntype Item struct{}\n",
	} {
		writeProjectFile(t, root, name, content)
	}
}

func writeProjectFile(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// storeFileSummaries records a fresh summary for each named file at its
// indexed content.
func storeFileSummaries(t *testing.T, s *Service, root string, paths ...string) {
	t.Helper()
	index, err := s.manager.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	cache, err := project.NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		for _, file := range index.Files {
			if file.Path != path {
				continue
			}
			if err := cache.Store(project.FileAnalysis{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Path: path, ContentHash: file.ContentHash, Language: file.Language,
				Purpose: "Holds " + path + " at " + file.ContentHash[:16] + ".", Status: project.AnalysisStatusFresh, PromptVersion: "fixture", ContextPolicyVersion: "fixture"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// docsRequest asks for a refresh of the active project at its current revision.
func docsRequest(t *testing.T, s *Service, confirm bool) DocsRefreshRequest {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return DocsRefreshRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, ConfirmRemoteProvider: confirm}
}

func docsComponent(t *testing.T, s *Service, path string) project.ProjectDocsComponent {
	t.Helper()
	overview, err := s.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range overview.Docs.Components {
		if component.Path == path {
			return component
		}
	}
	t.Fatalf("no component %s in %+v", path, overview.Docs.Components)
	return project.ProjectDocsComponent{}
}

// refreshDocsAndWait starts a refresh from the Docs page and returns how it
// ended.
func refreshDocsAndWait(t *testing.T, s *Service, request DocsRefreshRequest) DocsRefresh {
	t.Helper()
	running, err := s.StartDocsRefresh(context.Background(), request)
	if err != nil || running.Status != "running" {
		t.Fatalf("start = %+v, %v", running, err)
	}
	s.docsJob.mu.Lock()
	done := s.docsJob.done
	s.docsJob.mu.Unlock()
	waitForTestSignal(t, done, "docs refresh")
	latest := s.latestDocsRefresh(request.ProjectID)
	if latest == nil {
		t.Fatal("the finished refresh is not readable")
	}
	return *latest
}

func TestDocsRefreshRollsFileSummariesUpAndRewritesOnlyWhatChanged(t *testing.T) {
	server, model := newDocsModelServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeDocsFixture(t, root)
	storeFileSummaries(t, s, root, "store/store.go", "model/model.go")
	refresh := refreshDocsAndWait(t, s, docsRequest(t, s, false))
	if refresh.Status != "completed" || refresh.Components != 2 || refresh.Summarized != 2 || refresh.Kept != 0 || refresh.Overview != "written" {
		t.Fatalf("refresh = %+v", refresh)
	}
	if component := docsComponent(t, s, "model"); component.SummarySource != "module" || component.Summary != "Summary of model." || len(component.Responsibilities) != maxResponsibilities {
		t.Fatalf("model component = %+v", component)
	}
	overview, err := s.ProjectOverview()
	if err != nil || overview.Docs.Status != project.ProjectAnalysisStatusFresh || overview.Analysis.Purpose != "Keeps items." || overview.DocsRefresh == nil || overview.DocsRefresh.Summarized != 2 {
		t.Fatalf("overview after refresh: %+v, %+v, %v", overview.Docs, overview.DocsRefresh, err)
	}
	walkthroughs := overview.Docs.Walkthroughs
	if len(walkthroughs) != 1 || len(walkthroughs[0].Steps) != 2 || walkthroughs[0].Steps[0].Line != 3 || walkthroughs[0].Steps[1].Symbol != "Items" {
		t.Fatalf("walkthroughs = %+v", walkthroughs)
	}

	// Nothing changed: a refresh from the Docs page keeps the summaries and
	// writes the overview again; the end of a run keeps both.
	if again := refreshDocsAndWait(t, s, docsRequest(t, s, false)); again.Summarized != 0 || again.Kept != 2 || again.Overview != "written" {
		t.Fatalf("unchanged refresh = %+v", again)
	}
	runtime := s.conf().runtimes.analyze
	if after, err := s.refreshDocs(context.Background(), runtime, false); err != nil || after.Summarized != 0 || after.Kept != 2 || after.Overview != "kept" {
		t.Fatalf("unchanged run refresh = %+v, %v", after, err)
	}

	// One file changed and was summarized again: only its component is.
	writeProjectFile(t, root, "store/store.go", "package store\n\nimport \"example.test/fixture/model\"\n\nvar Items, Archive []model.Item\n")
	storeFileSummaries(t, s, root, "store/store.go")
	changed, err := s.refreshDocs(context.Background(), runtime, false)
	if err != nil || changed.Summarized != 1 || changed.Kept != 1 || changed.Overview != "written" {
		t.Fatalf("changed refresh = %+v, %v", changed, err)
	}
	summaries, overviews := model.counts()
	if summaries["store"] != 2 || summaries["model"] != 1 || overviews != 3 || model.other != 0 {
		t.Fatalf("model calls: summaries %v, overviews %d, other %d", summaries, overviews, model.other)
	}
}

func TestAFailedOverviewRewriteKeepsTheSavedOverview(t *testing.T) {
	server, model := newDocsModelServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeDocsFixture(t, root)
	storeFileSummaries(t, s, root, "store/store.go", "model/model.go")
	if refresh := refreshDocsAndWait(t, s, docsRequest(t, s, false)); refresh.Overview != "written" {
		t.Fatalf("first refresh = %+v", refresh)
	}
	model.mu.Lock()
	model.overviewFails = true
	model.mu.Unlock()
	if refresh := refreshDocsAndWait(t, s, docsRequest(t, s, false)); refresh.Status != "failed" || refresh.Overview != "failed" || refresh.Reason == "" {
		t.Fatalf("failed refresh = %+v", refresh)
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := project.LoadProjectAnalysisReport(root, project.ProjectAnalysisInput{ProjectID: analysis.ProjectID})
	if err != nil || saved == nil || saved.Purpose != "Keeps items." || saved.Failure != "" || len(saved.Walkthroughs) != 1 {
		t.Fatalf("saved overview after a failed rewrite = %+v, %v", saved, err)
	}
	if overview, err := s.ProjectOverview(); err != nil || overview.Docs.Overview.Purpose != "Keeps items." {
		t.Fatalf("docs after a failed rewrite = %+v, %v", overview, err)
	}
}

func TestDocsComponentListsItsFilesWithTheirSummaryLine(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	writeDocsFixture(t, root)
	writeProjectFile(t, root, "store/store_test.go", "package store\n")
	storeFileSummaries(t, s, root, "store/store.go")
	files, err := s.DocsComponentFiles("store")
	if err != nil || files.Path != "store" || len(files.Files) != 2 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if first := files.Files[0]; first.Path != "store/store.go" || first.Language != "Go" || first.Lines != 5 || !strings.HasPrefix(first.Summary, "Holds store/store.go at ") {
		t.Fatalf("store.go = %+v", first)
	}
	if second := files.Files[1]; second.Path != "store/store_test.go" || second.Summary != "" {
		t.Fatalf("a file without a summary got one: %+v", second)
	}
	if _, err := s.DocsComponentFiles("nowhere"); !errors.Is(err, ErrUnknownDocsComponent) {
		t.Fatalf("unknown component error = %v", err)
	}
}

func TestDocsRefreshKeepsWrittenSummariesWhenTheModelFails(t *testing.T) {
	var calls sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		match := componentLine.FindStringSubmatch(request.Messages[len(request.Messages)-1].Content)
		if match == nil || match[1] == "store" {
			http.Error(w, "model unavailable", http.StatusInternalServerError)
			return
		}
		calls.Store(match[1], true)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: "docs-model", Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: `{"summary":"Defines items.","responsibilities":[]}`}, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeDocsFixture(t, root)
	storeFileSummaries(t, s, root, "store/store.go", "model/model.go")
	refresh, err := s.refreshDocs(context.Background(), s.conf().runtimes.analyze, false)
	if err == nil || refresh.Status != "failed" || refresh.Reason == "" || refresh.Overview != "skipped" {
		t.Fatalf("refresh = %+v, %v", refresh, err)
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := project.LoadModuleSummaries(root, analysis.ProjectID)
	if err != nil || stored.Entries["model"].Summary != "Defines items." || len(stored.Entries) != 1 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
}

func TestDocsRefreshRequiresTheCurrentProjectConsentAndOneAtATime(t *testing.T) {
	server, model := newDocsModelServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeDocsFixture(t, root)
	storeFileSummaries(t, s, root, "model/model.go")
	stale := docsRequest(t, s, false)
	stale.ProjectRevision = "older"
	if _, err := s.StartDocsRefresh(context.Background(), stale); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("an older revision was refreshed: %v", err)
	}
	s.docsJob.mu.Lock()
	s.docsJob.cancel = func() {}
	s.docsJob.mu.Unlock()
	if _, err := s.StartDocsRefresh(context.Background(), docsRequest(t, s, false)); !errors.Is(err, ErrDocsRefreshBusy) {
		t.Fatalf("a second refresh started: %v", err)
	}
	s.docsJob.mu.Lock()
	s.docsJob.cancel = nil
	s.docsJob.mu.Unlock()
	// A run's refresh holds the docs: the page's refresh ends skipped.
	s.docsRefreshMu.Lock()
	skipped := refreshDocsAndWait(t, s, docsRequest(t, s, false))
	s.docsRefreshMu.Unlock()
	if skipped.Status != "skipped" || skipped.Reason != docsRefreshBusyReason {
		t.Fatalf("refresh during a run's = %+v", skipped)
	}
	// A remote analyze model needs the request's consent.
	s.conf().runtimes.analyze.effective.RemoteProvider = true
	if _, err := s.StartDocsRefresh(context.Background(), docsRequest(t, s, false)); err == nil || !strings.Contains(err.Error(), "requires explicit confirmation") {
		t.Fatalf("a remote model ran without consent: %v", err)
	}
	if summaries, overviews := model.counts(); len(summaries) != 0 || overviews != 0 {
		t.Fatalf("refused refreshes called the model: %v %d", summaries, overviews)
	}
	if refresh := refreshDocsAndWait(t, s, docsRequest(t, s, true)); refresh.Status != "completed" || refresh.Summarized != 1 {
		t.Fatalf("a confirmed refresh = %+v", refresh)
	}
}

func TestDocsRefreshStopsWithTheProjectAndKeepsWhatItWrote(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeDocsFixture(t, root)
	storeFileSummaries(t, s, root, "model/model.go")
	request := docsRequest(t, s, false)
	if _, err := s.StartDocsRefresh(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "the summary request")
	s.invalidateJobsForProjectChange(stopReasonShutdown)
	if err := s.waitDocsRefreshDone(t.Context()); err != nil {
		t.Fatal(err)
	}
	if latest := s.latestDocsRefresh(request.ProjectID); latest == nil || latest.Status != "failed" || latest.Reason != docsRefreshStoppedReason {
		t.Fatalf("stopped refresh = %+v", latest)
	}
}

func TestAnalysisRunEndsByRefreshingTheDocsItPlanned(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	analyze := preview.Providers[1]
	if preview.Docs == nil || preview.Docs.ProviderID != analyze.ID || preview.Docs.Components != 1 || preview.Docs.MaxModelRequests != 2*(analyze.Model.MaxRetries+1) {
		t.Fatalf("docs plan = %+v, analyze provider %+v", preview.Docs, analyze)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if run.Docs == nil || run.Docs.Status != "completed" || run.Docs.Summarized != 1 || run.Docs.Overview != "written" || calls.Load() != 3 {
		t.Fatalf("docs = %+v, stage calls %d", run.Docs, calls.Load())
	}
	if component := docsComponent(t, s, "."); component.SummarySource != "module" || component.Summary != "Runs the fixture." {
		t.Fatalf("component = %+v", component)
	}
	// The refresh is kept with the run's saved progress.
	stored, err := s.currentTestAnalysisRun(context.Background())
	if err != nil || stored.Docs == nil || stored.Docs.Summarized != 1 {
		t.Fatalf("stored docs = %+v, %v", stored, err)
	}
}

func TestCodingAgentsReadWhereTheTaskFilesSitInTheDocs(t *testing.T) {
	requests := make(chan llm.ChatRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		content := `{"explanation":"Archive items.","changes":[{"path":"store/store.go","content":"package store\n\nvar Archive []int\n"}]}`
		if match := componentLine.FindStringSubmatch(request.Messages[len(request.Messages)-1].Content); match != nil {
			content = `{"summary":"Summary of ` + match[1] + `.","responsibilities":[]}`
		} else if reply, ok := docsRefreshReply(request); ok {
			content = reply
		} else {
			requests <- request
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: content}}}})
	}))
	t.Cleanup(server.Close)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if brief := s.docsBrief([]string{"main.go"}); brief != "" {
		t.Fatalf("a brief without docs: %q", brief)
	}
	writeDocsFixture(t, root)
	writeProjectFile(t, root, "cmd/app/main.go", "package main\n\nimport \"example.test/fixture/store\"\n\nfunc main() { _ = store.Items }\n")
	storeFileSummaries(t, s, root, "store/store.go", "model/model.go", "cmd/app/main.go")
	if refresh := refreshDocsAndWait(t, s, docsRequest(t, s, false)); refresh.Summarized != 3 {
		t.Fatalf("refresh = %+v", refresh)
	}
	brief := s.docsBrief([]string{"store/store.go"})
	for _, want := range []string{"- store (holds the task's files): Summary of store. Imports: model. Used by: cmd/app.", "- model (imported by store): Summary of model.", "- cmd/app (uses store): Summary of cmd/app."} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	// A one-shot change request carries it after the task's files.
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.OpenChangeSession(context.Background(), ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Archive items", Paths: []string{"store/store.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Keep archived items apart."}); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if input := request.Messages[len(request.Messages)-1].Content; !strings.Contains(input, "Where these files sit") || !strings.Contains(input, "Summary of store.") {
		t.Fatalf("the change request lacks the docs:\n%s", input)
	}
}

func TestToolAgentsReadTheDocsAfterTheProjectGuidance(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "AGENTS.md", "Keep functions small.\n")
	session := &ChangeSession{Kind: "feature", Title: "Archive items", Targets: []ChangeTarget{{Path: "store/store.go"}}}
	prompt := toolTaskPrompt(root, session, toolCoder, "Keep archived items apart.", "\nWhere these files sit (docs):\n- store (holds the task's files): Saves items.\n")
	guidance, docs := strings.Index(prompt, "Keep functions small."), strings.Index(prompt, "Where these files sit")
	if guidance < 0 || docs < guidance {
		t.Fatalf("prompt order:\n%s", prompt)
	}
}
