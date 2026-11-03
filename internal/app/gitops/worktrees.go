package gitops

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

type ChangeCommit struct {
	GitIdentity
	State   string `json:"state"`
	Message string `json:"message"`
	OID     string `json:"oid,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ChangeHistoryEntry is a summary of a change session for listing and worktree inspection.
type ChangeHistoryEntry struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	Kind            string `json:"kind"`
	Title           string `json:"title"`
	Revision        int64  `json:"revision"`
	Hash            string `json:"hash"`
	State           string `json:"state"`
	Freshness       string `json:"freshness"`
	WorkflowStatus  string `json:"workflow_status,omitempty"`
	WorkflowReason  string `json:"workflow_reason,omitempty"`
	// WorkflowPhase is what a running workflow does now; nil when none runs
	// or before its first stage starts.
	WorkflowPhase *ChangeWorkflowPhase `json:"workflow_phase,omitempty"`
	// Review is where your review of the current version stands; nil unless
	// the version waits for it.
	Review         *ChangeReviewProgress   `json:"review,omitempty"`
	Commit         *ChangeCommit           `json:"commit,omitempty"`
	FindingTargets []project.FindingTarget `json:"finding_targets,omitempty"`
	// DismissedTargets are issues the task found do not hold: merging it
	// dismisses them.
	DismissedTargets []project.FindingTarget `json:"dismissed_targets,omitempty"`
	Paths            []string                `json:"paths,omitempty"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

// ChangeWorkflowPhase is the stage a running workflow is in, for task lists.
type ChangeWorkflowPhase struct {
	// Stage is the running stage: create, test or review.
	Stage string `json:"stage"`
	// Issue is the 1-based issue being fixed, of Issues, while a task fixes
	// its issues one at a time.
	Issue  int `json:"issue,omitempty"`
	Issues int `json:"issues,omitempty"`
	// Repair is the automatic repair the run is on; 0 on its first pass.
	Repair int `json:"repair,omitempty"`
	// ChecksOnly marks a run started at the checks: its test stage runs
	// only the checks.
	ChecksOnly bool `json:"checks_only,omitempty"`
}

// Review steps of a version that waits for you: view its files, run its
// checks, then accept it.
const (
	ReviewViewFiles = "view_files"
	ReviewRunChecks = "run_checks"
	ReviewAccept    = "accept"
)

// ChangeReviewProgress is where your review of a task's current version
// stands.
type ChangeReviewProgress struct {
	// Step is what the review needs next: ReviewViewFiles, ReviewRunChecks
	// or ReviewAccept.
	Step string `json:"step"`
	// Files is how many files the version changes, Viewed how many of them
	// you marked viewed.
	Files  int `json:"files"`
	Viewed int `json:"viewed"`
}

// Integrated reports whether the task was applied and committed on its
// branch: the condition for merging its worktree and completing its findings.
func (entry ChangeHistoryEntry) Integrated() bool {
	return entry.State == "applied" && entry.Commit != nil && entry.Commit.State == "committed"
}

type GitBranch struct {
	Name         string `json:"name"`
	Head         string `json:"head"`
	WorktreePath string `json:"worktree_path,omitempty"`
}

type GitWorktree struct {
	Path string `json:"path"`
	GitIdentity
	Current      bool                 `json:"current"`
	Locked       bool                 `json:"locked"`
	LockReason   string               `json:"lock_reason,omitempty"`
	Prunable     bool                 `json:"prunable"`
	Dirty        *bool                `json:"dirty,omitempty"`
	Mergeable    bool                 `json:"mergeable"`
	Integration  string               `json:"integration,omitempty"`
	OverlapPaths []string             `json:"overlap_paths,omitempty"`
	Completed    []ChangeHistoryEntry `json:"completed"`
	Sessions     []ChangeHistoryEntry `json:"sessions,omitempty"`
	Error        string               `json:"error,omitempty"`
}

type GitWorkspace struct {
	*GitRepository
	Branches  []GitBranch   `json:"branches"`
	Worktrees []GitWorktree `json:"worktrees"`
}

func ReadWorktrees(ctx context.Context, root string) ([]GitWorktree, error) {
	output, err := Read(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	result := []GitWorktree{}
	for _, record := range strings.Split(output, "\x00\x00") {
		if record == "" {
			continue
		}
		tree := GitWorktree{Completed: []ChangeHistoryEntry{}}
		for _, field := range strings.Split(record, "\x00") {
			key, value, _ := strings.Cut(field, " ")
			switch key {
			case "worktree":
				tree.Path = value
			case "HEAD":
				tree.Head = value
			case "branch":
				tree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "locked":
				tree.Locked, tree.LockReason = true, value
			case "prunable":
				tree.Prunable = true
			}
		}
		if tree.Path == "" {
			return nil, fmt.Errorf("invalid Git worktree record")
		}
		if canonical, err := project.CanonicalRoot(tree.Path); err == nil {
			tree.Path = canonical
		} else {
			tree.Path = filepath.Clean(tree.Path)
		}
		tree.Current = tree.Path == filepath.Clean(root)
		result = append(result, tree)
		if len(result) > 100 {
			return nil, fmt.Errorf("more than 100 worktrees; use Git directly to manage this repository")
		}
	}
	return result, nil
}

func PopulateWorktree(ctx context.Context, target *GitRepository, tree *GitWorktree, loadHistory func(string) ([]ChangeHistoryEntry, error)) {
	canonical, err := project.CanonicalRoot(tree.Path)
	if err != nil {
		tree.Error = err.Error()
		return
	}
	tree.Path, tree.Current = canonical, canonical == target.Root
	repository, err := ReadRepository(ctx, canonical)
	if err != nil {
		tree.Error = err.Error()
		return
	}
	if !repository.Available || !repository.AtRoot || repository.GitIdentity != tree.GitIdentity {
		tree.Error = "Worktree changed; refresh Git status."
		return
	}
	tree.Dirty = &repository.Dirty
	if err := InspectIntegration(ctx, target, tree); err != nil {
		tree.Integration, tree.OverlapPaths = "", nil
		tree.Error = "Integration status unavailable: " + err.Error()
		return
	}
	history, err := loadHistory(canonical)
	if err != nil {
		tree.Error = "Fix history unavailable: " + err.Error()
		return
	}
	tree.Sessions = history
	for _, entry := range history {
		if entry.Integrated() {
			tree.Completed = append(tree.Completed, entry)
		}
	}
	tree.Mergeable, err = WorktreeMergeable(ctx, target, tree, repository)
	if err != nil {
		tree.Error = "Merge status unavailable: " + err.Error()
	}
}

func ReadBranches(ctx context.Context, root string, trees []GitWorktree) ([]GitBranch, error) {
	output, err := Read(ctx, root, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	result := []GitBranch{}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		name, head, ok := strings.Cut(line, "\x00")
		if !ok {
			return nil, fmt.Errorf("invalid Git branch record")
		}
		branch := GitBranch{Name: strings.TrimPrefix(name, "refs/heads/"), Head: head}
		for _, tree := range trees {
			if tree.Branch == branch.Name {
				branch.WorktreePath = tree.Path
			}
		}
		result = append(result, branch)
	}
	return result, nil
}
