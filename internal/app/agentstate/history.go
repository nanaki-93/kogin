package agentstate

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

const MaxProgressEvents = 2048

type ArtifactReference struct {
	Session Key    `json:"session"`
	Name    string `json:"name"`
	Version int64  `json:"version"`
}

type NodeProgress struct {
	NodeID   string              `json:"node_id"`
	State    string              `json:"state"`
	Evidence []ArtifactReference `json:"evidence,omitempty"`
}

// ProgressEvent contains only host-projected metadata, never raw SDK payloads.
type ProgressEvent struct {
	Sequence        uint64              `json:"sequence"`
	RootID          string              `json:"root_id"`
	RunID           string              `json:"run_id"`
	ParentID        string              `json:"parent_id,omitempty"`
	ProjectID       string              `json:"project_id"`
	ProjectRevision string              `json:"project_revision"`
	NodeID          string              `json:"node_id,omitempty"`
	Kind            string              `json:"kind"`
	State           string              `json:"state"`
	Time            time.Time           `json:"time"`
	Usage           BudgetUsage         `json:"usage"`
	Evidence        []ArtifactReference `json:"evidence,omitempty"`
	RequestID       string              `json:"request_id,omitempty"`
}

type RunSnapshot struct {
	Run
	Binding Binding
	Usage   BudgetUsage
	Limits  config.AgentLimits
}

type History struct {
	Traces   map[string]TraceRecord
	Epoch    string
	Sequence uint64
	Runs     []RunSnapshot
	Events   []ProgressEvent
}

func (s *Store) History(ctx context.Context) (History, error) {
	if err := context.Cause(ctx); err != nil {
		return History{}, err
	}
	db, err := s.load()
	if err != nil {
		return History{}, err
	}
	history := History{Traces: db.Traces, Epoch: db.ProgressEpoch, Sequence: db.ProgressSequence, Events: slices.Clone(db.Progress), Runs: make([]RunSnapshot, 0, len(db.Runs))}
	for _, run := range db.Runs {
		budget := db.Budgets[run.BudgetID]
		history.Runs = append(history.Runs, RunSnapshot{Run: run, Binding: budget.Binding, Usage: budget.Usage, Limits: budget.Limits})
	}
	slices.SortFunc(history.Runs, func(a, b RunSnapshot) int {
		if order := b.Started.Compare(a.Started); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return history, nil
}

func progressRoot(db *database, run Run) (string, error) {
	for depth := 0; depth < 64; depth++ {
		if run.ParentID == "" {
			return run.ID, nil
		}
		parent, exists := db.Runs[run.ParentID]
		if !exists || parent.BudgetID != run.BudgetID || parent.ID == run.ID {
			return "", ErrCorrupt
		}
		run = parent
	}
	return "", fmt.Errorf("agent invocation ancestry exceeds 64 levels")
}

func appendProgress(db *database, run Run, kind string, node *NodeProgress) error {
	root, err := progressRoot(db, run)
	if err != nil {
		return err
	}
	if db.ProgressEpoch == "" {
		db.ProgressEpoch = rand.Text()
	}
	db.ProgressSequence++
	budget := db.Budgets[run.BudgetID]
	event := ProgressEvent{Sequence: db.ProgressSequence, RootID: root, RunID: run.ID, ParentID: run.ParentID, ProjectID: budget.Binding.Project, ProjectRevision: budget.Binding.Revision, Kind: kind, State: run.Status, Time: time.Now().UTC(), Usage: budget.Usage}
	if node != nil {
		event.NodeID, event.State, event.Evidence = node.NodeID, node.State, slices.Clone(node.Evidence)
	}
	if run.Input != nil {
		event.RequestID = run.Input.RequestID
	}
	db.Progress = append(db.Progress, event)
	if len(db.Progress) > MaxProgressEvents {
		db.Progress = slices.Clone(db.Progress[len(db.Progress)-MaxProgressEvents:])
	}
	return nil
}

func appendNodeProgress(db *database, key Key, node *NodeProgress) error {
	if node == nil {
		return nil // Legacy and non-runtime sessions have no public projection.
	}
	for _, run := range db.Runs {
		if run.Session == key && unfinishedRun(run) {
			return appendProgress(db, run, "node", node)
		}
	}
	return fmt.Errorf("node progress has no unfinished invocation")
}

func validateProgress(db *database) error {
	if len(db.Progress) > MaxProgressEvents || db.ProgressEpoch == "" && (len(db.Progress) != 0 || db.ProgressSequence != 0) {
		return ErrCorrupt
	}
	if db.ProgressEpoch != "" && !validID(db.ProgressEpoch) {
		return ErrCorrupt
	}
	var previous uint64
	for _, event := range db.Progress {
		if event.Sequence <= previous || event.Sequence > db.ProgressSequence || previous != 0 && event.Sequence != previous+1 || validateProgressEvent(event) != nil {
			return ErrCorrupt
		}
		previous = event.Sequence
	}
	if previous != db.ProgressSequence {
		return ErrCorrupt
	}
	return nil
}

func validateProgressEvent(event ProgressEvent) error {
	for _, id := range []string{event.RootID, event.RunID, event.ProjectID, event.ProjectRevision} {
		if !validID(id) {
			return ErrCorrupt
		}
	}
	if event.Time.IsZero() || !validUsage(event.Usage) || len(event.Evidence) > 8 {
		return ErrCorrupt
	}
	for _, id := range []string{event.ParentID, event.NodeID, event.RequestID} {
		if id != "" && !validID(id) {
			return ErrCorrupt
		}
	}
	if !slices.Contains([]string{"started", "finished", "node", "input_requested", "input_received", "input_finished"}, event.Kind) || !slices.Contains([]string{"running", "waiting_input", "completed", "failed", "canceled", "expired", "interrupted", "recorded"}, event.State) {
		return ErrCorrupt
	}
	for _, reference := range event.Evidence {
		if reference.Session.Validate() != nil || reference.Name != "completion.json" || reference.Version < 1 {
			return ErrCorrupt
		}
	}
	return nil
}
