package agentruntime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

const ConfirmationSchema = "proposal-review-v1"

type ConfirmationInput struct {
	RequestID string         `json:"request_id"`
	Action    string         `json:"action"`
	Snapshot  ChangeSnapshot `json:"snapshot"`
	ScopeHash string         `json:"scope_hash"`
	ExpiresAt time.Time      `json:"expires_at"`
}

func validateConfirmation(input ConfirmationInput) error {
	if err := validateChangeSnapshot(input.Snapshot); err != nil {
		return err
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(input.ScopeHash, "sha256:"))
	if input.Action != "review_proposal" || err != nil || len(digest) != 32 || input.ExpiresAt.IsZero() {
		return fmt.Errorf("invalid proposal confirmation scope or action")
	}
	return validateGraphLabels(input.RequestID)
}

// RequestConfirmation persists native human-input history and then a waiting
// checkpoint. It returns without retaining a goroutine or the caller's budget.
func RequestConfirmation(ctx context.Context, input ConfirmationInput) (runID string, runErr error) {
	if err := validateConfirmation(input); err != nil {
		return "", err
	}
	if !input.ExpiresAt.After(time.Now()) || input.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return "", fmt.Errorf("confirmation must expire within 24 hours")
	}
	if p, _ := ctx.Value(persistenceKey{}).(*persistence); p == nil {
		return "", fmt.Errorf("confirmation requires durable admission")
	}
	invocation, err := createGraphSession(ctx, "confirmation", "confirmation-v1", input, ConfirmationSchema)
	if err != nil {
		return "", err
	}
	ctx = invocation.activate(ctx)
	defer func() {
		if runErr != nil {
			runErr = errors.Join(runErr, invocation.finish(ctx, runErr))
			runID = ""
		}
	}()
	graph, err := confirmationGraph(input)
	if err != nil {
		return "", err
	}
	worker, err := agent.New(agent.Config{Name: "proposal_confirmation", Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return graph.RunNode(agent.NewContext(ctx), nil)
	}})
	if err != nil {
		return "", err
	}
	runtime, err := confirmationRunner(invocation.service, worker)
	if err != nil {
		return "", err
	}
	checkpoint, err := collectInputRequest(ctx, input, runtime.Run(ctx, "local", invocation.session.ID(), nil, agent.RunConfig{}))
	if err != nil {
		return "", err
	}
	if err := invocation.persistence.store.PauseForInput(ctx, invocation.runID, checkpoint); err != nil {
		return "", sessionStorageError(err)
	}
	observeRunEnd(ctx, invocation.runID)
	return invocation.runID, nil
}

func collectInputRequest(ctx context.Context, input ConfirmationInput, events iter.Seq2[*session.Event, error]) (agentstate.InputCheckpoint, error) {
	var checkpoint agentstate.InputCheckpoint
	for event, err := range events {
		if err != nil {
			return checkpoint, err
		}
		if event == nil || event.RequestedInput == nil {
			continue
		}
		if checkpoint.RequestID != "" || event.RequestedInput.InterruptID != input.RequestID || event.InvocationID == "" {
			return checkpoint, fmt.Errorf("confirmation returned an unexpected input request")
		}
		checkpoint = agentstate.InputCheckpoint{RequestID: input.RequestID, NodeID: "human_review", InvocationID: event.InvocationID, ExpiresAt: input.ExpiresAt}
	}
	if err := context.Cause(ctx); err != nil {
		return checkpoint, err
	}
	if checkpoint.RequestID == "" {
		return checkpoint, fmt.Errorf("confirmation ended without requesting input")
	}
	return checkpoint, nil
}

type confirmationNode struct {
	workflow.BaseNode
	input ConfirmationInput
}

func confirmationGraph(input ConfirmationInput) (*workflow.Workflow, error) {
	rerun := false
	node := &confirmationNode{BaseNode: workflow.NewBaseNode("human_review", "Review the checked proposal", workflow.NodeConfig{RerunOnResume: &rerun}), input: input}
	return workflow.New("proposal_confirmation", []workflow.Edge{{From: workflow.Start, To: node}}, workflow.WithMaxConcurrency(1))
}

func (n *confirmationNode) Run(ctx agent.Context, _ any) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		schema := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"decision": {Type: "string", Enum: []any{"approve", "reject", "cancel", "expire"}}}, Required: []string{"decision"}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		yield(workflow.NewRequestInputEvent(ctx, session.RequestInput{InterruptID: n.input.RequestID, Message: "Review the checked proposal. Apply remains a separate action.", Payload: n.input, ResponseSchema: schema}), nil)
	}
}

func confirmationRunner(service session.Service, worker agent.Agent) (*runner.Runner, error) {
	return runner.New(runner.Config{AppName: "kasuri", Agent: worker, SessionService: service})
}

// ResumeConfirmation only resolves a pure handoff node. There are no model,
// tool, filesystem or Git callbacks to replay, even after a partial failure.
// The host must separately revalidate its domain authority before calling it.
func ResumeConfirmation(ctx context.Context, store *agentstate.Store, binding agentstate.Binding, runID string, input ConfirmationInput, decision string) (agentstate.InputCheckpoint, error) {
	if store == nil || binding.Validate() != nil {
		return agentstate.InputCheckpoint{}, fmt.Errorf("confirmation resume requires current admission")
	}
	if err := validateConfirmation(input); err != nil {
		return agentstate.InputCheckpoint{}, err
	}
	hash, err := fingerprint(input)
	if err != nil {
		return agentstate.InputCheckpoint{}, err
	}
	admission := agentstate.InputAdmission{RunID: runID, RequestID: input.RequestID, RequestHash: hash, Binding: binding}
	if err := validateConfirmationRun(ctx, store, admission); err != nil {
		return agentstate.InputCheckpoint{}, err
	}
	run, err := store.ReceiveInput(ctx, admission, decision)
	if err != nil {
		return agentstate.InputCheckpoint{}, err
	}
	if run.Status != "waiting_input" {
		return *run.Input, nil
	}
	if err := resumeConfirmationGraph(ctx, store, run, input); err != nil {
		return agentstate.InputCheckpoint{}, err
	}
	if err := store.FinishInput(ctx, admission); err != nil {
		return agentstate.InputCheckpoint{}, sessionStorageError(err)
	}
	return *run.Input, nil
}

func validateConfirmationRun(ctx context.Context, store *agentstate.Store, admission agentstate.InputAdmission) error {
	run, err := store.InputRun(ctx, admission)
	if err != nil {
		return err
	}
	schemaHash, err := fingerprint([]any{"confirmation-v1", ConfirmationSchema})
	if err != nil {
		return err
	}
	if run.SchemaVersion != "confirmation-v1" || run.SchemaHash != schemaHash {
		return fmt.Errorf("confirmation checkpoint schema changed; create a fresh request")
	}
	return nil
}

func resumeConfirmationGraph(ctx context.Context, store *agentstate.Store, run agentstate.Run, input ConfirmationInput) error {
	graph, err := confirmationGraph(input)
	if err != nil {
		return err
	}
	service := &localSessionService{store: store}
	stored, err := service.Get(ctx, &session.GetRequest{AppName: run.Session.App, UserID: run.Session.User, SessionID: run.Session.ID})
	if err != nil {
		return err
	}
	state, err := graph.ReconstructRunState(stored.Session, run.Input.InvocationID)
	if err != nil {
		return fmt.Errorf("reconstruct confirmation checkpoint: %w", err)
	}
	if state == nil || state.Nodes["human_review"] == nil {
		return fmt.Errorf("confirmation journal has no waiting node")
	}
	if state.Nodes["human_review"].Status == workflow.NodeCompleted {
		return nil // Receipt survived a crash after the native response was saved.
	}
	return runConfirmationResume(ctx, service, graph, state, run)
}

func runConfirmationResume(ctx context.Context, service session.Service, graph *workflow.Workflow, state *workflow.RunState, run agentstate.Run) error {
	response := map[string]any{"decision": run.Input.Decision}
	worker, err := agent.New(agent.Config{Name: "proposal_confirmation", Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return graph.Resume(agent.NewContext(ctx), state, map[string]any{run.Input.RequestID: response})
	}})
	if err != nil {
		return err
	}
	runtime, err := confirmationRunner(service, worker)
	if err != nil {
		return err
	}
	message := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: run.Input.RequestID, Name: workflow.WorkflowInputFunctionCallName, Response: response}}}}
	for _, err := range runtime.Run(ctx, run.Session.User, run.Session.ID, message, agent.RunConfig{}) {
		if err != nil {
			return err
		}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if state.Nodes["human_review"].Status != workflow.NodeCompleted {
		return fmt.Errorf("confirmation resume did not complete its handoff")
	}
	return nil
}
