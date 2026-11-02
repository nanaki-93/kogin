package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nanaki-93/kasuri/v2/internal/app/toolserver"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

// An agent run's tools work on the task's worktree. Every call passes the
// same gates whichever CLI makes it: reads stay within what the context
// policy admits, writes follow the task's diff policy and role, and the run
// has a call budget. A failed gate is an error the model reads and can act on.

// toolRole decides what an agent run may write.
type toolRole string

const (
	// toolCoder writes the change: any file the diff policy admits.
	toolCoder toolRole = "coder"
	// toolTester writes tests only; implementation files are read-only.
	toolTester toolRole = "tester"
	// toolReader only reads, to answer a question about the code.
	toolReader toolRole = "reader"
)

// toolLimits bound one agent run.
type toolLimits struct {
	// calls is how many tool calls the run may make before only finish is left.
	calls int
	// changedFiles is how many files the run may write.
	changedFiles int
	// checks is how many times the run may run the checks.
	checks int
}

func defaultToolLimits(role toolRole) toolLimits {
	if role == toolTester {
		return toolLimits{calls: 80, changedFiles: 16, checks: 4}
	}
	return toolLimits{calls: 150, changedFiles: 40, checks: 6}
}

// toolFinish is the report a run ends with.
type toolFinish struct {
	Summary  string          `json:"summary"`
	Criteria []criterionNote `json:"criteria,omitempty"`
}

type criterionNote struct {
	Criterion int    `json:"criterion"`
	How       string `json:"how"`
}

// taskToolRun holds one run's gates and what it did.
type taskToolRun struct {
	s        *Service
	root     string
	session  *ChangeSession
	role     toolRole
	limits   toolLimits
	policy   *project.ContextPolicy
	resolver *rules.Resolver
	// listed, when set, are the files the run sees instead of Git's list.
	listed []string

	// writeMu serializes writes: a model may send several calls at once.
	writeMu sync.Mutex
	mu      sync.Mutex
	calls   int
	counts  map[string]int
	written map[string]bool
	checks  int
	finish  *toolFinish
}

func (s *Service) newTaskToolRun(root string, session *ChangeSession, role toolRole) (*taskToolRun, error) {
	if !ownsWorktree(session) {
		return nil, errors.New("agent tools need a task in its own worktree")
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	return &taskToolRun{s: s, root: root, session: session, role: role, limits: defaultToolLimits(role), policy: policy, resolver: rules.NewResolver(root), counts: map[string]int{}, written: map[string]bool{}}, nil
}

// usage summarizes the calls, most used first: "read_file 12, edit_file 4".
func (run *taskToolRun) usage() (int, string) {
	run.mu.Lock()
	defer run.mu.Unlock()
	names := make([]string, 0, len(run.counts))
	for name := range run.counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if run.counts[names[i]] != run.counts[names[j]] {
			return run.counts[names[i]] > run.counts[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, run.counts[name]))
	}
	return run.calls, strings.Join(parts, ", ")
}

func (run *taskToolRun) finished() *toolFinish {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.finish
}

// admit counts a call and refuses it once the run is finished or out of calls.
func (run *taskToolRun) admit(name string) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.finish != nil {
		return errors.New("this run is finished; end your turn")
	}
	if run.calls >= run.limits.calls && name != "finish" && name != "answer" {
		return fmt.Errorf("this run has used its %d tool calls; call finish now with what you have", run.limits.calls)
	}
	run.calls++
	run.counts[name]++
	return nil
}

type toolSpec struct {
	name, description, schema string
	readOnly, destructive     bool
	call                      func(context.Context, json.RawMessage) (string, error)
}

// tools are the run's tools as the tool server serves them.
func (run *taskToolRun) tools() []toolserver.Tool {
	specs := append(run.readTools(), run.writeTools()...)
	return run.serve(append(specs, run.checkTools()...))
}

// serve turns tool specs into served tools that pass the run's call gate.
func (run *taskToolRun) serve(specs []toolSpec) []toolserver.Tool {
	tools := make([]toolserver.Tool, 0, len(specs))
	for _, spec := range specs {
		call, name := spec.call, spec.name
		tools = append(tools, toolserver.Tool{Name: name, Description: spec.description, Schema: json.RawMessage(spec.schema), ReadOnly: spec.readOnly, Destructive: spec.destructive,
			Call: func(ctx context.Context, arguments json.RawMessage) (string, error) {
				if err := run.admit(name); err != nil {
					return "", err
				}
				return call(ctx, arguments)
			}})
	}
	return tools
}

// decodeToolArguments reads a call's arguments strictly, so a misnamed field
// is reported instead of ignored.
func decodeToolArguments(arguments json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// cleanToolPath makes a model-supplied path project-relative and refuses
// paths outside the project and Git or Kasuri metadata.
func cleanToolPath(file string) (string, error) {
	file = strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(file)), "./")
	clean := path.Clean(file)
	switch {
	case file == "" || clean == ".":
		return "", errors.New("name a file by its project-relative path")
	case strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean):
		return "", fmt.Errorf("%s is outside the project; use project-relative paths", boundedProposalPath(file))
	case worktreeMetadata(clean):
		return "", fmt.Errorf("%s belongs to Git or Kasuri", boundedProposalPath(file))
	}
	return clean, nil
}

// readable resolves a file the run may read.
func (run *taskToolRun) readable(file string) (string, string, error) {
	clean, err := cleanToolPath(file)
	if err != nil {
		return "", "", err
	}
	if decision := run.policy.Decide(clean); !decision.Include {
		return "", "", fmt.Errorf("%s is left out by the project's context policy (%s)", clean, decision.Reason)
	}
	full, err := project.ResolveWritePath(run.root, clean)
	if err != nil {
		return "", "", fmt.Errorf("%s: %v", clean, err)
	}
	return clean, full, nil
}

// writable resolves a file the run may write: the diff policy, the role and
// the run's file budget.
func (run *taskToolRun) writable(file string) (string, string, error) {
	clean, full, err := run.readable(file)
	if err != nil {
		return "", "", err
	}
	if err := worktreeWritable(run.policy, run.session, clean); err != nil {
		return "", "", errors.New(strings.Replace(err.Error(), "proposal wrote", "you may not write", 1))
	}
	if run.role == toolTester && !run.resolver.IsTestFile(clean) {
		return "", "", fmt.Errorf("%s is not a test file; the testing agent writes tests only, and the implementation is fixed", clean)
	}
	if err := issueFixScope(run.session).check(clean, run.resolver.IsTestFile); err != nil {
		return "", "", err
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if !run.written[clean] && len(run.written) >= run.limits.changedFiles {
		return "", "", fmt.Errorf("this run may change at most %d files", run.limits.changedFiles)
	}
	run.written[clean] = true
	return clean, full, nil
}
