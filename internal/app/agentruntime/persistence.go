package agentruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/adk/v2/session"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

type persistenceKey struct{}
type persistence struct {
	store    *agentstate.Store
	binding  agentstate.Binding
	budgetID string
}

// PersistenceBinding returns the journal binding the run was admitted with.
func PersistenceBinding(ctx context.Context) (agentstate.Binding, bool) {
	p, ok := ctx.Value(persistenceKey{}).(*persistence)
	if !ok {
		return agentstate.Binding{}, false
	}
	return p.binding, true
}

func WithPersistence(ctx context.Context, store *agentstate.Store, binding agentstate.Binding) (context.Context, error) {
	if previous, ok := ctx.Value(persistenceKey{}).(*persistence); ok {
		if previous.binding != binding {
			return nil, fmt.Errorf("%w: journal binding changed", ErrPolicyDenied)
		}
		return ctx, nil
	}
	if store == nil || binding.Validate() != nil {
		return nil, fmt.Errorf("valid agent persistence binding is required")
	}
	budget := BudgetFromContext(ctx)
	if budget == nil {
		return nil, fmt.Errorf("durable agent run requires a budget")
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.persist != nil {
		return nil, fmt.Errorf("agent budget already belongs to another journal")
	}
	p := &persistence{store: store, binding: binding, budgetID: rand.Text()}
	budget.persist = func(ctx context.Context, usage BudgetSnapshot, span *agentstate.TraceSpan) (bool, error) {
		next := budget.sequence + 1
		recorded, err := store.Checkpoint(ctx, agentstate.BudgetRecord{ID: p.budgetID, Binding: binding, Limits: budget.limits, Sequence: next, Usage: usage}, span)
		if err != nil {
			return false, err
		}
		budget.sequence = next
		return recorded, nil
	}
	if err := budget.checkpointLocked(ctx); err != nil {
		budget.cancel(err)
		return nil, err
	}
	return context.WithValue(ctx, persistenceKey{}, p), nil
}

func (b *Budget) checkpointLocked(ctx context.Context) error {
	_, err := b.checkpointTraceLocked(ctx, nil)
	return err
}

func (b *Budget) checkpointTraceLocked(ctx context.Context, span *agentstate.TraceSpan) (bool, error) {
	if b.persist == nil {
		return false, nil
	}
	usage := b.usage.Clone()
	usage.ElapsedMillis = time.Since(b.started).Milliseconds()
	recorded, err := b.persist(ctx, usage, span)
	if err != nil {
		if isPersistenceCancellation(err) {
			return false, err
		}
		return false, fmt.Errorf("%w: save shared budget: %w", agentstate.ErrPersistence, err)
	}
	return recorded, nil
}

type runtimeSession struct {
	trace       *Trace
	record      agentstate.Run
	service     session.Service
	session     session.Session
	runID       string
	persistence *persistence
}

func createCompletionSession(ctx context.Context, request Request, providerBinding string) (*runtimeSession, error) {
	initial := map[string]any{"mini_orca.request": request.Messages, "mini_orca.schema": request.Schema}
	schemaVersion := "completion-v1"
	if request.Investigation != nil {
		initial["mini_orca.tools"] = request.Investigation.descriptors()
		initial["mini_orca.scope"] = request.Investigation.scope
		initial["mini_orca.context_tokens"] = request.ContextMaxTokens
		schemaVersion = "investigation-v1"
	}
	requestHash, schemaHash, err := invocationIdentity(request, initial)
	if err != nil {
		return nil, err
	}
	return createRuntimeSession(ctx, initial, schemaVersion, requestHash, schemaHash, providerBinding)
}

func createRuntimeSession(ctx context.Context, initial map[string]any, schemaVersion, requestHash, schemaHash, providerBinding string) (*runtimeSession, error) {
	p, _ := ctx.Value(persistenceKey{}).(*persistence)
	var service session.Service = session.InMemoryService()
	if p != nil {
		service = &localSessionService{store: p.store}
	}
	created, err := service.Create(ctx, &session.CreateRequest{AppName: "mini-orca", UserID: "local", State: initial})
	if err != nil {
		return nil, fmt.Errorf("create runtime session: %w", err)
	}
	result := &runtimeSession{service: service, session: created.Session, persistence: p}
	if p == nil {
		return result, nil
	}
	result.runID = rand.Text()
	now := time.Now().UTC()
	run := agentstate.Run{ID: result.runID, Session: agentstate.Key{App: "mini-orca", User: "local", ID: created.Session.ID()}, BudgetID: p.budgetID, RequestHash: requestHash, SchemaHash: schemaHash, ProviderBinding: providerBinding, SchemaVersion: schemaVersion, Status: "running", Started: now, Updated: now}
	run.ParentID, _ = ctx.Value(invocationKey{}).(string)
	if scope, ok := ctx.Value(changeScopeKey{}).(changeRunScope); ok {
		run.ChangeSessionID, run.WorkflowID = scope.SessionID, scope.WorkflowID
	}
	if err := result.beginTracedRun(ctx, run); err != nil {
		if isPersistenceCancellation(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: begin invocation: %w", agentstate.ErrPersistence, err)
	}
	result.record = run
	return result, nil
}

func invocationIdentity(request Request, initial map[string]any) (string, string, error) {
	var input any = struct {
		Messages []llm.ChatMessage
		Schema   *llm.JSONSchema
	}{request.Messages, request.Schema}
	var schema any = request.Schema
	if request.Investigation != nil {
		input = initial
		schema = []any{request.Schema, initial["mini_orca.tools"], initial["mini_orca.scope"]}
	}
	requestHash, err := fingerprint(input)
	if err != nil {
		return "", "", err
	}
	schemaHash, err := fingerprint(schema)
	return requestHash, schemaHash, err
}

func fingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("fingerprint agent input: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *runtimeSession) finish(ctx context.Context, runErr error) error {
	defer observeRunEnd(ctx, s.runID)
	if s.persistence == nil {
		return nil
	}
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	if errors.Is(runErr, context.Canceled) {
		status = "canceled"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.persistence.store.FinishRun(cleanup, s.runID, status, s.trace.completedSpan(ctx, runErr)); err != nil {
		return fmt.Errorf("%w: finish invocation: %w", agentstate.ErrPersistence, err)
	}
	return nil
}
