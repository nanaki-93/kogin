package app

import (
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/archgate"
	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const (
	// maxTaskPaths is how many files a new task may name, the same limit
	// the Web UI and plan investigation use.
	maxTaskPaths = 8
	// maxStoredTaskPaths bounds tasks read from disk: the API accepted up to
	// 64 paths before it matched the UI, and those tasks stay readable.
	maxStoredTaskPaths = 64
	// maxTaskTitleBytes is the longest task title a new task may have.
	maxTaskTitleBytes = 200
	maxChangeBytes    = 256 * 1024
	maxChangeMessages = 40
)

type ChangeTarget = archgate.ChangeTarget
type ChangeEdit = archgate.ChangeEdit

// ChangeSession is private project-local history, unlike source-free findings
// and audits. Restored check/review evidence is cleared before reuse.
type ChangeSession struct {
	Plan                *ChangePlan             `json:"plan,omitempty"`
	ReviewedFiles       []string                `json:"reviewed_files,omitempty"`
	FileReviewHash      string                  `json:"file_review_hash,omitempty"`
	LastReviewedChanges []ChangeEdit            `json:"last_reviewed_changes,omitempty"`
	LastReviewedHash    string                  `json:"last_reviewed_hash,omitempty"`
	ReviewDiff          []ChangeEdit            `json:"review_diff,omitempty"`
	SchemaVersion       int                     `json:"schema_version"`
	ID                  string                  `json:"id"`
	ProjectID           string                  `json:"project_id"`
	ProjectRevision     string                  `json:"project_revision"`
	Kind                string                  `json:"kind"`
	Title               string                  `json:"title"`
	AcceptanceCriteria  []string                `json:"acceptance_criteria"`
	FindingTargets      []project.FindingTarget `json:"finding_targets,omitempty"`
	Targets             []ChangeTarget          `json:"targets"`
	Instructions        map[string]string       `json:"instructions"`
	PolicyFingerprint   string                  `json:"policy_fingerprint"`
	WorkspaceHash       string                  `json:"workspace_hash"`
	ScopeVersion        int                     `json:"scope_version,omitempty"`
	Scope               []ChangeScopeFile       `json:"scope,omitempty"`
	StaleReason         string                  `json:"stale_reason,omitempty"` // Set on read; never stored.
	MainChanged         *ChangeMainChange       `json:"main_changed,omitempty"` // Set on read; never stored.
	Git                 *GitIdentity            `json:"git,omitempty"`
	Worktree            *ChangeWorktree         `json:"worktree,omitempty"`
	Commit              *ChangeCommit           `json:"commit,omitempty"`
	Revision            int64                   `json:"revision"`
	Hash                string                  `json:"hash"`
	State               string                  `json:"state"`
	Freshness           string                  `json:"freshness"`
	Messages            []ChangeMessage         `json:"messages"`
	Changes             []ChangeEdit            `json:"changes"`
	PinnedTests         []ChangeEdit            `json:"pinned_tests,omitempty"`
	ContextManifest     project.ContextManifest `json:"context_manifest"`
	Checks              []CheckResult           `json:"checks"`
	CheckOptions        CheckOptions            `json:"check_options"`
	ChecksWorkspaceHash string                  `json:"checks_workspace_hash,omitempty"`
	ChecksOutdated      bool                    `json:"checks_outdated,omitempty"` // Set on read; never stored.
	ReviewedHash        string                  `json:"reviewed_hash,omitempty"`
	ReviewNotes         string                  `json:"review_notes,omitempty"`
	Comments            []ChangeComment         `json:"comments,omitempty"`
	RepairAttempts      int                     `json:"repair_attempts"`
	Workflow            *ChangeWorkflow         `json:"workflow,omitempty"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
}

// Listing summaries keeps a full history within the native response bound;
// source and conversation contents are returned only for the chosen session.
type ChangeHistoryEntry = gitops.ChangeHistoryEntry

type ChangeCreateRequest struct {
	Plan               *ChangePlanInput        `json:"plan,omitempty"`
	ProjectID          string                  `json:"project_id"`
	ProjectRevision    string                  `json:"project_revision"`
	Kind               string                  `json:"kind"`
	Title              string                  `json:"title"`
	Paths              []string                `json:"paths"`
	AcceptanceCriteria []string                `json:"acceptance_criteria"`
	FindingTargets     []project.FindingTarget `json:"finding_targets,omitempty"`
}

type ChangeIdentity struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	Revision        int64  `json:"revision"`
	Hash            string `json:"hash"`
}

type ChangeEditInput struct {
	Criteria []int  `json:"criteria,omitempty"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Summary  string `json:"summary,omitempty"`
	// Delete removes the file and RenamedFrom moves a file to Path; both
	// need a task in its own worktree.
	Delete      bool   `json:"delete,omitempty"`
	RenamedFrom string `json:"renamed_from,omitempty"`
}

type ChangeMessageRequest struct {
	ChangeIdentity
	Profile               string            `json:"profile,omitempty"`
	Message               string            `json:"message"`
	ConfirmRemoteProvider bool              `json:"confirm_remote_provider"`
	Repair                bool              `json:"repair,omitempty"`
	Changes               []ChangeEditInput `json:"changes,omitempty"`
	// IncludeComments sends the open line comments as a repair brief.
	IncludeComments bool `json:"include_comments,omitempty"`
	sentComments    []string
}

type ChangeCheckRequest struct {
	ChangeIdentity
	CheckOptions
}

type ChangeApplyRequest struct {
	ChangeIdentity
	GitIdentity
	Confirm       bool   `json:"confirm"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type ChangeMutationResult struct {
	SessionID       string                `json:"session_id"`
	ProjectID       string                `json:"project_id"`
	ProjectRevision string                `json:"project_revision"`
	State           string                `json:"state"`
	Hash            string                `json:"hash"`
	UndoAvailable   bool                  `json:"undo_available"`
	Index           *project.ProjectIndex `json:"index,omitempty"`
	Warnings        []string              `json:"warnings"`
	Verification    *ChangeVerification   `json:"verification,omitempty"`
	Commit          *ChangeCommit         `json:"commit,omitempty"`
}

// ChangeMessage is retained in its private proposal conversation.
type ChangeMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
