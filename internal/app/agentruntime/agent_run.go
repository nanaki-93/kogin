package agentruntime

import (
	"context"
	"errors"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

// SupportsAgentRuns reports whether the model can run as a tool-using agent
// inside its CLI.
func (c *ProviderClient) SupportsAgentRuns() bool {
	return c.compatible != nil && c.compatible.SupportsAgentRuns()
}

// RunAgent runs a tool-using agent and charges every model turn it took to
// the run's budget, also when the run failed after using the model.
func (c *ProviderClient) RunAgent(ctx context.Context, run llm.AgentRun) (*llm.AgentRunResult, error) {
	if !c.SupportsAgentRuns() {
		return nil, llm.ErrAgentRunsUnsupported
	}
	reservation, err := c.reserveDispatch(ctx, struct{ System, Prompt string }{run.System, run.Prompt})
	if err != nil {
		return nil, err
	}
	result, err := c.compatible.RunAgent(ctx, run)
	if result == nil {
		return nil, errors.Join(err, reservation.settle(0, 0, false, false))
	}
	return result, errors.Join(err, reservation.settleTurns(result, err == nil))
}

// settleTurns settles a reservation that covered every turn of one agent
// run: the turns after the first count as model calls of the same model.
func (r *modelReservation) settleTurns(result *llm.AgentRunResult, completed bool) error {
	if r == nil {
		return nil
	}
	if extra := result.Turns - 1; extra > 0 {
		r.budget.mu.Lock()
		r.budget.usage.ModelCalls += extra
		r.budget.usage.Accounting.Providers[r.group].ModelCalls += extra
		r.budget.mu.Unlock()
	}
	if !result.UsageKnown {
		return r.settle(0, 0, false, completed)
	}
	receipt, err := chatTokenReceipt(&result.Usage)
	if err != nil {
		return errors.Join(err, r.settle(0, 0, false, completed))
	}
	if err := r.observeUsage(receipt); err != nil {
		return errors.Join(err, r.settle(0, 0, false, completed))
	}
	return r.settle(receipt.Input, receipt.Output, true, completed)
}
