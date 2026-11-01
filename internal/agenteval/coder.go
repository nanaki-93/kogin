package agenteval

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project/toolchain"
)

// The coder suite compares the tool-using coder with the one-shot proposal
// on small tasks in several languages. Each task's hidden tests are added
// only to score the result; the coders never see them. Receipts keep
// outcomes, failing test names and usage, never prompts or code.

//go:embed testdata/coder_tasks.json
var coderCorpusData []byte

const coderScoringTimeout = 5 * time.Minute

type coderCase struct {
	ID       string            `json:"id"`
	Language string            `json:"language"`
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Request  string            `json:"request"`
	Paths    []string          `json:"paths"`
	Criteria []string          `json:"criteria"`
	Files    map[string]string `json:"files"`
	Hidden   map[string]string `json:"hidden"`
	Test     []string          `json:"test"`
}

type coderCorpus struct {
	Version string      `json:"version"`
	Cases   []coderCase `json:"cases"`
}

// Coder names a coder: the tool-using agent or the one-shot proposal.
const (
	CoderTools   = "tools"
	CoderOneShot = "one-shot"
)

// CoderOptions select the cases and coders of a live coder campaign.
type CoderOptions struct {
	Config    *config.Config
	CaseID    string
	Coders    []string
	Confirmed bool
	// run replaces app.RunCoder in tests.
	run func(context.Context, *config.Config, app.CoderTask, bool) (*app.CoderAttempt, error)
}

// CoderCaseReceipt is one coder's result on one task.
type CoderCaseReceipt struct {
	ID       string `json:"id"`
	Language string `json:"language"`
	Coder    string `json:"coder"`
	// State is passed, failed (the hidden tests fail), error (the coder did
	// not produce a proposal) or unavailable (the tests could not run here).
	State    string                 `json:"state"`
	Reason   string                 `json:"reason,omitempty"`
	Failing  []string               `json:"failing_tests,omitempty"`
	Changed  int                    `json:"changed_files"`
	Usage    agentstate.BudgetUsage `json:"usage"`
	Tools    *app.ChangeStageTools  `json:"tools,omitempty"`
	Duration int64                  `json:"duration_ms"`
}

// CoderSummary totals one coder's results.
type CoderSummary struct {
	Cases        int   `json:"cases"`
	Passed       int   `json:"passed"`
	ModelCalls   int   `json:"model_calls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// UnknownUsage counts model calls whose usage the provider did not report.
	UnknownUsage int `json:"unknown_usage_calls"`
}

// CoderReceipt is the campaign's result.
type CoderReceipt struct {
	Version       int                     `json:"version"`
	Suite         string                  `json:"suite"`
	CorpusVersion string                  `json:"corpus_version"`
	CorpusDigest  string                  `json:"corpus_digest"`
	Runtime       RuntimeIdentity         `json:"runtime"`
	ModelKey      string                  `json:"model_key"`
	Cases         []CoderCaseReceipt      `json:"cases"`
	Summary       map[string]CoderSummary `json:"summary"`
	// ToolsAtLeastAsGood reports whether the tool-using coder passed at least
	// as many tasks as the one-shot coder; absent unless both ran.
	ToolsAtLeastAsGood *bool `json:"tools_at_least_as_good,omitempty"`
}

// CoderCorpusIdentity names the coder corpus.
func CoderCorpusIdentity() (string, string) { return "coder-tasks-v1", digest(coderCorpusData) }

// RunCoders runs each selected coder on each selected task and scores it.
func RunCoders(ctx context.Context, options CoderOptions) (CoderReceipt, error) {
	cases, err := selectCoderCases(options)
	if err != nil {
		return CoderReceipt{}, err
	}
	run := options.run
	if run == nil {
		run = app.RunCoder
	}
	bug := options.Config.ModelScopes.Bug
	model, _ := json.Marshal([]string{string(bug.Provider), bug.Model})
	version, hash := CoderCorpusIdentity()
	receipt := CoderReceipt{Version: 1, Suite: "coder", CorpusVersion: version, CorpusDigest: hash, Runtime: runtimeIdentity(), ModelKey: digest(model), Cases: []CoderCaseReceipt{}, Summary: map[string]CoderSummary{}}
	for _, item := range cases {
		for _, coder := range options.Coders {
			if ctx.Err() != nil {
				return receipt, context.Cause(ctx)
			}
			result := runCoderCase(ctx, options.Config, item, coder, run)
			receipt.Cases = append(receipt.Cases, result)
			summary := receipt.Summary[coder]
			summary.Cases++
			if result.State == "passed" {
				summary.Passed++
			}
			summary.ModelCalls += result.Usage.ModelCalls
			summary.InputTokens += result.Usage.InputTokens
			summary.OutputTokens += result.Usage.OutputTokens
			summary.UnknownUsage += result.Usage.UnknownUsageCalls
			receipt.Summary[coder] = summary
		}
	}
	tools, toolsRan := receipt.Summary[CoderTools]
	oneShot, oneShotRan := receipt.Summary[CoderOneShot]
	if toolsRan && oneShotRan {
		better := tools.Passed >= oneShot.Passed
		receipt.ToolsAtLeastAsGood = &better
	}
	return receipt, nil
}

func selectCoderCases(options CoderOptions) ([]coderCase, error) {
	if !options.Confirmed || options.Config == nil {
		return nil, errors.New("the coder suite runs live models and project tests; it needs a configuration and explicit confirmation")
	}
	if len(options.Coders) == 0 || slices.ContainsFunc(options.Coders, func(coder string) bool { return coder != CoderTools && coder != CoderOneShot }) {
		return nil, fmt.Errorf("coders must be %s and/or %s", CoderTools, CoderOneShot)
	}
	corpus, err := decodeCoderCorpus(coderCorpusData)
	if err != nil {
		return nil, err
	}
	var selected []coderCase
	for _, item := range corpus.Cases {
		if options.CaseID == "" || item.ID == options.CaseID {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("select an available coder case")
	}
	return selected, nil
}

func decodeCoderCorpus(data []byte) (coderCorpus, error) {
	var corpus coderCorpus
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&corpus) != nil || decoder.Decode(new(any)) != io.EOF || corpus.Version != "coder-tasks-v1" || len(corpus.Cases) == 0 || len(corpus.Cases) > 32 {
		return corpus, errors.New("invalid versioned coder corpus")
	}
	seen := map[string]bool{}
	for _, item := range corpus.Cases {
		if !caseID.MatchString(item.ID) || seen[item.ID] || item.Request == "" || len(item.Paths) == 0 || len(item.Criteria) == 0 || len(item.Files) == 0 || len(item.Hidden) == 0 || len(item.Test) == 0 {
			return corpus, fmt.Errorf("invalid coder case %q", item.ID)
		}
		seen[item.ID] = true
	}
	return corpus, nil
}

func runCoderCase(ctx context.Context, cfg *config.Config, item coderCase, coder string, run func(context.Context, *config.Config, app.CoderTask, bool) (*app.CoderAttempt, error)) CoderCaseReceipt {
	receipt := CoderCaseReceipt{ID: item.ID, Language: item.Language, Coder: coder}
	started := time.Now()
	attempt, err := run(ctx, cfg, app.CoderTask{Files: item.Files, Kind: item.Kind, Title: item.Title, Request: item.Request, Paths: item.Paths, Criteria: item.Criteria}, coder == CoderTools)
	receipt.Duration = time.Since(started).Milliseconds()
	if attempt != nil {
		receipt.Usage, receipt.Tools, receipt.Changed = attempt.Usage, attempt.Stage, len(attempt.Changes)
	}
	if err != nil {
		receipt.State, receipt.Reason = "error", boundedReason(err.Error())
		return receipt
	}
	receipt.State, receipt.Reason, receipt.Failing = scoreCoderAttempt(ctx, item, attempt.Changes)
	return receipt
}

var secretLike = regexp.MustCompile(`(?i)(token|key|secret)[=:]\S+`)

func boundedReason(reason string) string {
	reason = secretLike.ReplaceAllString(reason, "$1=…")
	if len(reason) > 300 {
		reason = reason[:300] + "…"
	}
	return reason
}

// scoreCoderAttempt rebuilds the project with the coder's changes and the
// hidden tests in a scratch directory and runs the task's test command.
func scoreCoderAttempt(ctx context.Context, item coderCase, changes []app.CoderFile) (string, string, []string) {
	dir, err := os.MkdirTemp("", "kasuri-coder-score-")
	if err != nil {
		return "unavailable", err.Error(), nil
	}
	defer os.RemoveAll(dir)
	files := map[string]string{}
	for name, content := range item.Files {
		files[name] = content
	}
	for _, change := range changes {
		delete(files, change.OldPath)
		if change.Status == "deleted" {
			delete(files, change.Path)
			continue
		}
		files[change.Path] = change.Content
	}
	for name, content := range item.Hidden {
		files[name] = content
	}
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "unavailable", err.Error(), nil
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "unavailable", err.Error(), nil
		}
	}
	timed, cancel := context.WithTimeout(ctx, coderScoringTimeout)
	defer cancel()
	command := exec.CommandContext(timed, item.Test[0], item.Test[1:]...)
	command.Dir = dir
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err = command.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return "passed", "", nil
	case errors.As(err, &exit):
		failing := toolchain.Failures(output.String())
		return "failed", fmt.Sprintf("%s exited with status %d", strings.Join(item.Test, " "), exit.ExitCode()), failing
	default:
		return "unavailable", fmt.Sprintf("%s could not run: %v", item.Test[0], err), nil
	}
}
