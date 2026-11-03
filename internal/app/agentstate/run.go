package agentstate

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// Binding contains identity hashes only; source and transcripts belong in the
// private session body, never in run diagnostics.
type Binding struct {
	Project       string `json:"project"`
	Revision      string `json:"revision"`
	Policy        string `json:"policy"`
	Configuration string `json:"configuration"`
}

type BudgetUsage struct {
	Accounting           *UsageAccounting `json:"accounting,omitempty"`
	ModelCalls           int              `json:"model_calls"`
	ToolCalls            int              `json:"tool_calls"`
	Repairs              int              `json:"repairs"`
	InputTokens          int64            `json:"input_tokens"`
	OutputTokens         int64            `json:"output_tokens"`
	UnknownUsageCalls    int              `json:"unknown_usage_calls"`
	ReservedInputTokens  int64            `json:"reserved_input_tokens"`
	ReservedOutputTokens int64            `json:"reserved_output_tokens"`
	ActiveCalls          int              `json:"active_calls"`
	ElapsedMillis        int64            `json:"elapsed_millis"`
}

type BudgetRecord struct {
	ID       string             `json:"id"`
	Binding  Binding            `json:"binding"`
	Limits   config.AgentLimits `json:"limits"`
	Sequence uint64             `json:"sequence"`
	Usage    BudgetUsage        `json:"usage"`
}

type Run struct {
	ChangeSessionID string           `json:"change_session_id,omitempty"`
	WorkflowID      string           `json:"workflow_id,omitempty"`
	ParentID        string           `json:"parent_id,omitempty"`
	Input           *InputCheckpoint `json:"input,omitempty"`
	ID              string           `json:"id"`
	Session         Key              `json:"session"`
	BudgetID        string           `json:"budget_id"`
	RequestHash     string           `json:"request_hash"`
	ProviderBinding string           `json:"provider_binding,omitempty"`
	SchemaHash      string           `json:"schema_hash"`
	SchemaVersion   string           `json:"schema_version"`
	Status          string           `json:"status"`
	Started         time.Time        `json:"started"`
	Updated         time.Time        `json:"updated"`
}

func (b Binding) Validate() error {
	for _, value := range []string{b.Project, b.Revision, b.Policy, b.Configuration} {
		if !validID(value) {
			return fmt.Errorf("invalid agent run binding")
		}
	}
	return nil
}

func validateRunRecords(db *database) error {
	for id, budget := range db.Budgets {
		if id != budget.ID || !validID(id) || budget.Binding.Validate() != nil || budget.Limits.Validate() != nil || !validUsage(budget.Usage) || budget.Sequence == 0 {
			return ErrCorrupt
		}
	}
	for id, run := range db.Runs {
		if err := validateRun(id, run); err != nil {
			return err
		}
		if _, ok := db.Budgets[run.BudgetID]; !ok {
			return ErrCorrupt
		}
		if _, ok := db.Sessions[run.Session.key()]; !ok {
			return ErrCorrupt
		}
		if _, err := progressRoot(db, run); err != nil {
			return err
		}
	}
	return nil
}

func validateRun(id string, run Run) error {
	if !validRunOwner(run) {
		return ErrCorrupt
	}
	if err := validateInputCheckpoint(run); err != nil {
		return err
	}
	if run.ProviderBinding != "" && !validHash(run.ProviderBinding) {
		return ErrCorrupt
	}
	if id != run.ID || !validID(id) || run.Session.Validate() != nil || !validRunSchema(run.SchemaVersion) || !validHash(run.RequestHash) || !validHash(run.SchemaHash) || run.Started.IsZero() || run.Updated.Before(run.Started) {
		return ErrCorrupt
	}
	switch run.Status {
	case "running", "waiting_input", "completed", "failed", "canceled", "expired", "interrupted":
		return nil
	default:
		return ErrCorrupt
	}
}

func validRunOwner(run Run) bool {
	if run.ChangeSessionID == "" {
		return run.WorkflowID == ""
	}
	return validID(run.ChangeSessionID) && validID(run.WorkflowID)
}

func validRunSchema(version string) bool {
	switch version {
	case "completion-v1", "investigation-v1", "change-graph-v1", "review-graph-v1", "repair-graph-v1", "confirmation-v1", "analysis-graph-v1", "knowledge-graph-v1", "grounding-v1", "delegation-v1", "mission-graph-v1", "multimodal-v1", "live-v1":
		return true
	default:
		return false
	}
}

func validHash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32
}

func validUsage(u BudgetUsage) bool {
	for _, value := range []int64{int64(u.ModelCalls), int64(u.ToolCalls), int64(u.Repairs), u.InputTokens, u.OutputTokens, int64(u.UnknownUsageCalls), u.ReservedInputTokens, u.ReservedOutputTokens, int64(u.ActiveCalls), u.ElapsedMillis} {
		if value < 0 || value > 1<<62 {
			return false
		}
	}
	return u.UnknownUsageCalls <= u.ModelCalls && u.ActiveCalls <= u.ModelCalls && validAccounting(u)
}

func (s *Store) Checkpoint(ctx context.Context, record BudgetRecord, span *TraceSpan) (bool, error) {
	recorded := false
	err := s.mutate(ctx, func(db *database) error {
		if previous, exists := db.Budgets[record.ID]; exists {
			if record.Binding != previous.Binding || record.Limits != previous.Limits || record.Sequence <= previous.Sequence || !monotonicUsage(previous.Usage, record.Usage) {
				return fmt.Errorf("agent budget checkpoint conflicts with stored identity or sequence")
			}
		}
		db.Budgets[record.ID] = record
		var err error
		recorded, err = applyTraceRecord(db, record.ID, span)
		return err
	})
	return recorded && err == nil, err
}

func monotonicUsage(previous, next BudgetUsage) bool {
	return next.ModelCalls >= previous.ModelCalls && next.ToolCalls >= previous.ToolCalls && next.Repairs >= previous.Repairs && next.InputTokens >= previous.InputTokens && next.OutputTokens >= previous.OutputTokens && next.UnknownUsageCalls >= previous.UnknownUsageCalls && next.ElapsedMillis >= previous.ElapsedMillis && monotonicAccounting(previous, next)
}

func (s *Store) BeginRun(ctx context.Context, run Run, span *TraceSpan) (bool, error) {
	recorded := false
	err := s.mutate(ctx, func(db *database) error {
		if _, exists := db.Runs[run.ID]; exists {
			return fmt.Errorf("agent invocation already exists")
		}
		if run.Status != "running" {
			return fmt.Errorf("new agent invocation must be running")
		}
		if run.ParentID != "" && db.Runs[run.ParentID].Status != "running" {
			return fmt.Errorf("child invocation requires its active parent")
		}
		db.Runs[run.ID] = run
		var err error
		recorded, err = applyTraceRecord(db, run.BudgetID, span)
		if err != nil {
			return err
		}
		return appendProgress(db, run, "started", nil)
	})
	return recorded && err == nil, err
}

func (s *Store) FinishRun(ctx context.Context, id, status string, span *TraceSpan) error {
	return s.mutate(ctx, func(db *database) error {
		run, exists := db.Runs[id]
		if !exists {
			return ErrNotFound
		}
		if run.Status != "running" || status != "completed" && status != "failed" && status != "canceled" {
			return fmt.Errorf("agent invocation terminal status cannot be replaced")
		}
		run.Status = status
		run.Updated = time.Now().UTC()
		db.Runs[id] = run
		if _, err := applyTraceRecord(db, run.BudgetID, span); err != nil {
			return err
		}
		return appendProgress(db, run, "finished", nil)
	})
}

func deleteSessionRuns(db *database, key Key) error {
	for _, run := range db.Runs {
		if run.Session == key && unfinishedRun(run) {
			return fmt.Errorf("cannot delete a session with an unfinished invocation")
		}
	}
	for _, run := range db.Runs {
		if parent, ok := db.Runs[run.ParentID]; ok && parent.Session == key && run.Session != key {
			return fmt.Errorf("delete descendant invocation sessions before their parent")
		}
	}
	for id, run := range db.Runs {
		if run.Session == key {
			delete(db.Runs, id)
		}
	}
	return nil
}
