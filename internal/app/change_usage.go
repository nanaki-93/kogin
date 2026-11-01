package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
)

// What agents spend is read from the agent journal: every run records the
// budget it charged, and runs started for a task carry its session ID. A
// workflow charges its stages as they finish, so a task shows what each stage
// cost across attempts. Costs are estimates from the configured price table.

// ChangeUsageTotal is the usage of a set of budgets and its estimated cost.
type ChangeUsageTotal struct {
	Runs  int                    `json:"runs"`
	Usage agentstate.BudgetUsage `json:"usage"`
	Cost  *AgentCostEstimate     `json:"cost"`
}

// ChangeUsageRow is what agents spent on one task.
type ChangeUsageRow struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind,omitempty"`
	State     string    `json:"state,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	ChangeUsageTotal
}

// ChangeUsageReport totals agent usage per task in the active checkout.
type ChangeUsageReport struct {
	ProjectID       string           `json:"project_id"`
	ProjectRevision string           `json:"project_revision"`
	Currency        string           `json:"currency,omitempty"`
	PriceVersion    string           `json:"price_version,omitempty"`
	Tasks           []ChangeUsageRow `json:"tasks"`
	Other           ChangeUsageTotal `json:"other"`
	Total           ChangeUsageTotal `json:"total"`
}

// chargeWorkflowStage adds what the run spent since its last charge to the
// stage that was running: the latest of create, test and review that started.
// Caller owns changesMu, which also guards job.spent.
func chargeWorkflowStage(ctx context.Context, job *changeWorkflowJob, w *ChangeWorkflow) {
	budget := agentruntime.BudgetFromContext(ctx)
	if budget == nil || w == nil {
		return
	}
	now := budget.Snapshot()
	delta := now.Minus(job.spent)
	job.spent = now
	stage := -1
	for i := 0; i < 3 && i < len(w.Stages); i++ {
		if !w.Stages[i].StartedAt.IsZero() {
			stage = i
		}
	}
	if stage < 0 || delta.ModelCalls == 0 && delta.InputTokens == 0 && delta.OutputTokens == 0 {
		return
	}
	if prior := w.Stages[stage].Usage; prior != nil {
		delta = prior.Plus(delta)
	}
	w.Stages[stage].Usage = &delta
}

// estimateStageCosts prices each stage's recorded usage for a read.
func (s *Service) estimateStageCosts(session *ChangeSession) {
	if session.Workflow == nil {
		return
	}
	for i := range session.Workflow.Stages {
		if usage := session.Workflow.Stages[i].Usage; usage != nil {
			session.Workflow.Stages[i].Cost = agentCost(*usage, s.conf().agentRuntime.Telemetry.Prices)
		}
	}
}

// ChangeUsage totals the agent journal's budgets per task.
func (s *Service) ChangeUsage(ctx context.Context) (*ChangeUsageReport, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	root := s.manager.Root()
	store, _, err := s.agentJournal(root)
	if err != nil {
		return nil, err
	}
	history, err := store.History(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := listChangeSessions(root)
	if err != nil {
		return nil, err
	}
	prices := s.conf().agentRuntime.Telemetry.Prices
	report := &ChangeUsageReport{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Currency: prices.Currency, PriceVersion: prices.Version, Tasks: []ChangeUsageRow{}}
	rows := map[string]*ChangeUsageRow{}
	for _, budget := range journalBudgets(history, index.ProjectID) {
		total := &report.Other
		if budget.session != "" {
			if rows[budget.session] == nil {
				rows[budget.session] = &ChangeUsageRow{SessionID: budget.session, Title: budget.session}
			}
			total = &rows[budget.session].ChangeUsageTotal
		}
		total.Runs += budget.runs
		total.Usage = total.Usage.Plus(budget.usage)
		report.Total.Runs += budget.runs
		report.Total.Usage = report.Total.Usage.Plus(budget.usage)
	}
	for _, task := range tasks {
		if row := rows[task.ID]; row != nil {
			row.Title, row.Kind, row.State, row.UpdatedAt = task.Title, task.Kind, task.State, task.UpdatedAt
		}
	}
	for _, row := range rows {
		row.Cost = agentCost(row.Usage, prices)
		report.Tasks = append(report.Tasks, *row)
	}
	slices.SortFunc(report.Tasks, func(a, b ChangeUsageRow) int {
		return cmp.Or(cmp.Compare(b.Usage.InputTokens+b.Usage.OutputTokens, a.Usage.InputTokens+a.Usage.OutputTokens), b.UpdatedAt.Compare(a.UpdatedAt))
	})
	report.Other.Cost, report.Total.Cost = agentCost(report.Other.Usage, prices), agentCost(report.Total.Usage, prices)
	return report, nil
}

type journalBudget struct {
	session string
	runs    int
	usage   agentstate.BudgetUsage
}

// journalBudgets lists the project's budgets with the task their runs served
// and how many top-level runs charged them.
func journalBudgets(history agentstate.History, projectID string) []journalBudget {
	byID := map[string]*journalBudget{}
	var order []string
	for _, run := range history.Runs {
		if run.Binding.Project != projectID {
			continue
		}
		budget := byID[run.BudgetID]
		if budget == nil {
			budget = &journalBudget{usage: run.Usage}
			byID[run.BudgetID] = budget
			order = append(order, run.BudgetID)
		}
		if budget.session == "" {
			budget.session = run.ChangeSessionID
		}
		if run.ParentID == "" {
			budget.runs++
		}
	}
	budgets := make([]journalBudget, 0, len(order))
	for _, id := range order {
		budgets = append(budgets, *byID[id])
	}
	return budgets
}

// revisionScope attributes a manual revision's model runs to its task.
func revisionScope(ctx context.Context, session *ChangeSession) context.Context {
	if ctx.Value(changeWorkflowContextKey{}) != nil {
		return ctx
	}
	return agentruntime.WithChangeScope(ctx, session.ID, fmt.Sprintf("%s-revision-%d", session.ID, session.Revision+1))
}
