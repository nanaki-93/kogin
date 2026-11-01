package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
)

// Graph inputs and outputs contain identity/progress only. Host-owned proposal
// stores and nested model sessions retain source-bearing evidence separately.
func runGraph[T any](ctx context.Context, name, version string, input, schema any, worker agent.Agent, collect func(context.Context, iter.Seq2[*session.Event, error]) (T, error), configs ...agent.RunConfig) (result T, runErr error) {
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	invocation, err := createGraphSession(ctx, name, version, input, schema)
	if err != nil {
		if errors.Is(err, agentstate.ErrPersistence) || errors.Is(err, ErrPolicyDenied) {
			CancelRun(ctx, err)
		}
		return result, err
	}
	ctx = invocation.activate(ctx)
	defer func() {
		runErr = errors.Join(runErr, invocation.finish(ctx, runErr))
		if errors.Is(runErr, agentstate.ErrPersistence) || errors.Is(runErr, ErrPolicyDenied) {
			CancelRun(ctx, runErr)
		}
		if runErr != nil {
			var empty T
			result = empty
		}
	}()
	policy, err := newPolicyPlugin()
	if err != nil {
		return result, err
	}
	runtime, err := runner.New(runner.Config{AppName: "kasuri", Agent: worker, SessionService: invocation.service, PluginConfig: runner.PluginConfig{Plugins: []*plugin.Plugin{policy}}})
	if err != nil {
		return result, fmt.Errorf("create %s runner: %w", name, err)
	}
	config := agent.RunConfig{}
	if len(configs) > 0 {
		config = configs[0]
	}
	return collect(ctx, runtime.Run(ctx, "local", invocation.session.ID(), nil, config))
}

func createGraphSession(ctx context.Context, name, version string, input, schema any) (*runtimeSession, error) {
	requestHash, err := fingerprint(input)
	if err != nil {
		return nil, err
	}
	schemaHash, err := fingerprint([]any{version, schema})
	if err != nil {
		return nil, err
	}
	return createRuntimeSession(withGraphScope(ctx, input), map[string]any{"mini_orca." + name: input}, version, requestHash, schemaHash, "")
}

func graphAgent(name string, edges []workflow.Edge, concurrency int, input any) (agent.Agent, error) {
	graph, err := workflow.New(name, edges, workflow.WithMaxConcurrency(concurrency))
	if err != nil {
		return nil, fmt.Errorf("construct %s graph: %w", name, err)
	}
	return agent.New(agent.Config{Name: name, Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return graph.RunNode(agent.NewContext(ctx), input)
	}})
}
