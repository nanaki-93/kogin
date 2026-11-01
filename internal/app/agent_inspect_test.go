package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func inspectorFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	key := agentstate.Key{App: "fixture", User: "owner", ID: "session"}
	if _, err := store.CreateSession(t.Context(), key, agentstate.State{"prompt": json.RawMessage(`"PRIVATE_PROMPT"`)}); err != nil {
		t.Fatal(err)
	}
	runtime, err := config.ResolveAgentRuntime(config.AgentRuntimeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	budget := agentstate.BudgetRecord{ID: "budget", Binding: agentstate.Binding{Project: "project", Revision: "revision", Policy: "policy", Configuration: "configuration"}, Limits: runtime.Limits, Sequence: 1}
	if _, err := store.Checkpoint(t.Context(), budget, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"parent", "child"} {
		run := agentstate.Run{ID: id, Session: key, BudgetID: budget.ID, RequestHash: strings.Repeat("a", 64), SchemaHash: strings.Repeat("b", 64), SchemaVersion: "completion-v1", Status: "running", Started: now, Updated: now}
		if id == "child" {
			run.ParentID = "parent"
		}
		if _, err := store.BeginRun(t.Context(), run, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"child", "parent"} {
		if err := store.FinishRun(t.Context(), id, "completed", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendEvent(t.Context(), key, agentstate.Event{ID: "private-event", Time: now, Data: json.RawMessage(`{"source":"PRIVATE_SOURCE"}`)}, nil); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.SaveArtifact(t.Context(), agentstate.ArtifactWrite{Key: agentstate.ArtifactKey{Session: key, Name: "evidence.txt"}, Kind: "text", MIME: "text/plain", Data: []byte("PRIVATE_ARTIFACT"), Metadata: map[string]string{"secret": "PRIVATE_METADATA"}})
	if err != nil {
		t.Fatal(err)
	}
	return root, artifactLibraryID(*artifact)
}

func inspectionFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[path] = string(data) + info.ModTime().String() + info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAgentInspectorMetadataPrivateSelectionAndNoWrites(t *testing.T) {
	root, artifactID := inspectorFixture(t)
	before := inspectionFiles(t, root)
	o := AgentInspectionOptions{Root: root, Limit: 100}
	result, err := InspectAgentState(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil || strings.Contains(string(data), "PRIVATE_") || len(result.Runs) != 2 || len(result.Artifacts) != 1 || result.Truncated {
		t.Fatal("metadata lost records or leaked content", string(data), err)
	}
	if result.Runs[0].ID != "child" || result.Runs[0].ParentID != "parent" {
		t.Fatal("ancestry lost", result.Runs)
	}
	o.Private = true
	if _, err := InspectAgentState(t.Context(), o); err == nil {
		t.Fatal("unselected private export")
	}
	o.RunID = "child"
	result, err = InspectAgentState(t.Context(), o)
	if err != nil || result.PrivateSession == nil || string(result.PrivateSession.State["prompt"]) != `"PRIVATE_PROMPT"` || result.PrivateArtifact != nil {
		t.Fatal("private run selection", result, err)
	}
	o.RunID, o.ArtifactID = "", artifactID
	result, err = InspectAgentState(t.Context(), o)
	if err != nil || result.PrivateArtifact == nil || string(result.PrivateArtifact.Data) != "PRIVATE_ARTIFACT" || result.PrivateSession != nil {
		t.Fatal("private artifact selection", result, err)
	}
	o.Private, o.ArtifactID, o.Limit = false, "", 1
	result, err = InspectAgentState(t.Context(), o)
	if err != nil || !result.Truncated || result.AvailableRuns != 2 || len(result.Runs) != 1 || len(result.Events) != 1 {
		t.Fatal("truncation hidden", result, err)
	}
	if !reflect.DeepEqual(before, inspectionFiles(t, root)) {
		t.Fatal("inspection mutated the journal")
	}
}

func TestAgentInspectorMissingCorruptCanceledAndInvalidSelection(t *testing.T) {
	root := t.TempDir()
	o := AgentInspectionOptions{Root: root, Limit: 100}
	result, err := InspectAgentState(t.Context(), o)
	if err != nil || len(result.Runs) != 0 || len(inspectionFiles(t, root)) != 0 {
		t.Fatal("empty inspection wrote metadata", err)
	}
	root, artifact := inspectorFixture(t)
	o.Root = root
	for _, bad := range []AgentInspectionOptions{{Root: root, Limit: 0}, {Root: ".", Limit: 100}, {Root: root, RunID: "child", ArtifactID: artifact, Limit: 100}, {Root: root, RunID: "missing", Limit: 100}, {Root: root, ArtifactID: "missing", Limit: 100}} {
		if _, err := InspectAgentState(t.Context(), bad); err == nil {
			t.Fatal("invalid selection admitted", bad)
		}
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	if _, err := InspectAgentState(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	files := inspectionFiles(t, root)
	for path := range files {
		if filepath.Ext(path) == ".json" {
			if err := os.WriteFile(path, []byte(`{"broken":`), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := inspectionFiles(t, root)
	if _, err := InspectAgentState(t.Context(), o); err == nil {
		t.Fatal("corrupt journal became empty success")
	}
	if !reflect.DeepEqual(before, inspectionFiles(t, root)) {
		t.Fatal("inspector repaired corrupt data")
	}
}
