package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

// Workflow choices reference catalog models or configured profiles; credentials
// and retry limits remain daemon-owned.
type ChangeWorkflowModels struct {
	Create string `json:"create"`
	Test   string `json:"test"`
	Review string `json:"review"`
}

func (models ChangeWorkflowModels) selections() []string {
	return []string{models.Create, models.Test, models.Review}
}

func (models ChangeWorkflowModels) validate() error {
	for _, profile := range models.selections() {
		if !validModelSelection(profile) {
			return fmt.Errorf("workflow models must reference catalog model IDs or configured analyze, bug or code profiles")
		}
	}
	return nil
}

type ChangeWorkflowRequest struct {
	StreamOutput bool   `json:"stream_output,omitempty"`
	PlanHash     string `json:"plan_hash,omitempty"`
	ResumeFrom   string `json:"resume_from,omitempty"`
	ChangeIdentity
	Message           string               `json:"message"`
	Models            ChangeWorkflowModels `json:"models"`
	ConfirmedProfiles []string             `json:"confirmed_profiles"`
	ConfirmSecurity   bool                 `json:"confirm_security"`
	AutoRepair        bool                 `json:"auto_repair,omitempty"`
	// IncludeComments sends the open line comments as a repair brief.
	IncludeComments bool `json:"include_comments,omitempty"`
}

type ChangeWorkflowControl struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	WorkflowID      string `json:"workflow_id"`
}

type ChangeWorkflowStage struct {
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Model      *EffectiveModel `json:"model,omitempty"`
	// Usage is what the stage spent across attempts; Cost is estimated on read.
	Usage *agentstate.BudgetUsage `json:"usage,omitempty"`
	Cost  *AgentCostEstimate      `json:"cost,omitempty"`
	// Tools describes the stage's latest run as a tool-using agent; nil for a
	// one-shot proposal.
	Tools *ChangeStageTools `json:"tools,omitempty"`
}

// ChangeStageTools is what a tool-using agent did in a stage.
type ChangeStageTools struct {
	Calls int `json:"calls"`
	// Summary counts the calls per tool, most used first.
	Summary  string `json:"summary,omitempty"`
	Turns    int    `json:"turns"`
	Finished bool   `json:"finished"`
	// finish is what the agent's finish call said; it is not stored.
	finish *toolFinish
}

// Findings block approval; notes are advisory and never change the verdict.
type ChangeWorkflowReview struct {
	ProposalHash string       `json:"proposal_hash"`
	Verdict      string       `json:"verdict"`
	Summary      string       `json:"summary"`
	Findings     []ReviewItem `json:"findings"`
	Notes        []ReviewItem `json:"notes,omitempty"`
}

// ChangeWorkflowOverride records a human decision to review a proposal the
// model reviewers rejected. It binds one proposal hash and grants no approval:
// the files, checks and explicit approval gates still apply.
type ChangeWorkflowOverride struct {
	ProposalHash string    `json:"proposal_hash"`
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
}

type ChangeSpecialistReview struct {
	ID       string                `json:"id"`
	Status   string                `json:"status"`
	Reason   string                `json:"reason,omitempty"`
	Evidence string                `json:"evidence"`
	Model    *EffectiveModel       `json:"model"`
	Review   *ChangeWorkflowReview `json:"review,omitempty"`
}

// Workflow reports live in private change history alongside captured source.
// A model verdict never establishes human approval or measured performance.
type ChangeWorkflow struct {
	StreamOutput       bool                     `json:"stream_output,omitempty"`
	Input              *ChangeInput             `json:"input,omitempty"`
	Attempt            int                      `json:"attempt,omitempty"`
	ResumeFrom         string                   `json:"resume_from,omitempty"`
	ID                 string                   `json:"id"`
	Status             string                   `json:"status"`
	Reason             string                   `json:"reason,omitempty"`
	Models             ChangeWorkflowModels     `json:"models"`
	AutoRepair         bool                     `json:"auto_repair,omitempty"`
	AutoRepairAttempts int                      `json:"auto_repair_attempts,omitempty"`
	Stages             []ChangeWorkflowStage    `json:"stages"`
	Review             *ChangeWorkflowReview    `json:"review,omitempty"`
	Override           *ChangeWorkflowOverride  `json:"override,omitempty"`
	Specialists        []ChangeSpecialistReview `json:"specialists,omitempty"`
	// Pause is set while the run waits for a subscription plan's usage limit
	// to reset (status paused).
	Pause *ChangeWorkflowPause `json:"pause,omitempty"`
	// Waiting names the task a waiting run waits for (status waiting), and
	// Pending what it starts with again once that task is merged or deleted.
	Waiting   *ChangeWorkflowWait    `json:"waiting,omitempty"`
	Pending   *ChangeWorkflowPending `json:"pending,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// ChangeWorkflowPause says where a run stopped at a plan's usage limit and
// how to continue it. Nothing the stage left half done was kept.
type ChangeWorkflowPause struct {
	// Stage is the stage that reached the limit: create, test or review.
	Stage string `json:"stage"`
	// ResumeFrom is the resume_from that continues the run: create, test,
	// checks or review.
	ResumeFrom string `json:"resume_from"`
	// Provider is the CLI whose plan reached its limit.
	Provider string `json:"provider"`
	// ResumesAt is when the plan's limit resets, when the CLI said so.
	ResumesAt time.Time `json:"resumes_at,omitempty"`
}

type changeWorkflowContextKey struct{}

type changeWorkflowJob struct {
	root, sessionID, id string
	models              ChangeWorkflowModels
	start               workflowStart
	specialists         []string
	autoRepair          bool
	// request is what started the run, kept for a run that ends waiting.
	request   ChangeWorkflowRequest
	cancel    context.CancelFunc
	interrupt context.CancelCauseFunc
	done      chan struct{}
	finished  bool
	failure   string
	// spent is the run's budget usage already charged to its stages.
	spent agentstate.BudgetUsage
}

// The controller tracks active workflow workers per change session. Session/proposal
// writes use changesMu; this mutex is never held while acquiring changesMu or doing I/O.
type changeWorkflowController struct {
	mu   sync.Mutex
	job  *changeWorkflowJob
	jobs map[string]*changeWorkflowJob
}

func (c *changeWorkflowController) getJob(sessionID string) *changeWorkflowJob {
	if c.jobs != nil {
		if j := c.jobs[sessionID]; j != nil {
			return j
		}
	}
	if c.job != nil && c.job.sessionID == sessionID {
		return c.job
	}
	return nil
}

// anyRunning reports a job that has not finished. The caller holds mu.
func (c *changeWorkflowController) anyRunning() bool {
	for _, job := range c.jobs {
		if !job.finished {
			return true
		}
	}
	return false
}

func (c *changeWorkflowController) setJob(job *changeWorkflowJob) {
	if c.jobs == nil {
		c.jobs = make(map[string]*changeWorkflowJob)
	}
	c.job = job
	c.jobs[job.sessionID] = job
}

func (c *changeWorkflowController) allJobs() []*changeWorkflowJob {
	seen := make(map[*changeWorkflowJob]bool)
	var out []*changeWorkflowJob
	for _, j := range c.jobs {
		if j != nil && !seen[j] {
			seen[j] = true
			out = append(out, j)
		}
	}
	if c.job != nil && !seen[c.job] {
		out = append(out, c.job)
	}
	return out
}

func isChangePipelineActive(workflow *ChangeWorkflow) bool {
	return workflow != nil && (workflow.Status == "running" || workflow.Status == "canceling")
}

func canHumanReviewProposal(session *ChangeSession) bool {
	w := session.Workflow
	return w == nil || (w.Status == "awaiting_human_review" && inputAllowsReview(w) && w.Review != nil && w.Review.ProposalHash == session.Hash && (w.Review.Verdict == "approve" || reviewOverridden(w, session.Hash)) && specialistsComplete(w, session.Hash))
}

// reviewOverridden reports a human override of the model verdict at hash.
func reviewOverridden(w *ChangeWorkflow, hash string) bool {
	return w.Override != nil && w.Override.ProposalHash == hash && w.Review != nil && w.Review.ProposalHash == hash && w.Review.Verdict == "changes_requested"
}

func invalidateChangeWorkflow(session *ChangeSession, reason string) {
	if session.Workflow != nil {
		session.Workflow.Status, session.Workflow.Reason = "outdated", reason
		session.Workflow.Stages[3].Status = "blocked"
	}
}

func stopChangeWorkflowStages(w *ChangeWorkflow) {
	for i := range w.Stages[:3] {
		switch w.Stages[i].Status {
		case "running":
			w.Stages[i].Status = w.Status
			if w.Stages[i].Reason == "" {
				w.Stages[i].Reason = w.Reason
			}
		case "pending":
			w.Stages[i].Status = "skipped"
		}
	}
	w.Stages[3].Status = "blocked"
	stopWorkflowSpecialists(w)
}

func validateStoredWorkflow(w *ChangeWorkflow) error {
	if w == nil {
		return nil // Existing schema-1 conversations have no workflow.
	}
	upgradeLegacyWorkflowScope(w)
	if err := validateChangeInput(w.Input); err != nil {
		return err
	}
	if w.ID == "" || len(w.ID) > 100 || len(w.Stages) != 4 {
		return fmt.Errorf("corrupt change workflow")
	}
	if err := w.Models.validate(); err != nil {
		return err
	}
	switch w.Status {
	case "running", "canceling", "failed", "canceled", "stale", "interrupted", "outdated", "changes_requested", "awaiting_human_review", "paused", "nothing_to_fix", "waiting":
	default:
		return fmt.Errorf("corrupt workflow status")
	}
	if err := validateWorkflowPause(w); err != nil {
		return err
	}
	if err := validateWorkflowWait(w); err != nil {
		return err
	}
	if err := validateWorkflowStages(w); err != nil {
		return err
	}
	if err := validateWorkflowSpecialists(w); err != nil {
		return err
	}
	if err := validateWorkflowOverride(w); err != nil {
		return err
	}
	if w.Review != nil {
		return validateWorkflowReview(w.Review)
	}
	return nil
}

func validateWorkflowStages(w *ChangeWorkflow) error {
	for i, name := range []string{"create", "test", "review", "human_review"} {
		if w.Stages[i].Name != name {
			return fmt.Errorf("corrupt workflow stages")
		}
	}
	for i, profile := range []string{w.Models.Create, w.Models.Test, w.Models.Review} {
		if w.Stages[i].Model == nil || w.Stages[i].Model.Profile != profile {
			return fmt.Errorf("corrupt workflow model provenance")
		}
	}
	return nil
}

// settleWorkflowPause drops the pause of a run that is no longer paused: it
// was restored, outdated or went stale while it waited.
func settleWorkflowPause(w *ChangeWorkflow) {
	if w != nil && w.Status != "paused" {
		w.Pause = nil
	}
}

// validateWorkflowPause accepts a pause only on a paused run, naming a stage
// and a start that continues it.
func validateWorkflowPause(w *ChangeWorkflow) error {
	pause := w.Pause
	if pause == nil {
		if w.Status == "paused" {
			return fmt.Errorf("corrupt workflow pause")
		}
		return nil
	}
	if w.Status != "paused" || !slices.Contains([]string{"create", "test", "review"}, pause.Stage) || !slices.Contains([]string{"create", "test", "checks", "review"}, pause.ResumeFrom) || pause.Provider == "" || len(pause.Provider) > 32 {
		return fmt.Errorf("corrupt workflow pause")
	}
	return nil
}

// validateWorkflowOverride accepts an override only beside the rejecting
// review it overrides, at the same proposal hash.
func validateWorkflowOverride(w *ChangeWorkflow) error {
	override := w.Override
	if override == nil {
		return nil
	}
	if w.Review == nil || w.Review.Verdict != "changes_requested" || override.ProposalHash != w.Review.ProposalHash || !boundedWorkflowText(override.Reason, maxOverrideReason) || override.At.IsZero() {
		return fmt.Errorf("corrupt workflow review override")
	}
	return nil
}

// legacyCodeScope is the name the code scope had before it was renamed. Tasks
// and recovery journals saved then still name it, and must stay readable: a
// journal that cannot be read blocks Apply, Undo and task deletion.
const legacyCodeScope = "function"

// upgradeLegacyWorkflowScope reads a stored workflow's legacy scope name as the
// code scope, in memory only; requests naming it are still rejected.
func upgradeLegacyWorkflowScope(w *ChangeWorkflow) {
	for _, selection := range []*string{&w.Models.Create, &w.Models.Test, &w.Models.Review} {
		if *selection == legacyCodeScope {
			*selection = string(config.CodeModelScope)
		}
	}
	upgrade := func(model *EffectiveModel) {
		if model == nil {
			return
		}
		if model.Scope == legacyCodeScope {
			model.Scope = string(config.CodeModelScope)
		}
		if model.Profile == legacyCodeScope {
			model.Profile = string(config.CodeModelScope)
		}
	}
	for i := range w.Stages {
		upgrade(w.Stages[i].Model)
	}
	for i := range w.Specialists {
		upgrade(w.Specialists[i].Model)
	}
}
