package agentruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
)

type traceParentKey struct{}

// ADK tool contexts deliberately do not support WithAgentContext. Decorate only
// Value so native tools keep their state/actions interface and cancellation.
type tracedAgentContext struct {
	agent.Context
	parent string
}

func (ctx *tracedAgentContext) Value(key any) any {
	if _, ok := key.(traceParentKey); ok {
		return ctx.parent
	}
	return ctx.Context.Value(key)
}

// Trace owns a local span's completion. It never installs global SDK telemetry
// providers or reads ambient exporter/resource configuration.
type Trace struct {
	once     sync.Once
	err      error
	p        *persistence
	span     agentstate.TraceSpan
	started  time.Time
	recorded bool
}

func TraceIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func StartTrace(ctx context.Context, kind, operation string) (context.Context, *Trace, error) {
	key := ""
	if operation != "" {
		key = TraceIdentity(operation)
	}
	return startTraceKey(ctx, kind, key)
}

func startTraceKey(ctx context.Context, kind, key string) (context.Context, *Trace, error) {
	trace, err := newTrace(ctx, kind, key)
	if err != nil || trace == nil {
		return ctx, nil, err
	}
	trace.recorded, err = trace.p.store.BeginSpan(ctx, trace.p.budgetID, trace.span)
	if err != nil {
		if !isPersistenceCancellation(err) {
			err = fmt.Errorf("%w: begin local trace: %w", agentstate.ErrPersistence, err)
			CancelRun(ctx, err)
		}
		return ctx, nil, err
	}
	child := context.WithValue(ctx, traceParentKey{}, trace.span.ID)
	if native, ok := ctx.(agent.Context); ok {
		child = &tracedAgentContext{Context: native, parent: trace.span.ID}
	}
	return child, trace, nil
}

func newTrace(ctx context.Context, kind, key string) (*Trace, error) {
	p, _ := ctx.Value(persistenceKey{}).(*persistence)
	if p == nil {
		return nil, nil
	}
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return nil, fmt.Errorf("create trace identity: %w", err)
	}
	started := time.Now()
	span := agentstate.TraceSpan{ID: hex.EncodeToString(bytes[:]), Kind: kind, OperationKey: key, Status: "running", Started: started.UTC()}
	span.ParentID, _ = ctx.Value(traceParentKey{}).(string)
	if run, _ := ctx.Value(invocationKey{}).(string); run != "" {
		span.RunKey = TraceIdentity(run)
	}
	return &Trace{p: p, span: span, started: started}, nil
}

func (trace *Trace) beginSpan() *agentstate.TraceSpan {
	if trace == nil {
		return nil
	}
	span := trace.span
	return &span
}

func (trace *Trace) completedSpan(ctx context.Context, workErr error) *agentstate.TraceSpan {
	if trace == nil || !trace.recorded {
		return nil
	}
	span := trace.span
	elapsed := max(time.Duration(0), time.Since(trace.started))
	span.Status, span.Ended, span.ElapsedNanos = traceStatus(ctx, workErr), trace.started.Add(elapsed).UTC(), elapsed.Nanoseconds()
	return &span
}

func traceStatus(ctx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return "timed_out"
	}
	if errors.Is(err, context.Canceled) || errors.Is(context.Cause(ctx), context.Canceled) {
		return "canceled"
	}
	return "failed"
}

// Finish remains owned by the invoking operation, including cancellation. Its
// bounded cleanup cannot leak a goroutine or replace the original work error.
func (trace *Trace) Finish(ctx context.Context, workErr error) error {
	return errors.Join(workErr, trace.finish(ctx, workErr))
}

func (trace *Trace) finish(ctx context.Context, workErr error) error {
	if trace == nil || !trace.recorded {
		return nil
	}
	trace.once.Do(func() {
		span := trace.completedSpan(ctx, workErr)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := trace.p.store.FinishSpan(cleanup, trace.p.budgetID, span.ID, span.Status, span.Ended, span.ElapsedNanos); err != nil {
			trace.err = fmt.Errorf("%w: finish local trace: %w", agentstate.ErrPersistence, err)
			CancelRun(ctx, trace.err)
		}
	})
	return trace.err
}

func tracedHost[T any](ctx context.Context, kind, operation string, work func(context.Context) (T, error)) (result T, err error) {
	ctx, trace, err := StartTrace(ctx, kind, operation)
	if err != nil {
		return result, err
	}
	defer func() { err = trace.Finish(ctx, err) }()
	return work(ctx)
}

func (s *runtimeSession) beginTracedRun(ctx context.Context, run agentstate.Run) error {
	kind := "invocation"
	if run.SchemaVersion == "delegation-v1" {
		kind = "delegation"
	}
	ctx = context.WithValue(ctx, invocationKey{}, s.runID)
	trace, err := newTrace(ctx, kind, TraceIdentity(run.SchemaVersion))
	if err != nil {
		return err
	}
	trace.recorded, err = s.persistence.store.BeginRun(ctx, run, trace.beginSpan())
	s.trace = trace
	return err
}

func (r *modelReservation) completedTraceSpan(completed bool, settlementErr error) *agentstate.TraceSpan {
	workErr := settlementErr
	if !completed && workErr == nil {
		workErr = fmt.Errorf("provider attempt did not complete")
	}
	return r.trace.completedSpan(r.ctx, workErr)
}
