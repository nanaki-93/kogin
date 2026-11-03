package app

import (
	"context"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
)

// All scoped model work enters the same ADK runner; retries and publication
// remain with the host that admitted the request.
func (r modelRuntime) execute(ctx context.Context, request agentruntime.Request) (modelOutput, error) {
	if r.client == nil {
		return modelOutput{}, fmt.Errorf("model client is not configured")
	}
	return agentruntime.Complete(ctx, r.client, request)
}

// scopedRuntimes owns the immutable runtime selected for each fixed workflow
// scope. Runtime profiles are established during service construction.
type scopedRuntimes struct {
	analyze  modelRuntime
	bug      modelRuntime
	function modelRuntime
}

func (r scopedRuntimes) forScope(scope string) (modelRuntime, bool) {
	switch scope {
	case r.analyze.effective.Scope:
		return r.analyze, true
	case r.bug.effective.Scope:
		return r.bug, true
	case r.function.effective.Scope:
		return r.function, true
	default:
		return modelRuntime{}, false
	}
}
