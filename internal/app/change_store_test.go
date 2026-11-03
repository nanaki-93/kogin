package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
)

func artifactChangeFixture(t *testing.T) (*Service, string, *ChangeSession) {
	t.Helper()
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	opened := openChangeFixture(t, s, "main.go")
	proposal, err := s.SendChangeMessage(t.Context(), opened.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(opened),
		Changes:        []ChangeEditInput{{Path: "main.go", Content: "package main\n\nfunc Run() {}\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, root, proposal
}

func TestChangeEvidenceArtifactsArePrivateBoundAndPassive(t *testing.T) {
	s, root, proposal := artifactChangeFixture(t)
	before, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	checked, err := s.CheckChange(t.Context(), proposal.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(proposal)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentstate.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	key := agentstate.ArtifactKey{Session: agentstate.Key{App: agentstate.ChangesApp, User: "local", ID: checked.ID}}
	records, err := store.ArtifactVersions(t.Context(), key)
	if err != nil || len(records) != 2 {
		t.Fatalf("evidence=%+v %v", records, err)
	}
	for _, record := range records {
		if !record.Protected || record.Provenance["proposal"] != checked.Hash || record.Provenance["project"] != checked.ProjectID {
			t.Fatalf("unbound evidence: %+v", record)
		}
		if err := store.DeleteArtifacts(t.Context(), record.Key, agentstate.ArtifactRetention{}); !errors.Is(err, agentstate.ErrArtifactInUse) {
			t.Fatalf("recovery evidence was deletable: %v", err)
		}
	}
	key.Name = "proposal.json"
	saved, err := store.ExportArtifact(t.Context(), key, 1)
	if err != nil {
		t.Fatal(err)
	}
	var edits []ChangeEdit
	if err := json.Unmarshal(saved.Data, &edits); err != nil || len(edits) != 1 || edits[0].Hash != checked.Changes[0].Hash {
		t.Fatal("proposal evidence changed", err)
	}
	if err := storeChangeSession(root, checked); err != nil {
		t.Fatal(err)
	}
	if versions, err := store.ArtifactVersions(t.Context(), key); err != nil || len(versions) != 1 {
		t.Fatal("unchanged proposal consumed another version", err)
	}
	paths, err := filepath.Glob(filepath.Join(root, ".kogin", "agents", "*", "artifacts.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing private catalog", err)
	}
	prior, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeSession(t.Context(), checked.ID); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(prior, after) {
		t.Fatal("inspection rewrote artifacts", err)
	}
	source, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil || !bytes.Equal(before, source) {
		t.Fatal("artifact storage changed project source", err)
	}
}

func TestChangeArtifactFailureDoesNotPublishChecks(t *testing.T) {
	s, root, proposal := artifactChangeFixture(t)
	paths, err := filepath.Glob(filepath.Join(root, ".kogin", "agents", "*", "artifacts.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("missing private catalog", err)
	}
	if err := os.Rename(paths[0], paths[0]+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths[0], 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckChange(t.Context(), proposal.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(proposal)}); !errors.Is(err, agentstate.ErrPersistence) {
		t.Fatalf("evidence failure hidden: %v", err)
	}
	read, err := loadChangeSession(root, proposal.ID)
	if err != nil || len(read.Checks) != 0 {
		t.Fatalf("failed evidence published checks: %+v %v", read, err)
	}
}
