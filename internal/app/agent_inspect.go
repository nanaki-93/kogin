package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

type AgentInspectionOptions struct {
	Root, RunID, ArtifactID string
	Private                 bool
	Limit                   int
}

type AgentInspectionRun struct {
	ID              string                 `json:"id"`
	ParentID        string                 `json:"parent_id,omitempty"`
	ProjectID       string                 `json:"project_id"`
	ProjectRevision string                 `json:"project_revision"`
	Schema          string                 `json:"schema"`
	State           string                 `json:"state"`
	Started         time.Time              `json:"started"`
	Updated         time.Time              `json:"updated"`
	Usage           agentstate.BudgetUsage `json:"usage"`
}

type AgentInspectionArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Hash      string `json:"hash"`
	Size      int    `json:"size"`
	MIME      string `json:"mime"`
	InUse     bool   `json:"in_use"`
	Protected bool   `json:"protected"`
}

type AgentInspectionContent struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Hash    string `json:"hash"`
	MIME    string `json:"mime"`
	Data    []byte `json:"data_base64"`
}

type AgentInspection struct {
	Epoch              string                     `json:"epoch"`
	Sequence           uint64                     `json:"sequence"`
	AvailableRuns      int                        `json:"available_runs"`
	AvailableArtifacts int                        `json:"available_artifacts"`
	Truncated          bool                       `json:"truncated"`
	Runs               []AgentInspectionRun       `json:"runs"`
	Events             []agentstate.ProgressEvent `json:"events"`
	Artifacts          []AgentInspectionArtifact  `json:"artifacts"`
	PrivateSession     *agentstate.Session        `json:"private_session,omitempty"`
	PrivateArtifact    *AgentInspectionContent    `json:"private_artifact,omitempty"`
}

// InspectAgentState reads through the journal owner. It never creates a running
// Service, repairs history, resumes checkpoints or constructs model providers.
func InspectAgentState(ctx context.Context, options AgentInspectionOptions) (*AgentInspection, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if err := validateInspectionOptions(options); err != nil {
		return nil, err
	}
	store, err := agentstate.Open(options.Root)
	if err != nil {
		return nil, err
	}
	history, err := store.History(ctx)
	if err != nil {
		return nil, err
	}
	artifacts, err := store.ArtifactInventory(ctx)
	if err != nil {
		return nil, err
	}
	result := &AgentInspection{Epoch: history.Epoch, Sequence: history.Sequence, AvailableRuns: len(history.Runs), AvailableArtifacts: len(artifacts), Runs: []AgentInspectionRun{}, Events: []agentstate.ProgressEvent{}, Artifacts: []AgentInspectionArtifact{}}
	if err := inspectRecordedRuns(ctx, store, history, options, result); err != nil {
		return nil, err
	}
	if err := inspectRecordedArtifacts(ctx, store, artifacts, options, result); err != nil {
		return nil, err
	}
	return result, context.Cause(ctx)
}

func validateInspectionOptions(o AgentInspectionOptions) error {
	if !filepath.IsAbs(o.Root) || o.Limit < 1 || o.Limit > 500 {
		return fmt.Errorf("inspection requires an absolute checkout root and a limit of 1–500 records")
	}
	if o.RunID != "" && o.ArtifactID != "" {
		return fmt.Errorf("inspect one selected invocation or artifact at a time")
	}
	if o.Private && o.RunID == "" && o.ArtifactID == "" {
		return fmt.Errorf("private inspection requires one exact invocation or artifact ID")
	}
	return nil
}

func inspectRecordedRuns(ctx context.Context, store *agentstate.Store, history agentstate.History, o AgentInspectionOptions, result *AgentInspection) error {
	selected := map[string]bool{}
	for _, snapshot := range history.Runs {
		if o.RunID != "" && snapshot.ID != o.RunID {
			continue
		}
		if len(result.Runs) >= o.Limit {
			result.Truncated = true
			continue
		}
		selected[snapshot.ID] = true
		result.Runs = append(result.Runs, AgentInspectionRun{ID: snapshot.ID, ParentID: snapshot.ParentID, ProjectID: snapshot.Binding.Project, ProjectRevision: snapshot.Binding.Revision, Schema: snapshot.SchemaVersion, State: snapshot.Status, Started: snapshot.Started, Updated: snapshot.Updated, Usage: snapshot.Usage})
		if o.Private && o.RunID != "" {
			var err error
			result.PrivateSession, err = store.Session(ctx, snapshot.Session)
			if err != nil {
				return err
			}
		}
	}
	if o.RunID != "" && !selected[o.RunID] {
		return agentstate.ErrNotFound
	}
	for _, event := range history.Events {
		if !selected[event.RunID] {
			continue
		}
		if len(result.Events) >= o.Limit {
			result.Truncated = true
			continue
		}
		result.Events = append(result.Events, event)
	}
	return nil
}

func inspectRecordedArtifacts(ctx context.Context, store *agentstate.Store, entries []agentstate.ArtifactInventoryEntry, o AgentInspectionOptions, result *AgentInspection) error {
	slices.SortFunc(entries, func(a, b agentstate.ArtifactInventoryEntry) int {
		if order := b.Created.Compare(a.Created); order != 0 {
			return order
		}
		return strings.Compare(artifactLibraryID(a.Artifact), artifactLibraryID(b.Artifact))
	})
	found := false
	for _, entry := range entries {
		id := artifactLibraryID(entry.Artifact)
		if o.ArtifactID != "" && id != o.ArtifactID {
			continue
		}
		if len(result.Artifacts) >= o.Limit {
			result.Truncated = true
			continue
		}
		found = true
		result.Artifacts = append(result.Artifacts, AgentInspectionArtifact{ID: id, Name: entry.Key.Name, Version: entry.Version, Hash: entry.Hash, Size: entry.Size, MIME: entry.MIME, InUse: entry.InUse, Protected: entry.Protected})
		if o.Private && o.ArtifactID != "" {
			artifact, err := store.ExportArtifact(ctx, entry.Key, entry.Version)
			if err != nil {
				return err
			}
			if artifact.Hash != entry.Hash {
				return agentstate.ErrArtifactConflict
			}
			result.PrivateArtifact = &AgentInspectionContent{ID: id, Version: artifact.Version, Hash: artifact.Hash, MIME: artifact.MIME, Data: artifact.Data}
		}
	}
	if o.ArtifactID != "" && !found {
		return agentstate.ErrNotFound
	}
	return nil
}

// artifactLibraryID is the stable, opaque ID of one recorded artifact version.
func artifactLibraryID(record agentstate.Artifact) string {
	key := record.Key
	digest := sha256.Sum256([]byte(strings.Join([]string{key.Session.App, key.Session.User, key.Session.ID, key.Name, strconv.FormatInt(record.Version, 10)}, "\x00")))
	return hex.EncodeToString(digest[:])
}
