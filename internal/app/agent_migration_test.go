package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func rewriteMigrationJournal(t *testing.T, root string, edit func(map[string]json.RawMessage)) (string, []byte) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, ".mini-orca", "agents", "*", "journal.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("expected one journal", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	edit(document)
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], data, 0600); err != nil {
		t.Fatal(err)
	}
	return paths[0], data
}

func seedAgentRun(t *testing.T, s *Service, store *agentstate.Store, id, parent string, started time.Time) agentstate.Run {
	t.Helper()
	_, binding, err := s.agentJournal(s.manager.Root())
	if err != nil {
		t.Fatal(err)
	}
	key := agentstate.Key{App: "mini-orca", User: "local", ID: id}
	if _, err := store.CreateSession(t.Context(), key, agentstate.State{"private": json.RawMessage(`"PRIVATE_PROMPT"`)}); err != nil {
		t.Fatal(err)
	}
	budgetID := "budget-" + id
	if parent != "" {
		budgetID = "budget-" + parent
	}
	if parent == "" {
		if _, err := store.Checkpoint(t.Context(), agentstate.BudgetRecord{ID: budgetID, Binding: binding, Limits: s.conf().agentRuntime.Limits, Sequence: 1}, nil); err != nil {
			t.Fatal(err)
		}
	}
	run := agentstate.Run{ID: id, ParentID: parent, Session: key, BudgetID: budgetID, RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: "completion-v1", Status: "running", Started: started, Updated: started}
	if _, err := store.BeginRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestAgentMigrationReadsLegacyPassivelyAndAdmitsOnlyFreshNativeWork(t *testing.T) {
	var calls atomic.Int32
	provider := changeProvider(t, func() string { calls.Add(1); return "admitted result" })
	s, root := newSemanticAnalysisService(t, provider.URL, 0)
	store, _, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	finished := seedAgentRun(t, s, store, "old-finished", "", time.Now().UTC())
	if err := store.FinishRun(t.Context(), finished.ID, "completed", nil); err != nil {
		t.Fatal(err)
	}
	seedAgentRun(t, s, store, "old-abandoned", "", time.Now().UTC())
	before, err := store.History(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path, original := rewriteMigrationJournal(t, root, func(value map[string]json.RawMessage) {
		value["version"] = json.RawMessage(`1`)
		delete(value, "runtime_family")
	})
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	list, err := legacy.History(t.Context())
	roots := 0
	for _, run := range list.Runs {
		if run.ParentID == "" && run.Binding.Project == index.ProjectID {
			roots++
		}
	}
	if err != nil || roots != 2 {
		t.Fatal(list, err)
	}
	if _, err := InspectAgentState(t.Context(), AgentInspectionOptions{Root: root, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(path)
	if err != nil || string(read) != string(original) || calls.Load() != 0 {
		t.Fatal("passive legacy history mutated or dispatched", err)
	}
	result, err := s.retryRequest(t.Context(), s.conf().runtimes.function, []llm.ChatMessage{{Role: "user", Content: "explicit new request"}}, nil)
	if err != nil || result.Content != "admitted result" || calls.Load() != 1 {
		t.Fatal("native request", result, err)
	}
	after, err := store.History(t.Context())
	if err != nil || len(after.Runs) != 3 {
		t.Fatal("fresh native invocation missing", err)
	}
	for _, run := range after.Runs {
		switch run.ID {
		case "old-finished":
			for _, prior := range before.Runs {
				if prior.ID == run.ID && !reflect.DeepEqual(prior, run) {
					t.Fatal("completed history lost provenance")
				}
			}
		case "old-abandoned":
			if run.Status != "interrupted" {
				t.Fatal("old invocation reused", run.Status)
			}
		default:
			if run.Status != "completed" || run.SchemaVersion != "completion-v1" {
				t.Fatal("fresh work bypassed ADK persistence", run)
			}
		}
	}
	read, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(read), `"runtime_family":"adk-go-v2"`) {
		t.Fatal("runtime compatibility not committed", err)
	}
}

func TestAgentMigrationFailureAndCancellationPreventProviderDispatch(t *testing.T) {
	for _, kind := range []string{"future", "downgrade", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			provider := changeProvider(t, func() string { calls.Add(1); return "must not run" })
			s, root := newSemanticAnalysisService(t, provider.URL, 0)
			store, _, err := s.agentJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			seedAgentRun(t, s, store, "legacy", "", time.Now().UTC())
			path, original := rewriteMigrationJournal(t, root, func(value map[string]json.RawMessage) {
				if kind == "future" {
					value["version"] = json.RawMessage(`99`)
				} else {
					value["version"] = json.RawMessage(`1`)
				}
				if kind == "canceled" {
					delete(value, "runtime_family")
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			_, err = s.retryRequest(ctx, s.conf().runtimes.function, []llm.ChatMessage{{Role: "user", Content: "request"}}, nil)
			if kind == "downgrade" {
				// An unreadable journal is set aside with its bytes; work then
				// starts a new journal.
				records, qerr := agentstate.Quarantines(root)
				if err != nil || qerr != nil || len(records) != 1 || calls.Load() != 1 {
					t.Fatalf("unreadable journal not set aside: %+v %v %v", records, err, qerr)
				}
				if kept, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(records[0].QuarantinedAs))); err != nil || string(kept) != string(original) {
					t.Fatal("set-aside journal lost its bytes", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed migration became admitted work")
			}
			if kind == "future" && !strings.Contains(err.Error(), "upgrade Mini-Orca") {
				t.Fatalf("newer journal error has no remedy: %v", err)
			}
			read, err := os.ReadFile(path)
			if err != nil || string(read) != string(original) || calls.Load() != 0 {
				t.Fatal("rejected admission mutated or dispatched", err)
			}
		})
	}
}

func TestAgentMigrationPreservesSchemaOneConversationAndRejectsAmbiguity(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	conversation := openChangeFixture(t, s, "main.go")
	path, err := changeMetadataPath(root, conversation.ID+".json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := loadChangeSession(root, conversation.ID)
	if err != nil || !reflect.DeepEqual(conversation.Targets, restored.Targets) || conversation.Hash != restored.Hash || restored.Workflow != nil {
		t.Fatal("schema-1 conversation lost captured provenance", err)
	}
	var future map[string]json.RawMessage
	if err := json.Unmarshal(data, &future); err != nil {
		t.Fatal(err)
	}
	future["schema_version"] = json.RawMessage(`99`)
	futureData, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{append([]byte(`{"schema_version":1,`), data[1:]...), append(append([]byte{}, data...), []byte(` {}`)...), futureData} {
		if err := os.WriteFile(path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadChangeSession(root, conversation.ID); err == nil {
			t.Fatal("ambiguous or future change history accepted")
		}
		preserved, err := os.ReadFile(path)
		if err != nil || string(preserved) != string(invalid) {
			t.Fatal("corrupt change history overwritten", err)
		}
	}
	for _, text := range []string{`{"schema_version":"1","schema_version":"1","plan":{"compatibility_stage":"semantic"}}`, `{"schema_version":"1","plan":{"compatibility_stage":"performance"}} {}`, `{"schema_version":"future","plan":{"compatibility_stage":"semantic"}}`} {
		if _, err := decodeCompatibleAnalysisHistory([]byte(text)); !errors.Is(err, errAnalysisRunCorrupt) {
			t.Fatal("legacy exception hid corrupt analysis", err)
		}
	}
}

func TestAgentMigrationNamesSavedStagesAndRejectsInvalidGraphEntry(t *testing.T) {
	models := ChangeWorkflowModels{Create: "bug", Test: "code", Review: "analyze"}
	session := &ChangeSession{Workflow: &ChangeWorkflow{Stages: []ChangeWorkflowStage{{Name: "test", Model: &EffectiveModel{Profile: "code"}}, {Name: "create", Model: &EffectiveModel{Profile: "bug"}}}}}
	if err := validateResumedModels(session, models, 2); err != nil {
		t.Fatal("named stage provenance lost", err)
	}
	for _, invalid := range []int{-1, 3, 100} {
		if err := validateResumedModels(session, models, invalid); err == nil {
			t.Fatal("invalid resume index accepted")
		}
	}
	for _, invalid := range []agentruntime.ChangeNode{"", "lint", agentruntime.ChangeHumanReview} {
		if err := (&Service{}).executeChangeGraph(t.Context(), nil, invalid, ""); err == nil {
			t.Fatal("invalid graph entry accepted")
		}
	}
	session.Workflow.Stages = append(session.Workflow.Stages, session.Workflow.Stages[1])
	if err := validateResumedModels(session, models, 2); err == nil {
		t.Fatal("duplicate stage identity accepted")
	}
	session.Workflow.Stages = []ChangeWorkflowStage{{Name: "create"}}
	if err := validateResumedModels(session, models, 1); err == nil {
		t.Fatal("missing model provenance accepted")
	}
}
