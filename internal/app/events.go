package app

import (
	"context"
	"sync"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentruntime"
	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
)

// Event tells clients what changed. Kind names the area; change workflow
// events also name the task, its workflow, the stage that moved and the
// workflow status, and carry the run's usage and estimated cost so far.
type Event struct {
	Kind       string                  `json:"kind"`
	SessionID  string                  `json:"session_id,omitempty"`
	WorkflowID string                  `json:"workflow_id,omitempty"`
	Title      string                  `json:"title,omitempty"`
	Stage      string                  `json:"stage,omitempty"`
	Status     string                  `json:"status,omitempty"`
	Usage      *agentstate.BudgetUsage `json:"usage,omitempty"`
	Cost       *AgentCostEstimate      `json:"cost,omitempty"`
}

type eventBroker struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

func newEventBroker() *eventBroker {
	return &eventBroker{
		subscribers: make(map[chan Event]struct{}),
	}
}

func (b *eventBroker) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	unsubscribe := func() {
		b.mu.Lock()
		delete(b.subscribers, ch)
		b.mu.Unlock()
	}
	return ch, unsubscribe
}

func (b *eventBroker) publish(event Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeEvents registers a listener channel for real-time project/workflow updates.
func (s *Service) SubscribeEvents() (<-chan Event, func()) {
	if s == nil || s.events == nil {
		ch := make(chan Event)
		return ch, func() {}
	}
	return s.events.subscribe()
}

func (s *Service) notifyEvent(kind string) {
	s.publishEvent(Event{Kind: kind})
}

// publishEvent tells this checkout's listeners. A task's progress in a
// worktree is also told to the active project's listeners, the only ones the
// Web UI has: without it, a task in its own worktree reached human review
// unnoticed until something else refreshed the page.
func (s *Service) publishEvent(event Event) {
	if s == nil {
		return
	}
	if s.events != nil {
		s.events.publish(event)
	}
	if s.owner != nil && event.Kind == "workflow" {
		s.owner.publishEvent(event)
	}
}

// notifyWorkflow reports a change workflow's progress on a task: the stage
// that moved (empty when the run ends), its status, and the run's usage.
func (s *Service) notifyWorkflow(ctx context.Context, session *ChangeSession, stage string) {
	event := Event{Kind: "workflow", SessionID: session.ID, Title: session.Title, Stage: stage}
	if w := session.Workflow; w != nil {
		event.WorkflowID, event.Status = w.ID, w.Status
	}
	if budget := agentruntime.BudgetFromContext(ctx); budget != nil {
		usage := budget.Snapshot()
		event.Usage, event.Cost = &usage, agentCost(usage, s.conf().agentRuntime.Telemetry.Prices)
	}
	s.publishEvent(event)
}
