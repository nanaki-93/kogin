package agentstate

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func inputFixture(t *testing.T, expired bool) (*Store, Key, InputAdmission) {
	t.Helper()
	s, key := testStore(t)
	runtime, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	admission := InputAdmission{RunID: "run", RequestID: "request", RequestHash: strings.Repeat("a", 64), Binding: Binding{"project", "revision", "policy", "configuration"}}
	if _, err := s.Checkpoint(t.Context(), BudgetRecord{ID: "budget", Binding: admission.Binding, Limits: runtime.Limits, Sequence: 1}, nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if expired {
		started = started.Add(-2 * time.Hour)
	}
	run := Run{ID: admission.RunID, Session: key, BudgetID: "budget", RequestHash: admission.RequestHash, SchemaHash: strings.Repeat("b", 64), SchemaVersion: "confirmation-v1", Status: "running", Started: started, Updated: started}
	if _, err := s.BeginRun(t.Context(), run, nil); err != nil {
		t.Fatal(err)
	}
	checkpoint := InputCheckpoint{RequestID: admission.RequestID, NodeID: "human_review", InvocationID: "invocation", ExpiresAt: started.Add(time.Hour)}
	if err := s.PauseForInput(t.Context(), run.ID, checkpoint); err != nil {
		t.Fatal(err)
	}
	return s, key, admission
}

func TestInputReceiptIsDurableIdempotentAndProtected(t *testing.T) {
	s, key, admission := inputFixture(t, false)
	artifact := artifactFixture(key, "checkpoint.txt", "recovery")
	if _, err := s.SaveArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(t.Context(), key); err == nil {
		t.Fatal("deleted waiting checkpoint")
	}
	if err := s.DeleteArtifacts(t.Context(), artifact.Key, ArtifactRetention{}); !errors.Is(err, ErrArtifactInUse) {
		t.Fatalf("deleted waiting evidence: %v", err)
	}
	if err := s.FinishRun(t.Context(), admission.RunID, "completed", nil); err == nil {
		t.Fatal("ordinary finish bypassed input receipt")
	}
	if err := s.FinishInput(t.Context(), admission); err == nil {
		t.Fatal("completed without response")
	}
	first, err := s.ReceiveInput(t.Context(), admission, "approve")
	if err != nil || first.Status != "waiting_input" || first.Input.Decision != "approve" {
		t.Fatalf("receipt: %+v %v", first, err)
	}
	reopened, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.ReceiveInput(t.Context(), admission, "approve")
	if err != nil || *second.Input != *first.Input {
		t.Fatalf("duplicate changed receipt: %+v %v", second, err)
	}
	if _, err := reopened.ReceiveInput(t.Context(), admission, "reject"); err == nil {
		t.Fatal("conflicting input replaced approval")
	}
	if err := reopened.FinishInput(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	if err := reopened.FinishInput(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteSession(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}

func TestInputExpiryAndConcurrentConflictingResponses(t *testing.T) {
	s, _, admission := inputFixture(t, true)
	run, err := s.ReceiveInput(t.Context(), admission, "approve")
	if err != nil || run.Input.Decision != "expire" {
		t.Fatalf("expired input approved: %+v %v", run, err)
	}
	if err := s.FinishInput(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	run, err = s.InputRun(t.Context(), admission)
	if err != nil || run.Status != "expired" {
		t.Fatalf("expiry lost: %+v %v", run, err)
	}
	s, _, admission = inputFixture(t, false)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, decision := range []string{"approve", "reject"} {
		wg.Go(func() { _, err := s.ReceiveInput(t.Context(), admission, decision); results <- err })
	}
	wg.Wait()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("conflicting decisions not serialized: %v %v", first, second)
	}
}

func TestInputAdmissionAndStorageFailuresCannotCreateReceipts(t *testing.T) {
	s, _, admission := inputFixture(t, false)
	for _, alter := range []func(*InputAdmission){
		func(a *InputAdmission) { a.RequestID = "other" },
		func(a *InputAdmission) { a.RequestHash = strings.Repeat("c", 64) },
		func(a *InputAdmission) { a.Binding.Policy = "changed" },
		func(a *InputAdmission) { a.Binding.Configuration = "changed" },
	} {
		changed := admission
		alter(&changed)
		if _, err := s.ReceiveInput(t.Context(), changed, "approve"); err == nil {
			t.Fatal("changed admission was accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ReceiveInput(ctx, admission, "approve"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	s.write = func(string, []byte, os.FileMode, func() error) error { return errors.New("disk unavailable") }
	if _, err := s.ReceiveInput(t.Context(), admission, "approve"); !errors.Is(err, ErrPersistence) {
		t.Fatalf("storage failure hidden: %v", err)
	}
	run, err := s.InputRun(t.Context(), admission)
	if err != nil || run.Input.Decision != "" {
		t.Fatalf("failed receipt leaked: %+v %v", run, err)
	}
}
