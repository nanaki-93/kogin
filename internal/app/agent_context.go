package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

type preparedChangeContext struct {
	ctx      context.Context
	release  context.CancelFunc
	messages []llm.ChatMessage
	manifest project.ContextManifest
}

type changeContextComposition struct {
	Text     string                  `json:"text"`
	Manifest project.ContextManifest `json:"manifest"`
}

func (s *Service) prepareChangeContext(ctx context.Context, root string, session *ChangeSession, message string, runtime modelRuntime, stageZero bool) (prepared preparedChangeContext, err error) {
	ctx, err = s.freezeAgentSettings(ctx, root)
	if err != nil {
		return prepared, err
	}
	prepared.ctx, prepared.release = ctx, func() {}
	defer func() {
		if err != nil && prepared.release != nil {
			prepared.release()
		}
	}()
	settings := s.admittedRuntime(ctx).Context
	if settings.Compaction || settings.LocalCache || settings.ProviderCache {
		prepared.ctx, prepared.release, err = s.agentRunContext(ctx, runtime)
		if err != nil {
			return prepared, err
		}
		identity := ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}
		prepared.ctx = agentruntime.WithContextAuthority(prepared.ctx, func(current context.Context) error {
			_, currentRoot, err := s.loadChangeForAction(current, session.ID, identity)
			if err != nil {
				return err
			}
			if currentRoot != root {
				return project.ErrRevisionConflict
			}
			return nil
		})
	}
	composition, err := composedChangeContext(prepared.ctx, root, session, stageZero)
	if err != nil {
		return prepared, err
	}
	prepared.manifest = composition.Manifest
	text := composition.Text
	if brief := s.docsBrief(changeTargetPaths(session)); brief != "" && len(text)+len(brief) <= maxChangeBytes {
		text += brief
	}
	prepared.messages, err = compactChangeMessages(prepared.ctx, runtime, session, message, text, stageZero, settings.Compaction)
	if err == nil {
		err = validateChangeContextLimit(prepared.messages, runtime)
	}
	return prepared, err
}

func composedChangeContext(ctx context.Context, root string, session *ChangeSession, stageZero bool) (changeContextComposition, error) {
	var readTargets []ChangeTarget
	if session.Plan != nil {
		readTargets = session.Plan.ReadTargets
	}
	identity := []any{"change-composition-v2", session.ProjectID, session.ProjectRevision, session.WorkspaceHash, session.PolicyFingerprint, session.Targets, readTargets, session.Instructions, stageZero}
	data, err := agentruntime.CachedContext(ctx, identity, func() (json.RawMessage, error) {
		text, manifest, err := composeChangeStagePromptContext(root, session, stageZero)
		if err != nil {
			return nil, err
		}
		return json.Marshal(changeContextComposition{Text: text, Manifest: manifest})
	})
	if err != nil {
		return changeContextComposition{}, err
	}
	var composition changeContextComposition
	if err := json.Unmarshal(data, &composition); err != nil {
		return composition, fmt.Errorf("decode composed context: %w", err)
	}
	if len(composition.Text) > maxChangeBytes || len(composition.Manifest.Included) > 128 {
		return composition, fmt.Errorf("composed context exceeds its source limits")
	}
	return composition, nil
}

func compactChangeMessages(ctx context.Context, runtime modelRuntime, session *ChangeSession, message, text string, stageZero, enabled bool) ([]llm.ChatMessage, error) {
	if !enabled {
		return changeStageMessages(session, message, text, stageZero)
	}
	copy := *session
	copy.Messages = nil
	mandatory, err := changeStageMessages(&copy, message, text, stageZero)
	if err != nil {
		return nil, err
	}
	if err := validateChangeContextLimit(mandatory, runtime); err != nil {
		return nil, err
	}
	history := make([]llm.ChatMessage, len(session.Messages))
	for i, item := range session.Messages {
		history[i] = llm.ChatMessage{Role: item.Role, Content: item.Content}
	}
	identity := []any{"change-history-v1", session.ID, session.Revision, session.Hash, session.WorkspaceHash, session.Instructions, mandatory, changeResponseSchema()}
	history, err = agentruntime.CompactHistory(ctx, runtime.client, history, identity, changeContextByteLimit(runtime)/4)
	if err != nil {
		return nil, err
	}
	for _, item := range history {
		copy.Messages = append(copy.Messages, ChangeMessage{Role: item.Role, Content: item.Content})
	}
	return changeStageMessages(&copy, message, text, stageZero)
}

func validateChangeContextLimit(messages []llm.ChatMessage, runtime modelRuntime) error {
	bytes := 0
	for _, message := range messages {
		bytes += len(message.Content)
	}
	if limit := changeContextByteLimit(runtime); limit > 0 && bytes > limit {
		return fmt.Errorf("selected mandatory context and retained history exceed configured %s token limit", runtime.effective.Profile)
	}
	return nil
}
