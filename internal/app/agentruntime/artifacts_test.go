package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func TestLocalArtifactServiceContract(t *testing.T) {
	store, err := agentstate.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var service artifact.Service = &localArtifactService{store: store}
	parts := []*genai.Part{{Text: "private result"}, {InlineData: &genai.Blob{Data: []byte{0, 1, 2, 255}, MIMEType: "application/octet-stream", DisplayName: "fixture"}}}
	var firstURI string
	for i, part := range parts {
		saved, err := service.Save(t.Context(), &artifact.SaveRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result", Part: part, CustomMetadata: map[string]any{"number": i, "accepted": false}})
		if err != nil || saved.Version != int64(i+1) {
			t.Fatalf("save=%+v %v", saved, err)
		}
		loaded, err := service.Load(t.Context(), &artifact.LoadRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result", Version: saved.Version})
		if err != nil || !reflect.DeepEqual(loaded.Part, part) {
			t.Fatalf("part changed: %+v %v", loaded, err)
		}
		metadata, err := service.GetArtifactVersion(t.Context(), &artifact.GetArtifactVersionRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result"})
		if err != nil || metadata.ArtifactVersion.CustomMetadata["accepted"] != "false" || metadata.ArtifactVersion.CreateTime.IsZero() || metadata.ArtifactVersion.CanonicalURI == "" {
			t.Fatalf("metadata=%+v %v", metadata, err)
		}
		if i == 0 {
			firstURI = metadata.ArtifactVersion.CanonicalURI
		} else if metadata.ArtifactVersion.CanonicalURI == firstURI {
			t.Fatal("artifact versions share an identity")
		}
	}
	versions, err := service.Versions(t.Context(), &artifact.VersionsRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result"})
	if err != nil || !reflect.DeepEqual(versions.Versions, []int64{1, 2}) {
		t.Fatalf("versions=%+v %v", versions, err)
	}
	names, err := service.List(t.Context(), &artifact.ListRequest{AppName: "app", UserID: "user", SessionID: "session"})
	if err != nil || !reflect.DeepEqual(names.FileNames, []string{"result"}) {
		t.Fatalf("names=%+v %v", names, err)
	}
	if err := service.Delete(t.Context(), &artifact.DeleteRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result", Version: 2}); err != nil {
		t.Fatal(err)
	}
	latest, err := service.Load(t.Context(), &artifact.LoadRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result"})
	if err != nil || latest.Part.Text != parts[0].Text {
		t.Fatalf("latest retained version=%+v %v", latest, err)
	}
	if err := service.Delete(t.Context(), &artifact.DeleteRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Load(t.Context(), &artifact.LoadRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing artifact error=%v", err)
	}
	for _, part := range []*genai.Part{
		{FileData: &genai.FileData{FileURI: "https://example.invalid/private"}},
		{Text: "combined", InlineData: &genai.Blob{Data: []byte("bytes")}},
		{Text: "hidden instruction", FunctionCall: &genai.FunctionCall{Name: "execute"}},
	} {
		if _, err := service.Save(t.Context(), &artifact.SaveRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "rejected", Part: part}); err == nil {
			t.Fatal("unsupported artifact part was accepted")
		}
	}
}

func TestLocalArtifactCancellationDoesNotCancelItsParentRun(t *testing.T) {
	parent, _, store := persistedContext(t, t.TempDir())
	ctx, cancel := context.WithCancel(parent)
	cancel()
	s := &localArtifactService{store: store}
	_, err := s.Save(ctx, &artifact.SaveRequest{AppName: "app", UserID: "user", SessionID: "session", FileName: "result", Part: &genai.Part{Text: "result"}})
	if !errors.Is(err, context.Canceled) || errors.Is(err, agentstate.ErrPersistence) || parent.Err() != nil {
		t.Fatalf("artifact cancellation escalated to storage/root failure: %v", err)
	}
}

func TestCompletionArtifactPersistsEventReferenceAndPolicyMetadata(t *testing.T) {
	ctx, _, store := persistedContext(t, t.TempDir())
	client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
		return fixtureResponse("private result"), nil
	})
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Role: "user", Content: "request"}}})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Sessions(t.Context(), agentstate.StateApp, "local")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%d %v", len(sessions), err)
	}
	view, err := (&localSessionService{store: store}).Get(t.Context(), &session.GetRequest{AppName: agentstate.StateApp, UserID: "local", SessionID: sessions[0].Key.ID})
	if err != nil {
		t.Fatal(err)
	}
	event := view.Session.Events().At(0)
	if event.Actions.ArtifactDelta["completion.json"] != 1 || event.CustomMetadata["mini_orca.budget"] == nil {
		t.Fatalf("artifact or subsequent policy callback missing: %+v", event)
	}
	record, err := store.ExportArtifact(t.Context(), agentstate.ArtifactKey{Session: sessions[0].Key, Name: "completion.json"}, 1)
	if err != nil || record.Provenance["project"] != "project" || record.Provenance["run"] == "" {
		t.Fatalf("artifact provenance=%+v %v", record, err)
	}
	var saved Result
	if err := json.Unmarshal(record.Data, &saved); err != nil || saved != result {
		t.Fatalf("artifact differs from result: %+v %v", saved, err)
	}
}

func TestCompletionArtifactWriteFailurePreventsPublication(t *testing.T) {
	root := t.TempDir()
	ctx, _, store := persistedContext(t, root)
	paths, err := filepath.Glob(filepath.Join(root, ".kogin", "agents", "*", "journal.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal("journal missing", err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(paths[0]), "artifacts.json"), 0700); err != nil {
		t.Fatal(err)
	}
	client := completionFixture(func(context.Context, []llm.ChatMessage, *llm.JSONSchema) (*llm.ChatResponse, error) {
		return fixtureResponse("must not publish"), nil
	})
	result, err := Complete(ctx, client, Request{Messages: []llm.ChatMessage{{Content: "request"}}})
	if result != (Result{}) || !errors.Is(err, agentstate.ErrPersistence) || !errors.Is(context.Cause(ctx), agentstate.ErrPersistence) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	sessions, err := store.Sessions(t.Context(), agentstate.StateApp, "local")
	if err != nil || len(sessions) != 1 || len(sessions[0].Events) != 0 {
		t.Fatalf("failed artifact became a final event: %+v %v", sessions, err)
	}
}
