package app

import (
	"context"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
)

// observeAgentRuns tells clients when an agent run's records change.
func (s *Service) observeAgentRuns(ctx context.Context) context.Context {
	return agentruntime.WithObserver(ctx, &agentruntime.Observer{Changed: func() { s.notifyEvent("agent_run") }})
}
