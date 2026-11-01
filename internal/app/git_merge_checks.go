package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app/gitops"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/toolchain"
)

// MergeChecksRequest names a merge in the current checkout: the commit the
// branch was on before it (From) and the merged commit it is on now (To).
type MergeChecksRequest struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	From            string `json:"from"`
	To              string `json:"to"`
}

// MergeChecks is what the tests and lint of the subprojects a merge changed
// report on the merged result.
type MergeChecks struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Outcome is passed, failed or unavailable.
	Outcome      string        `json:"outcome"`
	Summary      string        `json:"summary"`
	ChangedPaths int           `json:"changed_paths"`
	Checks       []CheckResult `json:"checks"`
}

var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// CheckMerge runs the tests and lint of every subproject a merge changed, on
// a copy of the merged checkout. Failures that also happen on the commit
// before the merge are reported as known instead of blocking, like a task's
// checks. It never changes the checkout: a failure is for the user to fix in
// a new task or by reverting the merge.
func (s *Service) CheckMerge(ctx context.Context, request MergeChecksRequest) (*MergeChecks, error) {
	root, changed, err := s.mergeToCheck(ctx, request)
	if err != nil {
		return nil, err
	}
	workspace, err := os.MkdirTemp("", "kasuri-merge-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workspace)
	if err := copyCheckWorkspace(ctx, root, workspace); err != nil {
		return nil, err
	}
	fingerprint, err := fingerprintCheckWorkspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.mergeToCheck(ctx, request); err != nil {
		return nil, err
	}
	run := &checkRun{s: s, ctx: ctx, root: root, revision: request.ProjectRevision, fingerprint: fingerprint, baseKey: "before merge " + request.From,
		revert: func(dir string) error { return restoreCommitPaths(ctx, root, dir, request.From, changed) }}
	defer run.cleanup()
	profiles := s.projectProfiles(root)
	affected := toolchain.Affected(profiles, changed)
	checks := run.commandChecks(workspace, affected, CheckOptions{RunTests: true, RunLint: true})
	if !anyTestCommand(affected) {
		checks = append(checks, CheckResult{Name: "tests", Required: true, State: CheckUnavailable, Outcome: "could_not_execute",
			Output: "No test command covers the merged files; no tests ran."})
	}
	if note := unaffectedNote(profiles, affected); note != "" {
		checks = append(checks, CheckResult{Name: "other subprojects", Required: false, State: CheckSkipped, Outcome: "not_affected", Output: note})
	}
	outcome, summary := mergeChecksOutcome(checks, ctx.Err())
	return &MergeChecks{From: request.From, To: request.To, Outcome: outcome, Summary: summary, ChangedPaths: len(changed), Checks: checks}, nil
}

// mergeToCheck confirms the request names the merge the checkout is on and
// returns the files the merge changed.
func (s *Service) mergeToCheck(ctx context.Context, request MergeChecksRequest) (string, []string, error) {
	root, err := s.mergedCheckout(ctx, request)
	if err != nil {
		return "", nil, err
	}
	listed, err := gitRead(ctx, root, "diff", "--name-only", "-z", "--no-renames", request.From, request.To)
	if err != nil {
		return "", nil, err
	}
	var changed []string
	for _, file := range strings.Split(listed, "\x00") {
		if file != "" && !worktreeMetadata(file) {
			changed = append(changed, file)
		}
	}
	return root, changed, nil
}

// mergedCheckout returns the project root when it is the project the request
// names, on the merged commit with no merge in progress, and From is an
// ancestor of that commit.
func (s *Service) mergedCheckout(ctx context.Context, request MergeChecksRequest) (string, error) {
	if !commitID.MatchString(request.From) || !commitID.MatchString(request.To) || request.From == request.To {
		return "", errors.New("name the commits before and after the merge")
	}
	index, err := s.manager.Index()
	if err != nil {
		return "", err
	}
	if index.ProjectID != request.ProjectID || index.ProjectRevision != request.ProjectRevision {
		return "", project.ErrRevisionConflict
	}
	root := s.manager.Root()
	repository, err := readGitRepository(ctx, root)
	if err != nil {
		return "", err
	}
	if !repository.Available || repository.Head != request.To || repository.MergeHead != "" {
		return "", fmt.Errorf("%w: the checkout is no longer on the merged commit", project.ErrRevisionConflict)
	}
	if _, err := gitRead(ctx, root, "merge-base", "--is-ancestor", request.From, request.To); err != nil {
		if gitops.ExitCode(err, 1) {
			return "", fmt.Errorf("%s is not before the merge", request.From[:7])
		}
		return "", err
	}
	return root, nil
}

// restoreCommitPaths turns the given files of a copy of root back into their
// content at commit, removing the ones the commit does not have.
func restoreCommitPaths(ctx context.Context, root, dir, commit string, files []string) error {
	for _, file := range files {
		full, err := project.ResolveWritePath(dir, file)
		if err != nil {
			return err
		}
		listed, err := gitRead(ctx, root, "ls-tree", "-z", commit, "--", file)
		if err != nil {
			return err
		}
		mode, _, _ := strings.Cut(listed, " ")
		switch mode {
		case "":
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
		case "100644", "100755":
			content, err := gitRead(ctx, root, "cat-file", "blob", commit+":"+file)
			if err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if mode == "100755" {
				perm = 0o755
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return err
			}
			if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.WriteFile(full, []byte(content), perm); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s is not a regular file before the merge", file)
		}
	}
	return nil
}

func mergeChecksOutcome(checks []CheckResult, err error) (string, string) {
	if err != nil {
		return "unavailable", "The checks were interrupted before they finished."
	}
	if requiredChecksPassed(checks) {
		return "passed", "The tests and lint of the merged subprojects pass on the merged result."
	}
	if onlyUnavailableRequiredChecks(checks) {
		return "unavailable", "Nothing could run to check the merged result. Trust the project's commands, or declare them under checks in AGENTS.md."
	}
	var failed []string
	for _, check := range checks {
		if check.Required && check.State != CheckPassed && check.State != CheckUnavailable {
			failed = append(failed, check.Name)
		}
	}
	return "failed", fmt.Sprintf("Failed on the merged result: %s. The merge stays in place: fix it in a new task, or revert the merge commit.", strings.Join(failed, ", "))
}
