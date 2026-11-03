package agentruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
)

func confirmationFixture() ConfirmationInput {
	return ConfirmationInput{RequestID: "request", Action: "review_proposal", Snapshot: changeGraphFixture().Snapshot, ScopeHash: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}
}

func TestNativeConfirmationPausesReopensAndResumesWithoutDispatch(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			root := t.TempDir()
			ctx, budget, store := persistedContext(t, root)
			input := confirmationFixture()
			id, err := RequestConfirmation(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := fingerprint(input)
			if err != nil {
				t.Fatal(err)
			}
			binding := agentstate.Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}
			admission := agentstate.InputAdmission{RunID: id, RequestID: input.RequestID, RequestHash: hash, Binding: binding}
			run, err := store.InputRun(t.Context(), admission)
			if err != nil || run.Status != "waiting_input" || run.Input.Decision != "" {
				t.Fatalf("checkpoint: %+v %v", run, err)
			}
			stored, err := store.Session(t.Context(), run.Session)
			if err != nil || len(stored.Events) == 0 {
				t.Fatalf("native request not saved: %v", err)
			}
			var exact ConfirmationInput
			if err := json.Unmarshal(stored.State["mini_orca.confirmation"], &exact); err != nil || exact != input {
				t.Fatalf("checkpoint lost exact int64 identity: %+v %v", exact, err)
			}
			reopened, err := agentstate.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := ResumeConfirmation(t.Context(), reopened, binding, id, input, decision)
			if err != nil || receipt.Decision != decision || receipt.ReceivedAt.IsZero() {
				t.Fatalf("resume: %+v %v", receipt, err)
			}
			after, err := reopened.Session(t.Context(), run.Session)
			if err != nil || len(after.Events) <= len(stored.Events) {
				t.Fatalf("native response not recorded: %v", err)
			}
			again, err := ResumeConfirmation(t.Context(), reopened, binding, id, input, decision)
			if err != nil || again != receipt {
				t.Fatalf("duplicate receipt: %+v %v", again, err)
			}
			duplicate, err := reopened.Session(t.Context(), run.Session)
			if err != nil || len(duplicate.Events) != len(after.Events) {
				t.Fatalf("duplicate replayed native handoff: %v", err)
			}
			if budget.Snapshot().ModelCalls != 0 || budget.Snapshot().ToolCalls != 0 {
				t.Fatal("human input dispatched a model or tool")
			}
		})
	}
}

func TestConfirmationRequiresExactScopeAndCurrentAdmission(t *testing.T) {
	ctx, _, store := persistedContext(t, t.TempDir())
	input := confirmationFixture()
	id, err := RequestConfirmation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	binding := agentstate.Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}
	for _, alter := range []func(*ConfirmationInput){
		func(i *ConfirmationInput) { i.Snapshot.Revision++ },
		func(i *ConfirmationInput) { i.Snapshot.Hash = "replacement" },
		func(i *ConfirmationInput) { i.ScopeHash = strings.Repeat("b", 64) },
		func(i *ConfirmationInput) { i.Action = "apply" },
		func(i *ConfirmationInput) { i.RequestID = "earlier" },
	} {
		changed := input
		alter(&changed)
		if _, err := ResumeConfirmation(t.Context(), store, binding, id, changed, "approve"); err == nil {
			t.Fatal("altered input authorized resume")
		}
	}
	changed := binding
	changed.Configuration = "replacement"
	if _, err := ResumeConfirmation(t.Context(), store, changed, id, input, "approve"); err == nil {
		t.Fatal("changed configuration resumed")
	}
	if _, err := ResumeConfirmation(t.Context(), store, binding, id, input, "apply"); err == nil {
		t.Fatal("arbitrary action resumed")
	}
	if _, err := RequestConfirmation(t.Context(), input); err == nil {
		t.Fatal("non-durable checkpoint created")
	}
}

func TestConfirmationResumePreservesWaitingCheckpointOnStorageFailure(t *testing.T) {
	root := t.TempDir()
	ctx, _, store := persistedContext(t, root)
	input := confirmationFixture()
	id, err := RequestConfirmation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	binding := agentstate.Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "config"}
	directory, backup := filepath.Join(root, ".kogin", "agents"), filepath.Join(root, "saved-agents")
	if err := os.Rename(directory, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeConfirmation(t.Context(), store, binding, id, input, "approve"); err == nil {
		t.Fatal("storage alias accepted response")
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, directory); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ResumeConfirmation(canceled, store, binding, id, input, "approve"); err == nil {
		t.Fatal("canceled response accepted")
	}
	if _, err := ResumeConfirmation(t.Context(), store, binding, id, input, "approve"); err != nil {
		t.Fatalf("valid recovery failed: %v", err)
	}
}
