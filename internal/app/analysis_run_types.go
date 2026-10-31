package app

import (
	"errors"
	"fmt"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const AnalysisRunSchemaVersion = "1"
const AnalysisRunScopeProject = "project"

// AnalysisRunStatus describes progress, never the quality of the analyzed code.
type AnalysisRunStatus string

const (
	AnalysisRunQueued         AnalysisRunStatus = "queued"
	AnalysisRunRunning        AnalysisRunStatus = "running"
	AnalysisRunPausing        AnalysisRunStatus = "pausing"
	AnalysisRunPaused         AnalysisRunStatus = "paused"
	AnalysisRunCanceling      AnalysisRunStatus = "canceling"
	AnalysisRunCanceled       AnalysisRunStatus = "canceled"
	AnalysisRunInterrupted    AnalysisRunStatus = "interrupted"
	AnalysisRunCompleted      AnalysisRunStatus = "completed"
	AnalysisRunCompletedEmpty AnalysisRunStatus = "completed_empty"
	AnalysisRunPartial        AnalysisRunStatus = "partial"
	AnalysisRunFailed         AnalysisRunStatus = "failed"
	AnalysisRunUnavailable    AnalysisRunStatus = "unavailable"
	AnalysisRunStale          AnalysisRunStatus = "stale"
)

func (status AnalysisRunStatus) Valid() bool {
	switch status {
	case AnalysisRunQueued, AnalysisRunRunning, AnalysisRunPausing, AnalysisRunPaused,
		AnalysisRunCanceling, AnalysisRunCanceled, AnalysisRunInterrupted, AnalysisRunCompleted,
		AnalysisRunCompletedEmpty, AnalysisRunPartial, AnalysisRunFailed, AnalysisRunUnavailable, AnalysisRunStale:
		return true
	default:
		return false
	}
}

type AnalysisStage string

const (
	AnalysisStageSemantic      AnalysisStage = "semantic"
	AnalysisStagePerformance   AnalysisStage = "performance"
	AnalysisStageSecurityRules AnalysisStage = "security_rules"
	AnalysisStageSecurityAI    AnalysisStage = "security_ai"
	AnalysisStageFeatures      AnalysisStage = "feature_suggestions"
)

// Categories identifies consumers of a producer, not a classifier for its findings.
// Semantic output must carry its own validated category for each risk.
func (stage AnalysisStage) Categories() []project.FindingCategory {
	switch stage {
	case AnalysisStageSemantic:
		return []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity}
	case AnalysisStagePerformance:
		return []project.FindingCategory{project.FindingCategoryPerformance}
	case AnalysisStageSecurityRules, AnalysisStageSecurityAI:
		return []project.FindingCategory{project.FindingCategorySecurity}
	default:
		return nil
	}
}

type AnalysisStageStatus string

const (
	AnalysisStagePending        AnalysisStageStatus = "pending"
	AnalysisStageRunning        AnalysisStageStatus = "running"
	AnalysisStageCompleted      AnalysisStageStatus = "completed"
	AnalysisStageCompletedEmpty AnalysisStageStatus = "completed_empty"
	AnalysisStagePartial        AnalysisStageStatus = "partial"
	AnalysisStageFailed         AnalysisStageStatus = "failed"
	AnalysisStageSkipped        AnalysisStageStatus = "skipped"
	AnalysisStageUnavailable    AnalysisStageStatus = "unavailable"
	AnalysisStageCanceled       AnalysisStageStatus = "canceled"
	AnalysisStageInterrupted    AnalysisStageStatus = "interrupted"
	AnalysisStageStale          AnalysisStageStatus = "stale"
)

// AnalysisRunLimits bounds one dispatch window, not the captured project inventory.
// MaxAttemptsPerStage includes the initial provider request and all transport retries.
type AnalysisRunLimits struct {
	BatchFiles          int `json:"batch_files"`
	BudgetSeconds       int `json:"budget_seconds"`
	MaxAttemptsPerStage int `json:"max_attempts_per_stage"`
}

func (limits AnalysisRunLimits) Validate() error {
	if limits.BatchFiles < 1 || limits.BatchFiles > 500 || limits.BudgetSeconds < 1 || limits.BudgetSeconds > 3600 || limits.MaxAttemptsPerStage < 1 || limits.MaxAttemptsPerStage > 4 {
		return fmt.Errorf("analysis limits require 1-500 batch files, 1-3600 seconds and 1-4 total attempts per stage")
	}
	return nil
}

// AnalysisQueueIdentity binds the preview to source, policy and effective providers.
// QueueID also covers ordered stages, exclusions, limits and refresh. Cache availability
// is not identity: a run's own successful writes must not invalidate its remaining queue.
type AnalysisQueueIdentity struct {
	ProjectID           string `json:"project_id"`
	ProjectRevision     string `json:"project_revision"`
	PolicyFingerprint   string `json:"policy_fingerprint"`
	ProviderFingerprint string `json:"provider_fingerprint"`
	QueueID             string `json:"queue_id"`
}

type AnalysisRunIdentity struct {
	AnalysisQueueIdentity
	ID         string `json:"id"`
	Generation string `json:"generation"`
}

func (identity AnalysisQueueIdentity) Validate() error {
	if identity.ProjectID == "" || identity.ProjectRevision == "" || identity.PolicyFingerprint == "" || identity.ProviderFingerprint == "" || identity.QueueID == "" {
		return fmt.Errorf("analysis queue identity is incomplete")
	}
	return nil
}

func (identity AnalysisRunIdentity) Validate() error {
	if err := identity.AnalysisQueueIdentity.Validate(); err != nil {
		return err
	}
	if identity.ID == "" || identity.Generation == "" {
		return fmt.Errorf("analysis run identity is incomplete")
	}
	return nil
}

type AnalysisPreviewRequest struct {
	Models            *AnalysisModels      `json:"models,omitempty"`
	RetryStaleFailed  bool                 `json:"retry_stale_failed,omitempty"`
	StaleOnly         bool                 `json:"stale_only,omitempty"`
	StalePath         string               `json:"stale_path,omitempty"`
	RecoverIncomplete bool                 `json:"recover_incomplete,omitempty"`
	ClearResults      bool                 `json:"clear_results,omitempty"`
	ProjectID         string               `json:"project_id"`
	ProjectRevision   string               `json:"project_revision"`
	Scope             string               `json:"scope"`
	Refresh           bool                 `json:"refresh"`
	Limits            AnalysisRunLimits    `json:"limits"`
	ResumeRun         *AnalysisRunIdentity `json:"resume_run,omitempty"`
}

func (request AnalysisPreviewRequest) Validate() error {
	if err := request.Models.Validate(); err != nil {
		return err
	}
	if request.RetryStaleFailed && request.Refresh {
		return fmt.Errorf("stale/failed analysis cannot refresh fresh results")
	}
	if err := validateAnalysisSelectionOptions(request.RecoverIncomplete, request.Refresh, request.ClearResults, request.RetryStaleFailed, request.StaleOnly, request.StalePath); err != nil {
		return err
	}
	if request.Models != nil && request.Models.Features != "" {
		return fmt.Errorf("feature discovery was removed; analysis models cannot name a features model")
	}
	if request.ProjectID == "" || request.ProjectRevision == "" || request.Scope != AnalysisRunScopeProject {
		return fmt.Errorf("analysis requires a project identity and whole-project scope")
	}
	if request.ResumeRun != nil {
		if err := request.ResumeRun.Validate(); err != nil {
			return err
		}
		if request.ResumeRun.ProjectID != request.ProjectID || request.ResumeRun.ProjectRevision != request.ProjectRevision {
			return fmt.Errorf("resume preview does not match the requested project")
		}
	}
	return request.Limits.Validate()
}

type AnalysisFileIdentity struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
	Language    string `json:"language"`
}

// AnalysisProviderRequirement contains displayable model configuration, never tokens.
// ID is a fingerprint of the effective configuration, including prompt identity.
type AnalysisProviderRequirement struct {
	ID                         string          `json:"id"`
	Stages                     []AnalysisStage `json:"stages"`
	Model                      EffectiveModel  `json:"model"`
	RemoteConfirmationRequired bool            `json:"remote_confirmation_required"`
}

// Carried is set on a retained file's stage that has no current saved report:
// the run keeps the stage's earlier outcome (with Reason) and never dispatches it.
type AnalysisStagePlan struct {
	Stage            AnalysisStage       `json:"stage"`
	Eligible         bool                `json:"eligible"`
	Cached           bool                `json:"cached"`
	Carried          AnalysisStageStatus `json:"carried,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	ProviderID       string              `json:"provider_id,omitempty"`
	MaxModelRequests int                 `json:"max_model_requests"`
}

// A scoped run (retry, stale-only, recovery or one stale file) still covers the
// whole project: files outside its scope are Retained. When the run is admitted
// their saved reports are read back and their other stages keep the earlier
// outcome; no model request is ever sent for them.
type AnalysisPlannedFile struct {
	AnalysisFileIdentity
	SizeBytes int64               `json:"size_bytes"`
	Retained  bool                `json:"retained,omitempty"`
	Stages    []AnalysisStagePlan `json:"stages"`
}

type AnalysisExcludedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type AnalysisRunPreview struct {
	Models                       *AnalysisModels               `json:"models,omitempty"`
	Features                     *AnalysisFeaturePlan          `json:"features,omitempty"`
	Docs                         *AnalysisDocsPlan             `json:"docs,omitempty"`
	RetryStaleFailed             bool                          `json:"retry_stale_failed,omitempty"`
	StaleOnly                    bool                          `json:"stale_only,omitempty"`
	StalePath                    string                        `json:"stale_path,omitempty"`
	RecoverIncomplete            bool                          `json:"recover_incomplete,omitempty"`
	ClearResults                 bool                          `json:"clear_results,omitempty"`
	SchemaVersion                string                        `json:"schema_version"`
	PreviewID                    string                        `json:"preview_id"`
	Identity                     AnalysisQueueIdentity         `json:"identity"`
	Scope                        string                        `json:"scope"`
	Refresh                      bool                          `json:"refresh"`
	Limits                       AnalysisRunLimits             `json:"limits"`
	Files                        []AnalysisPlannedFile         `json:"files"`
	Excluded                     []AnalysisExcludedFile        `json:"excluded"`
	Providers                    []AnalysisProviderRequirement `json:"providers"`
	ExpectedModelRequests        int                           `json:"expected_model_requests"`
	MaxModelRequests             int                           `json:"max_model_requests"`
	SecurityReviewIntentRequired bool                          `json:"security_review_intent_required"`
}

// Confirmations belong to an explicit start/resume request and are never persisted.
// Resuming an interrupted run must obtain fresh intent for the remaining requests.
type AnalysisRunConfirmations struct {
	ProviderIDs    []string `json:"provider_ids"`
	SecurityReview bool     `json:"security_review"`
}

type AnalysisRunStartRequest struct {
	Models            *AnalysisModels          `json:"models,omitempty"`
	RetryStaleFailed  bool                     `json:"retry_stale_failed,omitempty"`
	StaleOnly         bool                     `json:"stale_only,omitempty"`
	StalePath         string                   `json:"stale_path,omitempty"`
	RecoverIncomplete bool                     `json:"recover_incomplete,omitempty"`
	ClearResults      bool                     `json:"clear_results,omitempty"`
	Identity          AnalysisQueueIdentity    `json:"identity"`
	PreviewID         string                   `json:"preview_id"`
	Limits            AnalysisRunLimits        `json:"limits"`
	Refresh           bool                     `json:"refresh"`
	Confirmations     AnalysisRunConfirmations `json:"confirmations"`
}

// Validate checks the request shape; admission must also recompute the preview
// and verify confirmations against its effective provider requirements.
func (request AnalysisRunStartRequest) Validate() error {
	if err := request.Models.Validate(); err != nil {
		return err
	}
	if request.RetryStaleFailed && request.Refresh {
		return fmt.Errorf("stale/failed analysis cannot refresh fresh results")
	}
	if err := validateAnalysisSelectionOptions(request.RecoverIncomplete, request.Refresh, request.ClearResults, request.RetryStaleFailed, request.StaleOnly, request.StalePath); err != nil {
		return err
	}
	if request.Models != nil && request.Models.Features != "" {
		return fmt.Errorf("feature discovery was removed; analysis models cannot name a features model")
	}
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.PreviewID == "" {
		return fmt.Errorf("analysis start requires an admission preview")
	}
	return request.Limits.Validate()
}

// Recovery completes unfinished file stages only. It never refreshes current
// results or reinterprets the stale/failed retry contract.
func validateAnalysisSelectionOptions(recoverIncomplete, refresh, clearResults, retryStaleFailed, staleOnly bool, stalePath string) error {
	if clearResults && !refresh {
		return fmt.Errorf("clear_results requires refresh")
	}
	if stalePath != "" && ((!staleOnly && !recoverIncomplete) || !analysisMetadataPath(stalePath)) {
		return fmt.Errorf("stale_path requires stale_only and a project-relative file path")
	}
	incompatible := refresh || clearResults || retryStaleFailed
	if staleOnly && (incompatible || recoverIncomplete) {
		return fmt.Errorf("stale-only analysis cannot be combined with refresh, retry_stale_failed or recover_incomplete")
	}
	if recoverIncomplete && incompatible {
		return fmt.Errorf("incomplete-work recovery cannot be combined with refresh or retry_stale_failed")
	}
	return nil
}

type AnalysisRunAction string

const (
	AnalysisRunPause  AnalysisRunAction = "pause"
	AnalysisRunResume AnalysisRunAction = "resume"
	AnalysisRunCancel AnalysisRunAction = "cancel"
)

type AnalysisRunControlRequest struct {
	Identity      AnalysisRunIdentity       `json:"identity"`
	Action        AnalysisRunAction         `json:"action"`
	PreviewID     string                    `json:"preview_id,omitempty"`
	Confirmations *AnalysisRunConfirmations `json:"confirmations,omitempty"`
}

func (request AnalysisRunControlRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	switch request.Action {
	case AnalysisRunResume:
		if request.PreviewID == "" || request.Confirmations == nil {
			return fmt.Errorf("analysis resume requires a fresh preview and confirmations")
		}
	case AnalysisRunPause, AnalysisRunCancel:
		if request.PreviewID != "" || request.Confirmations != nil {
			return fmt.Errorf("analysis pause or cancel cannot carry admission confirmations")
		}
	default:
		return fmt.Errorf("analysis control action is invalid")
	}
	return nil
}

// Coverage counts file-stage work units. Partial reports retain useful evidence but
// do not count as completely covered units; reused current reports count as succeeded.
type AnalysisRunCoverage struct {
	Total       int `json:"total"`
	Pending     int `json:"pending"`
	Running     int `json:"running"`
	Succeeded   int `json:"succeeded"`
	Partial     int `json:"partial"`
	Failed      int `json:"failed"`
	Skipped     int `json:"skipped"`
	Unavailable int `json:"unavailable"`
}

func (coverage AnalysisRunCoverage) Validate() error {
	remaining := coverage.Total
	if remaining < 0 {
		return fmt.Errorf("analysis coverage cannot be negative")
	}
	for _, count := range []int{coverage.Pending, coverage.Running, coverage.Succeeded, coverage.Partial, coverage.Failed, coverage.Skipped, coverage.Unavailable} {
		if count < 0 || count > remaining {
			return fmt.Errorf("analysis coverage counts do not match total")
		}
		remaining -= count
	}
	if remaining != 0 {
		return fmt.Errorf("analysis coverage counts do not match total")
	}
	return nil
}

type AnalysisSectionProgress struct {
	Category     project.FindingCategory `json:"category"`
	Status       AnalysisRunStatus       `json:"status"`
	Coverage     AnalysisRunCoverage     `json:"coverage"`
	FindingCount *int                    `json:"finding_count"`
}

func (section AnalysisSectionProgress) Validate() error {
	if !section.Category.Valid() || !section.Status.Valid() {
		return fmt.Errorf("analysis section category or status is invalid")
	}
	if err := section.Coverage.Validate(); err != nil {
		return err
	}
	covered := section.Coverage.Succeeded + section.Coverage.Partial
	if (covered == 0) != (section.FindingCount == nil) || (section.FindingCount != nil && *section.FindingCount < 0) {
		return fmt.Errorf("analysis finding count must describe successful evidence, or be unknown")
	}
	switch section.Status {
	case AnalysisRunCompleted, AnalysisRunCompletedEmpty:
		return section.validateCompleted()
	case AnalysisRunPartial:
		return section.validatePartial(covered)
	case AnalysisRunFailed, AnalysisRunUnavailable:
		return section.validateAbsentEvidence(covered)
	}

	return nil
}

type AnalysisStageProgress struct {
	Stage        AnalysisStage       `json:"stage"`
	Status       AnalysisStageStatus `json:"status"`
	Attempts     int                 `json:"attempts"`
	Cached       bool                `json:"cached"`
	FindingCount *int                `json:"finding_count"`
	ReportID     string              `json:"report_id,omitempty"`
	Reason       string              `json:"reason,omitempty"`
}

type AnalysisRunFile struct {
	AnalysisFileIdentity
	Stages []AnalysisStageProgress `json:"stages"`
}

// AnalysisRun persists only identities and operational progress. Result prose lives
// in producer-owned report stores; no source, prompt, transcript or consent is saved here.
type AnalysisRun struct {
	Features *AnalysisFeatureProgress `json:"features,omitempty"`
	// Docs is what refreshing the docs did at the end of the run.
	Docs                 *DocsRefresh              `json:"docs,omitempty"`
	SchemaVersion        string                    `json:"schema_version"`
	Identity             AnalysisRunIdentity       `json:"identity"`
	Plan                 AnalysisRunPreview        `json:"plan"`
	Status               AnalysisRunStatus         `json:"status"`
	Files                []AnalysisRunFile         `json:"files"`
	Sections             []AnalysisSectionProgress `json:"sections"`
	ElapsedSeconds       int64                     `json:"elapsed_seconds"`
	WindowElapsedSeconds int64                     `json:"window_elapsed_seconds"`
	WindowFilesCompleted int                       `json:"window_files_completed"`
	Reason               string                    `json:"reason,omitempty"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
}

var errAnalysisRunPersistence = errors.New("analysis progress could not be saved; resume or cancel to recover")
var errAnalysisRunBusy = fmt.Errorf("%w: an analysis run is already active; continue or cancel it before replacement", project.ErrRevisionConflict)

// ErrAnalysisRunBusy distinguishes retained work from a changed project identity.
var ErrAnalysisRunBusy = errAnalysisRunBusy

// ErrAnalysisProgressUnavailable identifies recoverable progress storage failures.
var ErrAnalysisProgressUnavailable = errAnalysisRunPersistence

// One mutex owns admission, attempt reservations, progress and report publication.
// Disk writes remain inside that boundary. The worker never takes jobLifecycleMu,
// so project changes can invalidate it without waiting while holding its lock.

func (section AnalysisSectionProgress) validateCompleted() error {
	if section.Coverage.Total == 0 || section.Coverage.Succeeded != section.Coverage.Total || section.FindingCount == nil {
		return fmt.Errorf("completed analysis requires full successful coverage")
	}
	if (section.Status == AnalysisRunCompletedEmpty) != (*section.FindingCount == 0) {
		return fmt.Errorf("completed analysis status does not match finding count")
	}
	return nil
}

func (section AnalysisSectionProgress) validatePartial(covered int) error {
	if covered == 0 || section.Coverage.Succeeded == section.Coverage.Total {
		return fmt.Errorf("partial analysis requires useful but incomplete coverage")
	}
	if section.Coverage.Pending != 0 || section.Coverage.Running != 0 {
		return fmt.Errorf("partial analysis cannot hide pending work")
	}
	return nil
}

func (section AnalysisSectionProgress) validateAbsentEvidence(covered int) error {
	if section.Status == AnalysisRunFailed && (covered != 0 || section.Coverage.Failed == 0 || section.Coverage.Pending != 0 || section.Coverage.Running != 0) {
		return fmt.Errorf("failed analysis requires terminal work without successful evidence")
	}
	if section.Status == AnalysisRunUnavailable && (covered != 0 || section.Coverage.Failed != 0 || section.Coverage.Pending != 0 || section.Coverage.Running != 0) {
		return fmt.Errorf("unavailable analysis cannot hide eligible work")
	}
	return nil
}
