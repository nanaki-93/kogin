package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

func TestKnowledgeGraphPersistsStartAndTruthfulOutcome(t *testing.T) {
	for _, kind := range knowledgeKinds {
		for _, state := range []string{"completed", "failed", "partial", "unavailable"} {
			t.Run(string(kind)+"/"+state, func(t *testing.T) {
				ctx, budget, store := persistedContext(t, t.TempDir())
				calls := 0
				result, err := RunKnowledgeGraph(ctx, kind, func(ctx context.Context) (string, error) {
					calls++
					sessions, err := store.Sessions(ctx, "kasuri", "local")
					if err != nil || len(sessions) != 1 || len(sessions[0].Events) != 1 {
						return "", fmt.Errorf("host ran before its durable start: %v", err)
					}
					return state, nil
				})
				if err != nil || calls != 1 || result != (KnowledgeOutcome{Kind: kind, Status: state}) || budget.Snapshot().ModelCalls != 0 {
					t.Fatalf("knowledge result=%+v calls=%d err=%v", result, calls, err)
				}
				history, err := store.History(t.Context())
				if err != nil || len(history.Runs) != 1 || history.Runs[0].SchemaVersion != "knowledge-graph-v1" {
					t.Fatalf("missing native history: %+v %v", history, err)
				}
				settled := false
				for _, event := range history.Events {
					if event.Kind == "node" && event.State != "running" {
						settled = true
						if event.NodeID != "knowledge_"+string(kind) || event.State != analysisProgressState(state) {
							t.Fatalf("outcome became fabricated success: %+v", event)
						}
					}
				}
				if !settled {
					t.Fatal("missing settled progress")
				}
			})
		}
	}
}

func TestKnowledgeGraphRejectsUnsettledAndInvalidWorkWithoutRetries(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	for _, kind := range []KnowledgeKind{"", "overview/source.go"} {
		if _, err := RunKnowledgeGraph(ctx, kind, func(context.Context) (string, error) {
			t.Fatal("invalid operation dispatched")
			return "completed", nil
		}); err == nil {
			t.Fatal("invalid operation accepted")
		}
	}
	if _, err := RunKnowledgeGraph(ctx, KnowledgeOverview, nil); err == nil {
		t.Fatal("missing host accepted")
	}
	for _, status := range []string{"running", "error"} {
		calls := 0
		sentinel := errors.New("host authority failed")
		result, err := RunKnowledgeGraph(ctx, KnowledgeFeatures, func(context.Context) (string, error) {
			calls++
			if status == "error" {
				return "", sentinel
			}
			return status, nil
		})
		if err == nil || calls != 1 || result.Kind != "" || status == "error" && !errors.Is(err, sentinel) {
			t.Fatalf("invalid work succeeded or retried: %+v %d %v", result, calls, err)
		}
	}
}

func TestKnowledgeGraphCancellationJoinsHost(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	started, exited := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := RunKnowledgeGraph(ctx, KnowledgeExplanation, func(ctx context.Context) (string, error) {
			defer close(exited)
			close(started)
			<-ctx.Done()
			return "", context.Cause(ctx)
		})
		done <- err
	}()
	<-started
	CancelRun(ctx, context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("canceled graph left its host running")
	}
}

func TestKnowledgeGraphPersistenceFailureIsNotSuccess(t *testing.T) {
	root := t.TempDir()
	ctx, _, _ := persistedContext(t, root)
	result, err := RunKnowledgeGraph(ctx, KnowledgeOverview, func(context.Context) (string, error) {
		journal := filepath.Join(root, ".kasuri", "agents")
		if err := os.Rename(journal, filepath.Join(root, "saved")); err != nil {
			return "", err
		}
		return "completed", os.WriteFile(journal, []byte("blocked"), 0600)
	})
	if result.Kind != "" || !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) {
		t.Fatalf("persistence failure became success: %+v %v", result, err)
	}
}
