package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

func changeGraphFixture() ChangeGraphInput {
	return ChangeGraphInput{Snapshot: ChangeSnapshot{ProjectID: "project", ProjectRevision: "revision", SessionID: "change", WorkflowID: "workflow", Revision: 9007199254740993, Hash: "initial"}, Start: ChangeCreate, Models: [3]string{"creator", "tester", "critic"}}
}

func changeGraphHost(_ context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
	result := ChangeNodeResult{Node: input.Node, Snapshot: input.Snapshot, Status: "completed"}
	switch input.Node {
	case ChangeCreate, ChangeTests:
		result.Snapshot.Revision++
		result.Snapshot.Hash = string(input.Node)
	case ChangeReview:
		result.Verdict = "approve"
	case ChangeHumanReview:
		result.Verdict, result.Status = input.PriorVerdict, "awaiting_human_review"
	}
	return result, nil
}

func TestChangeGraphOrdersTypedNodesAndPersistsBeforeHostDispatch(t *testing.T) {
	ctx, budget, store := persistedContext(t, t.TempDir())
	var order []ChangeNode
	result, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
		records, err := store.Sessions(ctx, "kasuri", "local")
		if err != nil || len(records) != 1 || len(records[0].Events) != len(order)*2+1 {
			return ChangeNodeResult{}, fmt.Errorf("node dispatched before its start event was durable: %v", err)
		}
		var event struct{ CustomMetadata map[string]any }
		if err := json.Unmarshal(records[0].Events[len(records[0].Events)-1].Data, &event); err != nil || event.CustomMetadata["mini_orca.change_node"] != string(input.Node) {
			return ChangeNodeResult{}, fmt.Errorf("start event has incorrect node identity: %+v %v", event, err)
		}
		order = append(order, input.Node)
		return changeGraphHost(ctx, input)
	})
	if err != nil || !reflect.DeepEqual(order, changeNodes) || result.Outcome != "awaiting_human_review" || len(result.Nodes) != 5 {
		t.Fatalf("graph: %+v order=%v error=%v", result, order, err)
	}
	if result.Nodes[4].Snapshot.Revision != 9007199254740995 || budget.Snapshot().ModelCalls != 0 || budget.Snapshot().ToolCalls != 0 {
		t.Fatal("graph lost exact identity or charged a host node as a model/tool call")
	}
	records, err := store.Sessions(t.Context(), "kasuri", "local")
	if err != nil || len(records[0].Events) != 10 || records[0].State["mini_orca.change_graph"] == nil {
		t.Fatalf("missing durable graph: %+v %v", records, err)
	}
}

func TestChangeGraphResumeAndFailureDoNotRepeatEarlierNodes(t *testing.T) {
	for _, start := range []ChangeNode{ChangeCreate, ChangeTests, ChangeVerify, ChangeReview} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", start, fail), func(t *testing.T) {
				ctx, _ := budgetContext(t, nil)
				input := changeGraphFixture()
				input.Start = start
				var order []ChangeNode
				sentinel := errors.New("host failure")
				result, err := RunChangeGraph(ctx, input, func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
					order = append(order, input.Node)
					if fail {
						return ChangeNodeResult{}, sentinel
					}
					return changeGraphHost(ctx, input)
				})
				if len(order) == 0 || order[0] != start {
					t.Fatalf("resumed wrong stage: %v", order)
				}
				if fail {
					if len(order) != 1 || !errors.Is(err, sentinel) || len(result.Nodes) != 0 {
						t.Fatalf("host failure retried or continued: %v %v", order, err)
					}
				} else if err != nil || order[len(order)-1] != ChangeHumanReview {
					t.Fatalf("resume failed: %v %v", order, err)
				}
			})
		}
	}
}

func TestChangeGraphRejectsAlteredIdentityBeforeLaterNodes(t *testing.T) {
	for _, alter := range []func(*ChangeNodeResult){
		func(r *ChangeNodeResult) { r.Snapshot.WorkflowID = "replacement" },
		func(r *ChangeNodeResult) { r.Snapshot.ProjectRevision = "replacement" },
		func(r *ChangeNodeResult) { r.Snapshot.Revision = 0 },
		func(r *ChangeNodeResult) { r.Node = ChangeHumanReview },
	} {
		ctx, _ := budgetContext(t, nil)
		calls := 0
		result, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
			calls++
			result, err := changeGraphHost(ctx, input)
			alter(&result)
			return result, err
		})
		if !errors.Is(err, ErrPolicyDenied) || calls != 1 || len(result.Nodes) != 0 {
			t.Fatalf("invalid identity continued: %+v %d %v", result, calls, err)
		}
	}
}

func TestChangeGraphReadOnlyNodesCannotChangeTheProposal(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	var order []ChangeNode
	_, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
		order = append(order, input.Node)
		result, err := changeGraphHost(ctx, input)
		if input.Node == ChangeVerify {
			result.Snapshot.Hash = "unauthorized edit"
		}
		return result, err
	})
	if !errors.Is(err, ErrPolicyDenied) || len(order) != 3 {
		t.Fatalf("read-only node edited proposal: %v %v", order, err)
	}
}

func TestChangeGraphCancellationDrainsHostBeforeReturning(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	started, exited := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, _ ChangeNodeInput) (ChangeNodeResult, error) {
			calls.Add(1)
			close(started)
			<-ctx.Done()
			close(exited)
			return ChangeNodeResult{}, context.Cause(ctx)
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancel did not stop graph: %v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("graph returned with a host goroutine still active")
	}
}

func TestChangeGraphPersistenceFailureStopsSuccessors(t *testing.T) {
	root := t.TempDir()
	ctx, _, _ := persistedContext(t, root)
	calls := 0
	result, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
		calls++
		if err := os.Rename(filepath.Join(root, ".kasuri", "agents"), filepath.Join(root, "saved-agents")); err != nil {
			return ChangeNodeResult{}, err
		}
		if err := os.WriteFile(filepath.Join(root, ".kasuri", "agents"), []byte("blocked"), 0600); err != nil {
			return ChangeNodeResult{}, err
		}
		return changeGraphHost(ctx, input)
	})
	if !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) || calls != 1 || len(result.Nodes) != 0 {
		t.Fatalf("failed persistence continued: %+v %d %v", result, calls, err)
	}
}

func TestChangeGraphInvalidAdmissionDispatchesNothing(t *testing.T) {
	for _, alter := range []func(*ChangeGraphInput){
		func(r *ChangeGraphInput) { r.Start = ChangeHumanReview },
		func(r *ChangeGraphInput) { r.Start = "lint" },
		func(r *ChangeGraphInput) { r.Models[1] = "" },
		func(r *ChangeGraphInput) { r.Snapshot.Revision = -1 },
		func(r *ChangeGraphInput) { r.Snapshot.Hash = strings.Repeat("x", 513) },
	} {
		input := changeGraphFixture()
		alter(&input)
		_, err := RunChangeGraph(t.Context(), input, func(context.Context, ChangeNodeInput) (ChangeNodeResult, error) {
			t.Fatal("invalid admission dispatched")
			return ChangeNodeResult{}, nil
		})
		if err == nil {
			t.Fatal("invalid admission accepted")
		}
	}
}

func TestChangeGraphCriticRejectionCannotBecomeHumanApproval(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	result, err := RunChangeGraph(ctx, changeGraphFixture(), func(ctx context.Context, input ChangeNodeInput) (ChangeNodeResult, error) {
		result, err := changeGraphHost(ctx, input)
		if input.Node == ChangeReview {
			result.Verdict = "changes_requested"
		}
		if input.Node == ChangeHumanReview {
			result.Status = "changes_requested"
		}
		return result, err
	})
	if err != nil || result.Outcome != "changes_requested" {
		t.Fatalf("critic rejection lost: %+v %v", result, err)
	}
}
