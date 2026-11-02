package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func changeIdentity(session *ChangeSession) ChangeIdentity {
	return ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}
}

func TestChangeHistoryLimitCountsOnlyConversationsAndRejectsCorruption(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	session := openChangeFixture(t, service, "main.go")
	initialID := session.ID
	for i := 0; i < 200; i++ {
		session.ID = fmt.Sprintf("change-%032x", i)
		if err := storeChangeSession(root, session); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the initial random entry; keep exactly 200 conversations plus
	// metadata owned by other workflows.
	if err := os.Remove(filepath.Join(root, ".kasuri/changes", initialID+".json")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"features.json", "last-mutation.json"} {
		if err := os.WriteFile(filepath.Join(root, ".kasuri/changes", name), []byte("other metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	history, err := service.ChangeHistory(context.Background())
	if err != nil || len(history) != 200 {
		t.Fatalf("metadata changed conversation limit: %d, %v", len(history), err)
	}
	if _, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Kind: "feature", Title: "Above limit", Paths: []string{"main.go"}}); err == nil {
		t.Fatal("conversation limit was not enforced")
	}
	session.Changes = []ChangeEdit{{Path: "main.go", Content: "package main\n", Hash: contentHash([]byte("package main\n"))}}
	session.Hash = changeProposalHash(session.Changes)
	session.Changes = append(session.Changes, session.Changes[0])
	if err := storeChangeSession(root, session); err == nil {
		t.Fatal("duplicate persisted changes accepted")
	}
}

func TestTaskFilesWithRetiredFieldsStillLoad(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	session := openChangeFixture(t, service, "main.go")
	path := filepath.Join(root, ".kasuri", "changes", session.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["scope_suggestions"] = json.RawMessage(`["outside_test.go"]`)
	fields["workflow"] = json.RawMessage(`{"probe_summary":"Fix tests.","harness_lessons":["Keep tests."]}`)
	older, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := dropRetiredChangeFields(older)
	if err != nil || strings.Contains(string(cleaned), "scope_suggestions") || strings.Contains(string(cleaned), "probe_summary") || strings.Contains(string(cleaned), "harness_lessons") {
		t.Fatalf("retired fields kept: %s, %v", cleaned, err)
	}
	delete(fields, "workflow")
	older, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, older, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadChangeSession(root, session.ID)
	if err != nil || loaded.ID != session.ID {
		t.Fatalf("a task saved with scope suggestions no longer loads: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"scope_suggestions":[],"scope_suggestions":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadChangeSession(root, session.ID); err == nil {
		t.Fatal("a duplicate retired key hid an ambiguous task file")
	}
}

func TestNewTaskAcceptsAtMostEightPathsLikeTheUI(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	var paths []string
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("file%d.go", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	request := ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Wide task", Paths: paths}
	if _, err := service.OpenChangeSession(context.Background(), request); err == nil || !strings.Contains(err.Error(), "one to 8 paths") {
		t.Fatalf("nine paths: %v", err)
	}
	request.Paths = paths[:8]
	if _, err := service.OpenChangeSession(context.Background(), request); err != nil {
		t.Fatalf("eight paths: %v", err)
	}
}

func openChangeFixture(t *testing.T, service *Service, paths ...string) *ChangeSession {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Implement a bounded task", Paths: paths, AcceptanceCriteria: []string{"Preserve existing behavior."}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func changeProvider(t *testing.T, response func() string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: response()}}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestChangeMessageUsesSelectedProfileAndItsConsent(t *testing.T) {
	var calls atomic.Int32
	server := changeProvider(t, func() string {
		calls.Add(1)
		return `{"explanation":"Document the behavior.","changes":[{"path":"README.md","content":"# Behavior\nReturns the result.\n"}]}`
	})
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	service.conf().runtimes.bug.effective.RemoteProvider = true
	session := openChangeFixture(t, service, "README.md")
	request := ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Document the behavior.", Profile: "unknown"}
	if _, err := service.SendChangeMessage(context.Background(), session.ID, request); err == nil || calls.Load() != 0 {
		t.Fatal("unknown profile dispatched")
	}
	request.Profile = "bug"
	if _, err := service.SendChangeMessage(context.Background(), session.ID, request); err == nil || calls.Load() != 0 {
		t.Fatal("selected remote profile dispatched without consent")
	}
	request.ConfirmRemoteProvider = true
	proposal, err := service.SendChangeMessage(context.Background(), session.ID, request)
	if err != nil || proposal.ContextManifest.Scope != "bug" || calls.Load() != 1 {
		t.Fatalf("selected profile ignored: %+v, %v", proposal, err)
	}
	service.discoverPiModels = func(context.Context, string) ([]llm.PiModel, error) { return []llm.PiModel{}, nil }
	service.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) { return []llm.AgyModel{}, nil }
	catalog, err := service.AvailableModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request = ChangeMessageRequest{ChangeIdentity: changeIdentity(proposal), Message: "Add a second line.", Profile: "configured:" + strings.Repeat("0", 64), ConfirmRemoteProvider: true}
	if _, err := service.SendChangeMessage(context.Background(), session.ID, request); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 1 {
		t.Fatalf("unavailable catalog profile dispatched: %v", err)
	}
	request.Profile = catalog.Defaults["bug"]
	proposal, err = service.SendChangeMessage(context.Background(), session.ID, request)
	if err != nil || proposal.ContextManifest.Scope != "code" || calls.Load() != 2 {
		t.Fatalf("catalog profile failed: %+v, %v", proposal, err)
	}
}

func TestChangeProposalOmitsUnchangedAndFormattingOnlyFiles(t *testing.T) {
	session := &ChangeSession{Targets: []ChangeTarget{
		{Path: "main.go", Exists: true, Content: "package main\n\nfunc First() int { return 1 }\n\nfunc Untouched() int { return 2 }\n\nfunc Last() int { return 3 }\n"},
		{Path: "helper.go", Exists: true, Content: "package main\nfunc Helper() {\n    println( 1 )\n}\n"},
	}}
	response := `{"explanation":"Fix two return values.","changes":[{"path":"main.go","content":"package main\n\nfunc First() int { return 4 }\n\nfunc Untouched() int { return 2 }\n\nfunc Last() int { return 5 }\n"},{"path":"helper.go","content":"package main\n\nfunc Helper() {\n\tprintln(1)\n}\n"}]}`
	_, edits, err := parseChangeResponse(t.TempDir(), response, session)
	if err != nil || len(edits) != 1 || edits[0].Path != "main.go" {
		t.Fatalf("formatting-only file entered proposal: %+v, %v", edits, err)
	}
	for _, line := range edits[0].Diff.Lines {
		if strings.Contains(line.Text, "Untouched") && line.Kind != "context" {
			t.Fatalf("untouched declaration displayed as an edit: %+v", line)
		}
	}
	if edit, err := makeChangeEdit(session, "helper.go", session.Targets[1].Content); err != nil || edit != nil {
		t.Fatal("unchanged unformatted source became a change")
	}
}

func TestChangeWhitespaceFilteringPreservesLiteralAndMarkdownChanges(t *testing.T) {
	for _, test := range []struct{ path, before, after string }{
		{"main.go", "package main\nconst text = \"two  spaces\"\n", "package main\nconst text = \"two spaces\"\n"},
		{"main.go", "package main\nconst text = `\n  indent\n`\n", "package main\nconst text = `\n indent\n`\n"},
		{"README.md", "line\n    code\n", "line\ncode\n"},
		{"README.md", "line  \nbreak\n", "line\nbreak\n"},
	} {
		session := &ChangeSession{Targets: []ChangeTarget{{Path: test.path, Exists: true, Content: test.before}}}
		edit, err := makeChangeEdit(session, test.path, test.after)
		if err != nil || edit == nil || !strings.Contains(edit.Content, strings.TrimPrefix(test.after, "package main\n")) {
			t.Fatalf("meaningful whitespace was dropped for %s: %+v, %v", test.path, edit, err)
		}
	}
}

func TestChangeGenerationHistoryAndRevision(t *testing.T) {
	var calls atomic.Int32
	server := changeProvider(t, func() string {
		calls.Add(1)
		return `{"explanation":"Add the helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int {return 1}\n"}]}`
	})
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Preserve cancellation."), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, service, "main.go", "new.go")
	first, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Add a helper."})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.Changes) != 1 || !strings.Contains(first.Changes[0].Content, "return 1") || len(first.ContextManifest.Included) != 3 {
		t.Fatalf("proposal: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("generation wrote source")
	}
	if _, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Stale revision."}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("accepted old revision: %v", err)
	}
	first.Checks = []CheckResult{{Name: "parse", Required: true, State: CheckPassed}}
	first.ReviewedHash = first.Hash
	if err := storeChangeSession(root, first); err != nil {
		t.Fatal(err)
	}
	read, err := service.ChangeSession(context.Background(), session.ID)
	if err != nil || len(read.Messages) != 2 || read.Freshness != "current" {
		t.Fatalf("read: %+v, %v", read, err)
	}
	history, err := service.ChangeHistory(context.Background())
	if err != nil || len(history) != 1 || calls.Load() != 1 {
		t.Fatalf("passive history: %+v, %v", history, err)
	}
	info, err := os.Stat(filepath.Join(root, ".kasuri/changes", session.ID+".json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("history permissions: %v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stale, err := service.ChangeSession(context.Background(), session.ID)
	if err != nil || stale.Freshness != "stale" || len(stale.Changes) != 1 {
		t.Fatalf("stale history lost proposal: %+v, %v", stale, err)
	}
}

func TestChangeRejectsMalformedAndRetargetedResponses(t *testing.T) {
	for _, output := range []string{`{}`, `{"explanation":"x","changes":[{"path":"other.go","content":"package main"}]}`, `{"explanation":"x","changes":[{"path":"new.go","content":"package main","extra":true}]}`, `{"explanation":"x","changes":[]} trailing`, `{"explanation":"x","changes":[{"path":"new.go","content":""}]}`} {
		t.Run(output, func(t *testing.T) {
			server := changeProvider(t, func() string { return output })
			service, _ := newSemanticAnalysisService(t, server.URL, 0)
			session := openChangeFixture(t, service, "new.go")
			if _, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Implement."}); err == nil {
				t.Fatal("accepted invalid model output")
			}
			stored, err := service.ChangeSession(context.Background(), session.ID)
			if err != nil || stored.Revision != 0 || len(stored.Messages) != 0 {
				t.Fatalf("published failed output: %+v, %v", stored, err)
			}
		})
	}
}

func TestChangeLateGenerationAndCancellationDoNotPublish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := changeProvider(t, func() string {
		close(entered)
		<-release
		return `{"explanation":"x","changes":[{"path":"new.go","content":"package main\n"}]}`
	})
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := openChangeFixture(t, service, "new.go")
	result := make(chan error, 1)
	go func() {
		_, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Implement."})
		result <- err
	}()
	<-entered
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Changed during generation."), 0644); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("late publication: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.OpenChangeSession(ctx, ChangeCreateRequest{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Kind: "feature", Title: "Canceled", Paths: []string{"other.go"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestChangeCorruptHistoryIsSetAside(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	proposal := openChangeFixture(t, service, "AGENTS.md")
	if err := os.WriteFile(filepath.Join(root, ".kasuri/changes", proposal.ID+".json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeSession(context.Background(), proposal.ID); err == nil || !strings.Contains(err.Error(), ".kasuri/changes/"+proposal.ID+".json is unreadable") {
		t.Fatalf("corrupt task error does not name its file: %v", err)
	}
	if _, err := listChangeSessions(root); err == nil {
		t.Fatal("a strict listing overlooked a corrupt task")
	}
	history, err := service.ChangeHistory(context.Background())
	if err != nil || len(history) != 0 {
		t.Fatalf("task list did not set the corrupt task aside: %+v %v", history, err)
	}
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 1 || report.Files[0].Kind != "task" || report.Files[0].File != ".kasuri/changes/"+proposal.ID+".json" {
		t.Fatalf("quarantine report: %+v %v", report, err)
	}
	if kept, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(report.Files[0].QuarantinedAs))); err != nil || string(kept) != "broken" {
		t.Fatal("set-aside task lost its bytes", err)
	}
	if _, err := service.ChangeSession(context.Background(), "../escape"); err == nil {
		t.Fatal("accepted history traversal")
	}
	if left, err := service.DeleteQuarantined(context.Background(), deleteRequest(t, service, report.Files[0].QuarantinedAs)); err != nil || len(left.Files) != 0 {
		t.Fatalf("delete the set-aside task: %+v %v", left, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(report.Files[0].QuarantinedAs))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-aside task kept: %v", err)
	}
}

func TestChangeScopeAndRemoteConsent(t *testing.T) {
	service, _ := newSemanticAnalysisService(t, "https://provider.invalid", 0)
	session := openChangeFixture(t, service, "new.go")
	if _, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), Message: "Implement."}); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("missing remote consent: %v", err)
	}
	for _, paths := range [][]string{{"main.go", "main.go"}, {"../escape.go"}, {"config.yaml"}, {".kasuri/state.md"}} {
		if _, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Kind: "feature", Title: "Invalid", Paths: paths}); err == nil {
			t.Fatalf("accepted scope: %v", paths)
		}
	}
}

func TestChangeUnlocksAllTextFileTypesAndHunkProposalUpdates(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	if err := os.WriteFile(filepath.Join(root, "style.css"), []byte(":root {\n  --bg: #000;\n  --fg: #fff;\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	index, err := service.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{
		ProjectID:       index.ProjectID,
		ProjectRevision: index.ProjectRevision,
		Kind:            "feature",
		Title:           "Direct edit style.css",
		Paths:           []string{"style.css"},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(session),
		Message:        "Edit style.css directly",
		Changes: []ChangeEditInput{{
			Path:    "style.css",
			Content: ":root {\n  --bg: #111;\n  --fg: #eee;\n}\n",
		}},
	})
	if err != nil || proposal.Revision != 1 || len(proposal.Changes) != 1 {
		t.Fatalf("direct non-Go/non-Markdown proposal failed: %+v, %v", proposal, err)
	}
	updated, err := service.SendChangeMessage(context.Background(), proposal.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(proposal),
		Message:        "Accept first hunk only",
		Changes: []ChangeEditInput{{
			Path:    "style.css",
			Content: ":root {\n  --bg: #111;\n  --fg: #fff;\n}\n",
		}},
	})
	if err != nil || updated.Revision != 2 || len(updated.Changes) != 1 || !strings.Contains(updated.Changes[0].Content, "--fg: #fff;") {
		t.Fatalf("hunk proposal update failed: %+v, %v", updated, err)
	}
	reverted, err := service.SendChangeMessage(context.Background(), proposal.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(updated),
		Message:        "Reject remaining hunk",
		Changes: []ChangeEditInput{{
			Path:    "style.css",
			Content: ":root {\n  --bg: #000;\n  --fg: #fff;\n}\n",
		}},
	})
	if err != nil || reverted.Revision != 3 || len(reverted.Changes) != 0 {
		t.Fatalf("reverting all hunks failed: %+v, %v", reverted, err)
	}
}
