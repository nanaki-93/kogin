package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kasuri/v2/internal/app/agentstate"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// CoderTask is a task for comparing the coders: a small project, what to
// change and when it is done.
type CoderTask struct {
	Files    map[string]string
	Kind     string
	Title    string
	Request  string
	Paths    []string
	Criteria []string
}

// CoderFile is one file a coder changed.
type CoderFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"`
	Content string `json:"-"`
}

// CoderAttempt is what one coder made of a task, and what it spent.
type CoderAttempt struct {
	Tools   bool                   `json:"tools"`
	Changes []CoderFile            `json:"changes"`
	Usage   agentstate.BudgetUsage `json:"usage"`
	Stage   *ChangeStageTools      `json:"stage_tools,omitempty"`
	Elapsed time.Duration          `json:"-"`
}

// RunCoder runs the creation stage once, as the tool-using agent or as the
// one-shot proposal, on a task in its own worktree of a scratch copy of the
// project. It runs no tests, fix-ups or reviewers: the caller scores the
// result. The configuration's bug profile is the coder. Execution trust is
// granted for the scratch project, so callers point KASURI_TRUST_DIR at a
// scratch directory as cmd/agent-eval does.
func RunCoder(ctx context.Context, cfg *config.Config, task CoderTask, tools bool) (*CoderAttempt, error) {
	root, err := os.MkdirTemp("", "kasuri-coder-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	service, session, worktree, err := openCoderTask(ctx, cfg, root, task)
	if err != nil {
		return nil, err
	}
	defer service.CancelChangeWorkflows()
	runtime := service.workflowStageRuntime(0, string(config.BugModelScope))
	if tools && !toolStageAvailable(session, runtime) {
		return nil, errors.New("the bug profile's model cannot run a tool-using agent; use a pi, claude or agy model")
	}
	runCtx, cancel, err := service.workflowAgentContext(ctx, ChangeWorkflowModels{Create: string(config.BugModelScope), Test: string(config.BugModelScope), Review: string(config.BugModelScope)})
	if err != nil {
		return nil, err
	}
	defer cancel()
	limit := duration(runtime.effective.Timeout)
	if tools {
		limit = service.toolStageLimit(runtime)
	}
	timed, stop := context.WithTimeout(runCtx, limit)
	defer stop()
	started := time.Now()
	var result *ChangeSession
	var stage *ChangeStageTools
	if tools {
		result, stage, err = service.runToolAgent(timed, worktree, session, runtime, toolCoder, task.Request)
	} else {
		result, err = service.generateChangeWithRuntime(timed, worktree, session, ChangeMessageRequest{ChangeIdentity: identityForChange(session), Message: task.Request}, runtime)
	}
	attempt := &CoderAttempt{Tools: tools, Stage: stage, Usage: agentruntime.BudgetFromContext(runCtx).Snapshot(), Elapsed: time.Since(started), Changes: []CoderFile{}}
	if err != nil {
		return attempt, err
	}
	for _, edit := range result.Changes {
		attempt.Changes = append(attempt.Changes, CoderFile{Path: edit.Path, OldPath: edit.OldPath, Status: editStatus(edit), Content: edit.Content})
	}
	return attempt, nil
}

// openCoderTask writes the project into root, commits it, trusts it and
// opens the task in its own worktree.
func openCoderTask(ctx context.Context, cfg *config.Config, root string, task CoderTask) (*Service, *ChangeSession, string, error) {
	for name, content := range task.Files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, nil, "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return nil, nil, "", err
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".kasuri/\n.worktrees/\n"), 0o644); err != nil {
		return nil, nil, "", err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Kasuri evaluation"}, {"config", "user.email", "eval@kasuri.invalid"}, {"add", "--", "."}, {"commit", "-q", "-m", "Evaluation fixture"}} {
		if _, err := gitRead(ctx, root, args...); err != nil {
			return nil, nil, "", fmt.Errorf("prepare the evaluation project: %w", err)
		}
	}
	manager, err := project.NewManager(root)
	if err != nil {
		return nil, nil, "", err
	}
	if err := manager.Set(root, &project.Analysis{Name: "coder-evaluation", Path: root}); err != nil {
		return nil, nil, "", err
	}
	service, err := New(cfg, manager)
	if err != nil {
		return nil, nil, "", err
	}
	index, err := service.Reindex()
	if err != nil {
		return nil, nil, "", err
	}
	if _, err := service.TrustProjectExecution(ctx, index.ProjectRevision, true); err != nil {
		return nil, nil, "", err
	}
	opened, err := service.OpenChangeInWorktree(ctx, ChangeOpenRequest{ChangeCreateRequest: ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: task.Kind, Title: task.Title, Paths: task.Paths, AcceptanceCriteria: task.Criteria}, ConfirmExecution: true})
	if err != nil {
		return nil, nil, "", err
	}
	child, _, err := service.WorktreeService(ctx, opened.WorktreePath)
	if err != nil {
		return nil, nil, "", err
	}
	return child, opened.ChangeSession, opened.WorktreePath, nil
}
