package agentstate

import (
	"context"
	"sort"
	"sync"
	"time"
)

// The journal keeps every agent invocation's session, and a completion's
// session holds its whole request: without a bound the file grows with each
// analysis call (one analysis of a small project makes hundreds), every
// write rewrites it, and it reaches its size and record limits. Finished
// invocations that belong to no task workflow are dropped oldest first once
// the journal holds more than maxRetainedSessions sessions; the completions
// of an analysis still running go too, once they are pruneGrace old.
const (
	maxRetainedSessions        = 128
	retainedSessionsAfterPrune = 64
)

// pruneGrace keeps a just-finished invocation's session for whoever reads
// its result.
var pruneGrace = 10 * time.Minute

// pruneFinishedRuns drops finished invocations, oldest first, that belong
// to no task workflow, wait for no input, have no invocation left under
// them and finished more than pruneGrace ago, with their sessions, until at
// most retainedSessionsAfterPrune sessions remain. Budgets, traces and
// progress events stay: usage totals and history read them.
func pruneFinishedRuns(db *database) {
	if len(db.Sessions) <= maxRetainedSessions {
		return
	}
	cutoff := time.Now().Add(-pruneGrace)
	children := map[string]int{}
	sessionRuns := map[string]int{}
	var candidates []Run
	for _, run := range db.Runs {
		children[run.ParentID]++
		sessionRuns[run.Session.key()]++
		if prunableRun(run) && run.Updated.Before(cutoff) {
			candidates = append(candidates, run)
		}
	}
	// Children finish before their parent, so oldest first frees a parent
	// before it is reached.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Updated.Before(candidates[j].Updated) })
	for _, run := range candidates {
		if len(db.Sessions) <= retainedSessionsAfterPrune {
			return
		}
		if children[run.ID] > 0 {
			continue
		}
		delete(db.Runs, run.ID)
		children[run.ParentID]--
		session := run.Session.key()
		if sessionRuns[session]--; sessionRuns[session] == 0 {
			delete(db.Sessions, session)
		}
	}
}

func prunableRun(run Run) bool {
	switch run.Status {
	case "completed", "failed", "canceled":
	default:
		return false
	}
	return run.ChangeSessionID == "" && run.WorkflowID == "" && run.Input == nil
}

// journalGates serializes this process's writes of one journal: writers
// wait their turn here, for as long as their own context allows, instead of
// competing for the file lock, whose short timeout is meant for another
// process holding it.
var journalGates sync.Map

func (s *Store) enterJournal(ctx context.Context) (func(), error) {
	path, err := s.path("journal.json")
	if err != nil {
		return nil, err
	}
	value, _ := journalGates.LoadOrStore(path, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
