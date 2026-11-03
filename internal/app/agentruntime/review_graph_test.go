package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
)

func reviewGraphFixture() ReviewGraphInput {
	return ReviewGraphInput{Snapshot: changeGraphFixture().Snapshot, Specialists: []string{"correctness", "architecture", "test_quality"}, Concurrency: 2}
}

func approvedSpecialist(role string, snapshot ChangeSnapshot) ReviewNodeResult {
	return ReviewNodeResult{Specialist: role, Snapshot: snapshot, Verdict: "approve"}
}

func TestReviewGraphBoundsParallelWorkAndJoinsInCanonicalOrder(t *testing.T) {
	ctx, _, store := persistedContext(t, t.TempDir())
	input := reviewGraphFixture()
	entered, release := make(chan string, 3), make(chan struct{}, 3)
	finished := make(chan ReviewGraphResult, 1)
	failure := make(chan error, 1)
	go func() {
		result, err := RunReviewGraph(ctx, input, func(ctx context.Context, role string, snapshot ChangeSnapshot) (ReviewNodeResult, error) {
			records, err := store.Sessions(ctx, agentstate.StateApp, "local")
			if err != nil || len(records) != 1 {
				return ReviewNodeResult{}, fmt.Errorf("missing durable review admission: %v", err)
			}
			found := false
			for _, entry := range records[0].Events {
				var event struct{ CustomMetadata map[string]any }
				if err := json.Unmarshal(entry.Data, &event); err != nil {
					return ReviewNodeResult{}, err
				}
				found = found || event.CustomMetadata["mini_orca.review_node"] == role
			}
			if !found || snapshot != input.Snapshot {
				return ReviewNodeResult{}, fmt.Errorf("specialist started without durable matching identity")
			}
			entered <- role
			select {
			case <-release:
			case <-ctx.Done():
				return ReviewNodeResult{}, context.Cause(ctx)
			}
			result := approvedSpecialist(role, snapshot)
			if role == "architecture" {
				result.Verdict, result.Findings = "changes_requested", 1
			}
			return result, nil
		})
		finished <- result
		failure <- err
	}()
	for range 2 {
		<-entered
	}
	select {
	case role := <-entered:
		t.Fatalf("concurrency limit exceeded by %s", role)
	default:
	}
	release <- struct{}{}
	<-entered
	release <- struct{}{}
	release <- struct{}{}
	result, err := <-finished, <-failure
	if err != nil || result.Verdict != "changes_requested" {
		t.Fatalf("join: %+v %v", result, err)
	}
	var order []string
	for _, review := range result.Reviews {
		order = append(order, review.Specialist)
	}
	if !reflect.DeepEqual(order, input.Specialists) {
		t.Fatalf("completion timing changed join order: %v", order)
	}
}

func TestReviewGraphFailureDrainsSiblingsAndPublishesNoJoin(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	input := reviewGraphFixture()
	input.Concurrency = 3
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	done := make(chan error, 1)
	var exits atomic.Int32
	sentinel := errors.New("review unavailable")
	go func() {
		result, err := RunReviewGraph(ctx, input, func(ctx context.Context, role string, snapshot ChangeSnapshot) (ReviewNodeResult, error) {
			defer exits.Add(1)
			entered <- struct{}{}
			if role == "correctness" {
				<-release
				return ReviewNodeResult{}, sentinel
			}
			<-ctx.Done()
			return ReviewNodeResult{}, context.Cause(ctx)
		})
		if len(result.Reviews) != 0 || result.Verdict != "" {
			err = fmt.Errorf("incomplete reviewers published an aggregate: %+v", result)
		}
		done <- err
	}()
	for range 3 {
		<-entered
	}
	close(release)
	if err := <-done; !errors.Is(err, sentinel) || errors.Is(err, context.Canceled) || exits.Load() != 3 {
		t.Fatalf("review failure leaked siblings or lost cause: %v exits=%d", err, exits.Load())
	}
}

func TestReviewGraphRejectsChangedIdentityAndIncompleteJoin(t *testing.T) {
	ctx, _ := budgetContext(t, nil)
	input := reviewGraphFixture()
	input.Concurrency = 1
	calls := 0
	_, err := RunReviewGraph(ctx, input, func(_ context.Context, role string, snapshot ChangeSnapshot) (ReviewNodeResult, error) {
		calls++
		snapshot.Hash = "different"
		return approvedSpecialist(role, snapshot), nil
	})
	if !errors.Is(err, ErrPolicyDenied) || calls != 1 {
		t.Fatalf("changed identity continued: calls=%d %v", calls, err)
	}
	if _, err := joinReviewResults(input, map[string]any{}); err == nil {
		t.Fatal("missing reviews became approval")
	}
}

func TestReviewGraphValidatesRoutingAndSingleReviewer(t *testing.T) {
	for _, roles := range [][]string{nil, {"unknown"}, {"correctness", "correctness"}, {"performance", "correctness"}} {
		input := reviewGraphFixture()
		input.Specialists = roles
		_, err := RunReviewGraph(t.Context(), input, func(context.Context, string, ChangeSnapshot) (ReviewNodeResult, error) {
			t.Fatal("invalid routing dispatched")
			return ReviewNodeResult{}, nil
		})
		if err == nil {
			t.Fatalf("accepted invalid roles: %v", roles)
		}
	}
	for _, roles := range [][]string{{"correctness"}, reviewRoles} {
		input := reviewGraphFixture()
		input.Specialists = roles
		result, err := RunReviewGraph(t.Context(), input, func(_ context.Context, role string, snapshot ChangeSnapshot) (ReviewNodeResult, error) {
			return approvedSpecialist(role, snapshot), nil
		})
		if err != nil || len(result.Reviews) != len(roles) || result.Verdict != "approve" {
			t.Fatalf("review routes %v: %+v %v", roles, result, err)
		}
	}
}
