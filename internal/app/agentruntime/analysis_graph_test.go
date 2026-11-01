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
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
)

func analysisGraphFixture() AnalysisGraphInput {
	input := AnalysisGraphInput{Snapshot: AnalysisSnapshot{ProjectID: "project", ProjectRevision: "revision", RunID: "analysis", Generation: "generation", QueueID: "queue", PolicyFingerprint: "policy", ProviderFingerprint: "provider"}}
	for i, stage := range analysisNodeStages {
		input.Work = append(input.Work, AnalysisWork{Ordinal: i, FileIndex: 7, StageIndex: i, Stage: stage, FileHash: "PRIVATE_SOURCE_HASH"})
	}
	return input
}

func TestAnalysisGraphPersistsOrderedStagesAndTruthfulOutcomes(t *testing.T) {
	ctx, budget, store := persistedContext(t, t.TempDir())
	input := analysisGraphFixture()
	statuses := []string{"completed_empty", "partial", "completed", "failed"}
	var order []AnalysisWork
	result, err := RunAnalysisGraph(ctx, input, func(ctx context.Context, work AnalysisWork) (AnalysisOutcome, error) {
		records, err := store.Sessions(ctx, "kasuri", "local")
		if err != nil || len(records) != 1 || len(records[0].Events) != 2*len(order)+1 {
			return AnalysisOutcome{}, fmt.Errorf("analysis dispatched without its durable start: %v", err)
		}
		order = append(order, work)
		return AnalysisOutcome{Status: statuses[work.Ordinal], Continue: true}, nil
	})
	if err != nil || !reflect.DeepEqual(order, input.Work) || len(result.Nodes) != 4 || result.Stopped || budget.Snapshot().ModelCalls != 0 {
		t.Fatalf("window=%+v order=%+v err=%v", result, order, err)
	}
	history, err := store.History(t.Context())
	if err != nil || len(history.Runs) != 1 || history.Runs[0].SchemaVersion != "analysis-graph-v1" {
		t.Fatalf("history=%+v %v", history, err)
	}
	want := map[string]string{"analysis_0000_semantic": "completed", "analysis_0001_performance": "recorded", "analysis_0002_security_rules": "completed", "analysis_0003_security_ai": "failed"}
	for _, event := range history.Events {
		if event.Kind == "node" && event.State != "running" {
			if want[event.NodeID] != event.State {
				t.Fatalf("stage outcome became success: %+v", event)
			}
			delete(want, event.NodeID)
		}
	}
	data, err := json.Marshal(history.Events)
	if err != nil || len(want) != 0 || strings.Contains(string(data), "PRIVATE_SOURCE_HASH") {
		t.Fatalf("missing or source-bearing progress: %v %s %v", want, data, err)
	}
}

func TestAnalysisGraphStopsWithoutRetriesOrSuccessorDispatch(t *testing.T) {
	for _, state := range []string{"stopped", "failed", "error", "running"} {
		t.Run(state, func(t *testing.T) {
			ctx, _ := budgetContext(t, nil)
			calls := 0
			sentinel := errors.New("host authority lost")
			result, err := RunAnalysisGraph(ctx, analysisGraphFixture(), func(context.Context, AnalysisWork) (AnalysisOutcome, error) {
				calls++
				if state == "error" {
					return AnalysisOutcome{}, sentinel
				}
				return AnalysisOutcome{Status: state}, nil
			})
			if calls != 1 {
				t.Fatalf("stopped node dispatched %d times", calls)
			}
			if state == "error" || state == "running" {
				if err == nil || len(result.Nodes) != 0 || state == "error" && !errors.Is(err, sentinel) {
					t.Fatalf("invalid success: %+v %v", result, err)
				}
			} else if err != nil || !result.Stopped || len(result.Nodes) != 1 || result.Nodes[0].Status != state {
				t.Fatalf("lost stop outcome: %+v %v", result, err)
			}
		})
	}
}

func TestAnalysisGraphRejectsInvalidAdmissionWithoutDispatch(t *testing.T) {
	for _, alter := range []func(*AnalysisGraphInput){
		func(i *AnalysisGraphInput) { i.Snapshot.Generation = "" },
		func(i *AnalysisGraphInput) { i.Work = nil },
		func(i *AnalysisGraphInput) { i.Work[1].Stage = "untrusted" },
		func(i *AnalysisGraphInput) { i.Work[1] = i.Work[0] },
		func(i *AnalysisGraphInput) { i.Work[1].FileHash = "other" },
		func(i *AnalysisGraphInput) { i.Work[1].FileIndex = -1 },
		func(i *AnalysisGraphInput) { i.Work[1].StageIndex = 4 },
	} {
		input := analysisGraphFixture()
		alter(&input)
		_, err := RunAnalysisGraph(t.Context(), input, func(context.Context, AnalysisWork) (AnalysisOutcome, error) {
			t.Error("invalid admission dispatched")
			return AnalysisOutcome{}, nil
		})
		if err == nil {
			t.Fatal("invalid admission accepted")
		}
	}
}

func TestAnalysisGraphBoundsWindowAndSupportsSparseResumption(t *testing.T) {
	input := analysisGraphFixture()
	input.Work = nil
	for file := 0; file < 500; file++ {
		for stage, name := range analysisNodeStages {
			input.Work = append(input.Work, AnalysisWork{Ordinal: len(input.Work), FileIndex: file + 1000, StageIndex: stage, Stage: name, FileHash: "hash"})
		}
	}
	host := func(context.Context, AnalysisWork) (AnalysisOutcome, error) {
		return AnalysisOutcome{Status: "stopped"}, nil
	}
	bounded, _ := budgetContext(t, nil)
	result, err := RunAnalysisGraph(bounded, input, host)
	if err != nil || !result.Stopped || len(result.Nodes) != 1 {
		t.Fatalf("maximum bounded window did not stop cleanly: %+v %v", result, err)
	}
	input.Work = append(input.Work, AnalysisWork{Ordinal: 2000, FileIndex: 1500, Stage: "semantic", FileHash: "hash"})
	if err := validateAnalysisGraph(input, host); err == nil {
		t.Fatal("unbounded stage graph admitted")
	}
	input.Work = input.Work[:501]
	for i := range input.Work {
		input.Work[i] = AnalysisWork{Ordinal: i, FileIndex: i, Stage: "semantic", FileHash: "hash"}
	}
	if err := validateAnalysisGraph(input, host); err == nil {
		t.Fatal("unbounded file window admitted")
	}
	input = analysisGraphFixture()
	input.Work = input.Work[2:]
	for i := range input.Work {
		input.Work[i].Ordinal = i
	}
	ctx, _ := budgetContext(t, nil)
	var order []string
	_, err = RunAnalysisGraph(ctx, input, func(_ context.Context, work AnalysisWork) (AnalysisOutcome, error) {
		order = append(order, work.Stage)
		return AnalysisOutcome{Status: "completed", Continue: true}, nil
	})
	if err != nil || !reflect.DeepEqual(order, []string{"security_rules", "security_ai"}) {
		t.Fatalf("resume replayed prior work: %v %v", order, err)
	}
}

func TestAnalysisGraphCancellationJoinsHost(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	started, exited, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := RunAnalysisGraph(ctx, analysisGraphFixture(), func(ctx context.Context, _ AnalysisWork) (AnalysisOutcome, error) {
			close(started)
			<-ctx.Done()
			close(exited)
			return AnalysisOutcome{}, context.Cause(ctx)
		})
		done <- err
	}()
	<-started
	CancelRun(ctx, context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("graph left its host running")
	}
}

func TestAnalysisGraphJournalFailureStopsBeforeNextStage(t *testing.T) {
	root := t.TempDir()
	ctx, _, _ := persistedContext(t, root)
	calls := 0
	_, err := RunAnalysisGraph(ctx, analysisGraphFixture(), func(context.Context, AnalysisWork) (AnalysisOutcome, error) {
		calls++
		journal := filepath.Join(root, ".kasuri", "agents")
		if err := os.Rename(journal, filepath.Join(root, "saved")); err != nil {
			return AnalysisOutcome{}, err
		}
		if err := os.WriteFile(journal, []byte("blocked"), 0600); err != nil {
			return AnalysisOutcome{}, err
		}
		return AnalysisOutcome{Status: "completed", Continue: true}, nil
	})
	if calls != 1 || !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) {
		t.Fatalf("journal failure continued: calls=%d err=%v", calls, err)
	}
}
