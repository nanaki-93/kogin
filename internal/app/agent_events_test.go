package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func TestAgentWorkflowStreamingOutputIsOptIn(t *testing.T) {
	base, _, initial := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval }})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream {
			t.Errorf("stream choice lost: %+v %v", request, err)
		}
		content := map[string]string{"creator": workflowImplementation, "tester": workflowTests, "reviewer": workflowApproval}[request.Model]
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"model": request.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}}}})
		fmt.Fprintf(w, "data: %s\n\n", data)
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	cfg := scopedTestConfig(provider.URL)
	cfg.ModelScopes.Bug.Model, cfg.ModelScopes.Analyze.Model, cfg.ModelScopes.Code.Model = "creator", "tester", "reviewer"
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CancelChangeWorkflows)
	if _, err := s.TrustProjectExecution(t.Context(), initial.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	request := workflowRequest(initial)
	request.StreamOutput = true
	if _, err := s.StartChangeWorkflow(t.Context(), initial.ID, request); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, initial.ID)
	if ready.Workflow.Status != "awaiting_human_review" || !ready.Workflow.StreamOutput {
		t.Fatalf("streamed workflow failed: %+v", ready.Workflow)
	}
	history, store := agentJournalHistory(t, s)
	outputs := 0
	for _, run := range history.Runs {
		if run.SchemaVersion != "completion-v1" {
			continue
		}
		outputs++
		artifact, err := store.ExportArtifact(t.Context(), agentstate.ArtifactKey{Session: run.Session, Name: "completion.json"}, 0)
		var output struct{ Content string }
		if err != nil || json.Unmarshal(artifact.Data, &output) != nil || output.Content == "" {
			t.Fatalf("final output: %+v %v", artifact, err)
		}
	}
	if outputs != 5 {
		t.Fatalf("unexpected model invocation count: %d", outputs)
	}
	// The provider-neutral compatibility type is still used by ordinary callers.
	if !llm.ProviderCapabilities(s.conf().runtimes.bug.profile).Streaming {
		t.Fatal("streamed with unavailable capability")
	}
}

// agentJournalHistory reads the agent journal of the active checkout.
func agentJournalHistory(t *testing.T, s *Service) (agentstate.History, *agentstate.Store) {
	t.Helper()
	store, _, err := s.agentJournal(s.manager.Root())
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.History(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return history, store
}
