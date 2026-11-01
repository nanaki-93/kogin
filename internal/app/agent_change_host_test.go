package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
)

func TestCheckedChangeGraphPersistsOnlyIdentityAndRequiresLiveHandoffEvidence(t *testing.T) {
	var calls atomic.Int32
	s, root, initial := workflowService(t, [3]func() string{
		func() string { calls.Add(1); return workflowImplementation },
		func() string { calls.Add(1); return workflowTests },
		func() string { calls.Add(1); return workflowApproval },
	})
	if _, err := s.StartChangeWorkflow(t.Context(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, initial.ID)
	if ready.Workflow.Status != "awaiting_human_review" || ready.ReviewedHash != "" || calls.Load() != 5 {
		t.Fatalf("unexpected handoff: %+v", ready.Workflow)
	}
	store, _, err := s.agentJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	records, err := store.Sessions(t.Context(), "kasuri", "local")
	if err != nil {
		t.Fatal(err)
	}
	graphs := 0
	for _, record := range records {
		input := record.State["mini_orca.change_graph"]
		if input == nil {
			continue
		}
		graphs++
		var graph agentruntime.ChangeGraphInput
		if err := json.Unmarshal(input, &graph); err != nil || graph.Snapshot.WorkflowID != ready.Workflow.ID || graph.Start != agentruntime.ChangeCreate || graph.Models != [3]string{"bug", "analyze", "code"} {
			t.Fatalf("graph admission lost identity: %+v %v", graph, err)
		}
		var nodes []agentruntime.ChangeNode
		for _, entry := range record.Events {
			var event struct {
				Output *agentruntime.ChangeNodeResult
			}
			if err := json.Unmarshal(entry.Data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Output != nil {
				nodes = append(nodes, event.Output.Node)
			}
			if strings.Contains(string(entry.Data), "package main") || strings.Contains(string(entry.Data), "Helper") {
				t.Fatal("graph progress included source or task text")
			}
		}
		if !reflect.DeepEqual(nodes, []agentruntime.ChangeNode{agentruntime.ChangeCreate, agentruntime.ChangeTests, agentruntime.ChangeVerify, agentruntime.ChangeReview, agentruntime.ChangeHumanReview}) {
			t.Fatalf("missing graph node output: %v", nodes)
		}
	}
	if graphs != 1 {
		t.Fatalf("expected one graph, got %d", graphs)
	}
	job := &changeWorkflowJob{root: root, sessionID: ready.ID, id: ready.Workflow.ID, models: ready.Workflow.Models, specialists: workflowSpecialistIDs(ready.Workflow)}
	ctx := context.WithValue(t.Context(), changeWorkflowContextKey{}, job.id)
	input := agentruntime.ChangeNodeInput{Node: agentruntime.ChangeHumanReview, Snapshot: changeGraphSnapshot(ready), PriorVerdict: "approve"}
	for _, kind := range []string{"workflow", "proposal", "verdict", "authority"} {
		t.Run(kind, func(t *testing.T) {
			changed := input
			switch kind {
			case "workflow":
				changed.Snapshot.WorkflowID = "earlier"
			case "proposal":
				changed.Snapshot.Hash = "earlier"
			case "verdict":
				changed.PriorVerdict = "changes_requested"
			case "authority":
				delete(s.changeAuthority, ready.ProjectID+"/"+ready.ID)
			}
			if _, err := s.runChangeGraphNode(ctx, job, changed, ""); err == nil {
				t.Fatal("stale or historical evidence authorized handoff")
			}
		})
	}
	if calls.Load() != 5 {
		t.Fatal("handoff validation dispatched a provider")
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("graph applied source")
	}
}
