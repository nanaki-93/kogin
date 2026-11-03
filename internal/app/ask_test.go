package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// askService has the Review role on a pi agent the test binary plays, with
// the asker's script.
func askService(t *testing.T, steps []fakePiStep, review string) (*Service, string, string) {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(steps)
	if err := os.WriteFile(filepath.Join(dir, "asker.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeCLIEnv, "1")
	t.Setenv(fakeCLIDirEnv, dir)
	fixture, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	writeDocsFixture(t, root)
	cfg := scopedTestConfig("http://127.0.0.1:1")
	cfg.ModelScopes.Code = piScope("fake/asker")
	cfg.AgentRuntime.Roles.Review = config.ModelScope(review)
	service, err := New(cfg, fixture.manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.manager.Reindex(); err != nil {
		t.Fatal(err)
	}
	return service, root, dir
}

func askRequest(t *testing.T, s *Service, question string) AskRequest {
	t.Helper()
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return AskRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Question: question, ConfirmRemoteProvider: true}
}

func TestAskingTheCodebaseAnswersWithCheckedCitations(t *testing.T) {
	steps := []fakePiStep{
		{Tool: "search", Arguments: map[string]any{"query": "Items"}},
		{Tool: "read_file", Arguments: map[string]any{"path": "store/store.go"}},
		// A quote that is not in the lines is reported, and the agent fixes it.
		{Tool: "answer", Arguments: map[string]any{"answer": "The store keeps items.", "citations": []map[string]any{{"path": "store/store.go", "start_line": 1, "end_line": 2, "quote": "var Items"}}}},
		{Tool: "answer", Arguments: map[string]any{"answer": "The store keeps items in a slice of model.Item.", "citations": []map[string]any{{"path": "store/store.go", "start_line": 3, "end_line": 5, "quote": "var Items   []model.Item"}}}},
		{Text: "Done."},
	}
	s, _, dir := askService(t, steps, "code")
	answer, err := s.AskCodebase(context.Background(), askRequest(t, s, "Where are items kept?"))
	if err != nil {
		t.Fatal(err)
	}
	if answer.Answer != "The store keeps items in a slice of model.Item." || len(answer.Citations) != 1 || answer.ToolCalls != 4 {
		t.Fatalf("answer = %+v", answer)
	}
	if citation := answer.Citations[0]; citation.Path != "store/store.go" || citation.StartLine != 3 || citation.EndLine != 5 || !strings.HasPrefix(citation.ContentHash, "sha256:") {
		t.Fatalf("citation = %+v", citation)
	}
	record := fakePiLog(t, dir, "asker")
	if !strings.Contains(record.Prompt, "Question: Where are items kept?") || !strings.Contains(record.Prompt, "Project map") || !strings.Contains(record.Prompt, "- store (") {
		t.Fatalf("prompt = %s", record.Prompt)
	}
	if len(record.Results) < 3 || !strings.Contains(record.Results[2], "do not contain the quote") || !strings.Contains(record.Results[3], "Answer recorded") {
		t.Fatalf("answer results = %q", record.Results)
	}
}

func TestAskingTheCodebaseNeedsAnAgentModelConsentAndAnAnswer(t *testing.T) {
	s, _, _ := askService(t, []fakePiStep{{Text: "I think it is in the store."}}, "analyze")
	if _, err := s.AskCodebase(context.Background(), askRequest(t, s, "Where are items kept?")); !errors.Is(err, ErrAskNeedsAgentModel) {
		t.Fatalf("an HTTP review model answered: %v", err)
	}
	s, _, _ = askService(t, []fakePiStep{{Text: "I think it is in the store."}}, "code")
	request := askRequest(t, s, "Where are items kept?")
	request.ConfirmRemoteProvider = false
	if _, err := s.AskCodebase(context.Background(), request); err == nil || !strings.Contains(err.Error(), "requires explicit confirmation") {
		t.Fatalf("a CLI model ran without consent: %v", err)
	}
	if _, err := s.AskCodebase(context.Background(), askRequest(t, s, "  ")); err == nil {
		t.Fatal("an empty question was asked")
	}
	if _, err := s.AskCodebase(context.Background(), askRequest(t, s, "Where are items kept?")); err == nil || !strings.Contains(err.Error(), "without an answer") {
		t.Fatalf("a run without an answer = %v", err)
	}
}
