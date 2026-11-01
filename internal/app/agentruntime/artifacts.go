package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
)

type localArtifactService struct {
	store      *agentstate.Store
	provenance map[string]string
}

var _ artifact.Service = (*localArtifactService)(nil)

func completionArtifacts(invocation *runtimeSession) artifact.Service {
	if invocation.persistence == nil {
		return artifact.InMemoryService()
	}
	p := invocation.persistence
	return &localArtifactService{store: p.store, provenance: map[string]string{
		"project": p.binding.Project, "revision": p.binding.Revision,
		"policy": p.binding.Policy, "configuration": p.binding.Configuration,
		"run": invocation.runID,
	}}
}

func artifactKey(app, user, sessionID, name string) agentstate.ArtifactKey {
	return agentstate.ArtifactKey{Session: agentstate.Key{App: app, User: user, ID: sessionID}, Name: name}
}

func artifactStorageError(err error) error {
	if err == nil {
		return nil
	}
	if isPersistenceCancellation(err) {
		return err
	}
	if errors.Is(err, agentstate.ErrNotFound) {
		return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
	}
	return fmt.Errorf("%w: %w", agentstate.ErrPersistence, err)
}

func (s *localArtifactService) Save(ctx context.Context, req *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("artifact save request is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	write, err := artifactPayload(req.Part)
	if err != nil {
		return nil, err
	}
	write.Key = artifactKey(req.AppName, req.UserID, req.SessionID, req.FileName)
	write.Version = req.Version
	write.Provenance = maps.Clone(s.provenance)
	write.Metadata = make(map[string]string, len(req.CustomMetadata))
	for key, value := range req.CustomMetadata {
		write.Metadata[key] = fmt.Sprint(value)
	}
	record, err := s.store.SaveArtifact(ctx, write)
	if err != nil {
		return nil, artifactStorageError(err)
	}
	return &artifact.SaveResponse{Version: record.Version}, nil
}

func artifactPayload(part *genai.Part) (agentstate.ArtifactWrite, error) {
	if err := onlyModelFields(part, "text", "inlineData"); err != nil {
		return agentstate.ArtifactWrite{}, fmt.Errorf("artifact must contain only text or inline bytes: %w", err)
	}
	if part.InlineData != nil {
		if part.Text != "" {
			return agentstate.ArtifactWrite{}, fmt.Errorf("artifact cannot combine text and binary content")
		}
		blob := part.InlineData
		return agentstate.ArtifactWrite{Kind: "inline", MIME: blob.MIMEType, DisplayName: blob.DisplayName, Data: blob.Data}, nil
	}
	return agentstate.ArtifactWrite{Kind: "text", MIME: "text/plain", Data: []byte(part.Text)}, nil
}

func (s *localArtifactService) Load(ctx context.Context, req *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("artifact load request is required")
	}
	record, err := s.store.ExportArtifact(ctx, artifactKey(req.AppName, req.UserID, req.SessionID, req.FileName), req.Version)
	if err != nil {
		return nil, artifactStorageError(err)
	}
	part := &genai.Part{Text: string(record.Data)}
	if record.Kind == "inline" {
		part = &genai.Part{InlineData: &genai.Blob{Data: record.Data, MIMEType: record.MIME, DisplayName: record.DisplayName}}
	}
	return &artifact.LoadResponse{Part: part}, nil
}

func (s *localArtifactService) List(ctx context.Context, req *artifact.ListRequest) (*artifact.ListResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("artifact list request is required")
	}
	records, err := s.store.ArtifactVersions(ctx, artifactKey(req.AppName, req.UserID, req.SessionID, ""))
	if err != nil {
		return nil, artifactStorageError(err)
	}
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.Key.Name)
	}
	return &artifact.ListResponse{FileNames: slices.Compact(names)}, nil
}

func (s *localArtifactService) Versions(ctx context.Context, req *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("artifact versions request is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	records, err := s.store.ArtifactVersions(ctx, artifactKey(req.AppName, req.UserID, req.SessionID, req.FileName))
	if err != nil {
		return nil, artifactStorageError(err)
	}
	versions := make([]int64, 0, len(records))
	for _, record := range records {
		versions = append(versions, record.Version)
	}
	return &artifact.VersionsResponse{Versions: versions}, nil
}

func (s *localArtifactService) GetArtifactVersion(ctx context.Context, req *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("artifact metadata request is required")
	}
	record, err := s.store.ExportArtifact(ctx, artifactKey(req.AppName, req.UserID, req.SessionID, req.FileName), req.Version)
	if err != nil {
		return nil, artifactStorageError(err)
	}
	metadata := make(map[string]any, len(record.Metadata))
	for key, value := range record.Metadata {
		metadata[key] = value
	}
	return &artifact.GetArtifactVersionResponse{ArtifactVersion: &artifact.ArtifactVersion{Version: record.Version, CanonicalURI: s.store.ArtifactURI(*record), CustomMetadata: metadata, CreateTime: record.Created, MimeType: record.MIME}}, nil
}

func (s *localArtifactService) Delete(ctx context.Context, req *artifact.DeleteRequest) error {
	if req == nil {
		return fmt.Errorf("artifact delete request is required")
	}
	return artifactStorageError(s.store.DeleteArtifacts(ctx, artifactKey(req.AppName, req.UserID, req.SessionID, req.FileName), agentstate.ArtifactRetention{Version: req.Version}))
}

func newArtifactPlugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{Name: "kasuri_completion_artifacts", OnEventCallback: saveCompletionArtifact})
}

func saveCompletionArtifact(ctx agent.InvocationContext, event *session.Event) (*session.Event, error) {
	if event == nil || event.Partial || !event.TurnComplete {
		return nil, nil
	}
	result, ok := event.Output.(Result)
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode completion artifact: %w", err)
	}
	saved, err := ctx.Artifacts().Save(ctx, "completion.json", &genai.Part{InlineData: &genai.Blob{Data: data, MIMEType: "application/json"}})
	if err != nil {
		return nil, fmt.Errorf("save completion artifact: %w", err)
	}
	event.Actions.ArtifactDelta = maps.Clone(event.Actions.ArtifactDelta)
	if event.Actions.ArtifactDelta == nil {
		event.Actions.ArtifactDelta = map[string]int64{}
	}
	event.Actions.ArtifactDelta["completion.json"] = saved.Version
	// ADK stops its callback chain on a non-nil replacement. Mutate this owned
	// event and continue so the policy plugin still observes and validates it.
	return nil, nil
}
