package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

func TestRepairGraphPersistsConditionalTransitionsAndSharedAdmission(t *testing.T) {
	for limit := 0; limit <= 2; limit++ {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, budget, store := persistedContext(t, t.TempDir())
			calls := 0
			err := RunRepairGraph(ctx, RepairGraphInput{Snapshot: changeGraphFixture().Snapshot, MaxRepairs: limit}, func(ctx context.Context, attempt int) (bool, error) {
				records, err := store.Sessions(ctx, agentstate.StateApp, "local")
				if err != nil || len(records) != 1 || len(records[0].Events) != attempt*2+1 {
					return false, fmt.Errorf("attempt started before durable progress: %v", err)
				}
				var event struct{ CustomMetadata map[string]any }
				if err := json.Unmarshal(records[0].Events[attempt*2].Data, &event); err != nil {
					return false, err
				}
				if event.CustomMetadata["mini_orca.attempt"] != float64(attempt) || budget.Snapshot().Repairs != attempt {
					return false, fmt.Errorf("attempt bypassed shared repair admission")
				}
				calls++
				return attempt < limit, nil
			})
			if err != nil || calls != limit+1 || budget.Snapshot().Repairs != limit {
				t.Fatalf("repair routing: calls=%d usage=%+v error=%v", calls, budget.Snapshot(), err)
			}
		})
	}
}

func TestRepairGraphStopsOnHostErrorsAndNeverExceedsSharedLimit(t *testing.T) {
	sentinel := errors.New("authority lost")
	for _, fail := range []bool{false, true} {
		ctx, budget := budgetContext(t, func(limits *config.AgentLimits) { limits.Repairs = 1 })
		calls := 0
		err := RunRepairGraph(ctx, RepairGraphInput{Snapshot: changeGraphFixture().Snapshot, MaxRepairs: 2}, func(context.Context, int) (bool, error) {
			calls++
			if fail {
				return true, sentinel
			}
			return true, nil
		})
		if fail {
			if calls != 1 || !errors.Is(err, sentinel) || budget.Snapshot().Repairs != 0 {
				t.Fatalf("host error was retried: %d %v", calls, err)
			}
		} else if calls != 2 || !errors.Is(err, ErrBudgetExceeded) || budget.Snapshot().Repairs != 1 {
			t.Fatalf("repair budget was replenished: %d %v", calls, err)
		}
	}
}

func TestRepairGraphPersistenceFailurePreventsNextAttempt(t *testing.T) {
	root := t.TempDir()
	ctx, budget, _ := persistedContext(t, root)
	calls := 0
	err := RunRepairGraph(ctx, RepairGraphInput{Snapshot: changeGraphFixture().Snapshot, MaxRepairs: 2}, func(context.Context, int) (bool, error) {
		calls++
		if err := os.Rename(filepath.Join(root, ".kogin", "agents"), filepath.Join(root, "saved-agents")); err != nil {
			return false, err
		}
		return true, os.WriteFile(filepath.Join(root, ".kogin", "agents"), []byte("blocked"), 0600)
	})
	if !errors.Is(err, agentstate.ErrPersistence) || calls != 1 || budget.Snapshot().Repairs != 0 {
		t.Fatalf("failed checkpoint dispatched a repair: %d %v", calls, err)
	}
}

func TestRepairGraphRejectsUnboundedAdmissionAndHonorsCancellation(t *testing.T) {
	for _, limit := range []int{-1, 3} {
		if err := RunRepairGraph(t.Context(), RepairGraphInput{Snapshot: changeGraphFixture().Snapshot, MaxRepairs: limit}, func(context.Context, int) (bool, error) {
			t.Fatal("invalid admission dispatched")
			return false, nil
		}); err == nil {
			t.Fatal("invalid repair limit accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	err := RunRepairGraph(ctx, RepairGraphInput{Snapshot: changeGraphFixture().Snapshot, MaxRepairs: 2}, func(context.Context, int) (bool, error) {
		calls++
		cancel()
		return true, nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("canceled attempt continued: %d %v", calls, err)
	}
}
