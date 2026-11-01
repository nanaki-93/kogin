package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/app/archgate"
	"github.com/nanaki-93/kasuri/v2/internal/app/sandbox"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/toolchain"
)

const (
	maxCheckOutputBytes = sandbox.MaxOutputBytes
	// maxIssuesPerCheck bounds what one command reports; a linter that
	// prints hundreds of lines adds its first ones.
	maxIssuesPerCheck = 50
)

// checkSources is the finding source each command kind reports under.
var checkSources = map[string]string{checkKindLint: project.FindingSourceLint, checkKindTest: project.FindingSourceTest, checkKindSemgrep: project.FindingSourceSemgrep}

// projectCheckRun holds one run of the checks.
type projectCheckRun struct {
	s         *Service
	root      string
	revision  string
	index     *project.ProjectIndex
	hashes    map[string]string
	workspace string
	result    AnalysisChecks
	// reported holds each source's findings; complete marks the sources
	// whose every command ran, which alone may close issues they no longer
	// report.
	reported map[string][]project.UnifiedFinding
	complete map[string]bool
}

// runProjectChecks runs a plan's checks in a copy of the project, records
// what they report as issues and closes the ones they no longer report.
func (s *Service) runProjectChecks(ctx context.Context, revision string, plan AnalysisChecksPlan) AnalysisChecks {
	run := &projectCheckRun{s: s, root: s.manager.Root(), revision: revision, reported: map[string][]project.UnifiedFinding{}, complete: map[string]bool{},
		result: AnalysisChecks{Status: "completed", Checks: []AnalysisCheckResult{}}}
	index, err := s.manager.Index()
	if err != nil || index.ProjectRevision != revision {
		run.result.Status, run.result.Reason = "failed", checksCopyReason
		return run.finish()
	}
	run.index, run.hashes = index, scanFileHashes(index)
	run.addParse()
	run.addBudgets()
	if len(plan.Commands) > 0 {
		run.runCommands(ctx, plan.Commands)
	}
	if ctx.Err() != nil {
		run.result.Status = "canceled"
	}
	run.record()
	return run.finish()
}

func (run *projectCheckRun) finish() AnalysisChecks {
	run.result.At = time.Now().UTC()
	return run.result
}

func (run *projectCheckRun) add(name string, command []string, state string, findings []project.UnifiedFinding, source string) {
	run.result.Checks = append(run.result.Checks, AnalysisCheckResult{Name: name, Command: command, State: state, Issues: len(findings)})
	run.reported[source] = append(run.reported[source], findings...)
	run.result.Issues += len(findings)
}

func (run *projectCheckRun) addParse() {
	findings := parserScanFindings(run.index)
	run.add("parse", nil, scanPhaseState(findings), findings, project.FindingSourceParser)
	run.complete[project.FindingSourceParser] = true
}

// notCode are the languages of data and prose, which code budgets do not
// measure.
var notCode = map[string]bool{"": true, "Text": true, "Markdown": true, "YAML": true, "JSON": true, "TOML": true, "INI": true, "Config": true, "XML": true, "SQL": true}

// addBudgets reports every code file over the code budgets of its AGENTS.md
// files, whether or not a task changed it.
func (run *projectCheckRun) addBudgets() {
	var targets []archgate.ChangeTarget
	var edits []archgate.ChangeEdit
	for _, file := range run.index.Files {
		if file.Binary || notCode[file.Language] {
			continue
		}
		info, err := project.GetFileInfo(run.root, file.Path)
		if err != nil || info.Binary || info.ContentHash != file.ContentHash {
			continue
		}
		targets = append(targets, archgate.ChangeTarget{Path: file.Path, Content: info.Content, Exists: true})
		edits = append(edits, archgate.ChangeEdit{Path: file.Path, Content: info.Content})
	}
	var findings []project.UnifiedFinding
	for _, report := range archgate.BudgetReports(run.root, targets, edits) {
		if len(report.Debt) > 0 {
			finding := budgetDebtFinding(report)
			finding.FileHash = run.hashes[report.Path]
			findings = append(findings, finding)
		}
	}
	run.add("code budgets", nil, scanPhaseState(findings), findings, project.FindingSourceRules)
	run.complete[project.FindingSourceRules] = true
}

func (run *projectCheckRun) runCommands(ctx context.Context, commands []AnalysisCheckCommand) {
	for _, command := range commands {
		run.complete[checkSources[command.Kind]] = true
	}
	workspace, err := os.MkdirTemp("", "kasuri-checks-")
	if err == nil {
		defer os.RemoveAll(workspace)
		err = copyCheckWorkspace(ctx, run.root, workspace)
	}
	if err != nil {
		run.result.Reason = checksCopyReason
		for _, command := range commands {
			run.complete[checkSources[command.Kind]] = false
			run.result.Checks = append(run.result.Checks, AnalysisCheckResult{Name: command.Name, Command: command.Command, State: CheckUnavailable,
				Output: sanitizeCheckOutput(err.Error(), false, workspace, run.root)})
		}
		return
	}
	run.workspace = workspace
	for _, command := range commands {
		run.runCommand(ctx, command)
	}
}

func (run *projectCheckRun) runCommand(ctx context.Context, command AnalysisCheckCommand) {
	source := checkSources[command.Kind]
	executes := command.Kind != checkKindSemgrep && toolchain.ExecutesProjectCode(command.Command)
	if executes && run.s.requireProjectExecutionTrust(ctx, run.revision) != nil {
		run.complete[source] = false
		run.result.Checks = append(run.result.Checks, AnalysisCheckResult{Name: command.Name, Command: command.Command, State: CheckSkipped, Output: checksUntrustedReason})
		return
	}
	dir := filepath.Join(run.workspace, filepath.FromSlash(command.Dir))
	check, raw := run.s.runCheckWithOutput(ctx, dir, run.revision, command.Name, false, command.Command, false, executes)
	result := AnalysisCheckResult{Name: command.Name, Command: command.Command, State: check.State, Output: check.Output, DurationMS: check.DurationMS}
	var findings []project.UnifiedFinding
	var err error
	switch {
	case check.State == CheckCanceled:
		run.complete[source] = false
	case command.Kind == checkKindSemgrep:
		if findings, err = run.semgrepFindings(raw); err != nil {
			result.State, result.Output = CheckFailed, err.Error()
			run.complete[source] = false
		} else {
			result.State = scanPhaseState(findings)
		}
	case check.State == CheckFailed && command.Kind == checkKindLint:
		findings = run.lintFindings(command, raw)
	case check.State == CheckFailed:
		findings = run.testFindings(command, raw)
	}
	result.Issues = len(findings)
	run.reported[source] = append(run.reported[source], findings...)
	run.result.Issues += len(findings)
	run.result.Checks = append(run.result.Checks, result)
}

// record reconciles each source whose every command ran: issues they report
// open or stay open, and the ones no longer reported close. The legacy vet
// source of the Go scan closes with lint, which now runs go vet.
func (run *projectCheckRun) record() {
	store, err := project.NewFindingStore(run.root)
	if err != nil {
		return
	}
	input := project.FindingInput{ProjectID: run.index.ProjectID, ProjectRevision: run.index.ProjectRevision, FileHashes: run.hashes}
	if run.complete[project.FindingSourceLint] {
		run.complete[project.FindingSourceVet] = true
	}
	for _, source := range []string{project.FindingSourceParser, project.FindingSourceRules, project.FindingSourceLint, project.FindingSourceVet, project.FindingSourceTest, project.FindingSourceSemgrep} {
		if run.complete[source] {
			project.AttachSymbols(run.root, run.reported[source])
			_, _ = store.ReconcileSource(input, source, run.reported[source])
		}
	}
	run.s.notifyEvent("findings")
}

// resolve finds the indexed file a tool's path names: as printed, under the
// command's subproject, or the one indexed file that ends with it (go test
// prints file names without their package directory). hint narrows that last
// case to files whose directory it ends with.
func (run *projectCheckRun) resolve(dir, printed, hint string) string {
	printed = filepath.ToSlash(printed)
	if run.workspace != "" {
		printed = strings.TrimPrefix(printed, filepath.ToSlash(run.workspace)+"/")
	}
	printed = strings.TrimPrefix(printed, filepath.ToSlash(run.root)+"/")
	for _, candidate := range []string{path.Join(dir, printed), path.Clean(printed)} {
		if _, ok := run.hashes[candidate]; ok {
			return candidate
		}
	}
	match := ""
	for file := range run.hashes {
		if !strings.HasSuffix(file, "/"+printed) || dir != "" && !strings.HasPrefix(file, dir+"/") {
			continue
		}
		if hint != "" && strings.HasSuffix(hint, path.Dir(file)) {
			return file
		}
		if match != "" {
			return ""
		}
		match = file
	}
	return match
}

func (run *projectCheckRun) lintFindings(command AnalysisCheckCommand, raw string) []project.UnifiedFinding {
	var findings []project.UnifiedFinding
	for _, location := range toolchain.Locations(raw) {
		file := run.resolve(command.Dir, location.Path, "")
		if file == "" || location.Message == "" || len(findings) == maxIssuesPerCheck {
			continue
		}
		findings = append(findings, run.finding(project.FindingSourceLint, project.FindingCategoryMaintainability, "medium", command.Name, location.Message, location.Message,
			file, location.Line, sanitizeCheckOutput(location.Message, false, run.workspace, run.root)))
	}
	if len(findings) == 0 {
		findings = append(findings, run.commandFinding(project.FindingSourceLint, project.FindingCategoryMaintainability, command, raw))
	}
	return findings
}

func (run *projectCheckRun) testFindings(command AnalysisCheckCommand, raw string) []project.UnifiedFinding {
	var findings []project.UnifiedFinding
	for _, name := range toolchain.Failures(raw) {
		if len(findings) == maxIssuesPerCheck {
			break
		}
		file, line := "", 0
		for _, location := range toolchain.FailureLocations(raw, name) {
			if file = run.resolve(command.Dir, location.Path, name); file != "" {
				line = location.Line
				break
			}
		}
		findings = append(findings, run.finding(project.FindingSourceTest, project.FindingCategoryBugs, "high", command.Name, "Test fails: "+name,
			name+" fails in "+command.Name+".", file, line, sanitizeCheckOutput(toolchain.FailureOutput(raw, name), false, run.workspace, run.root)))
	}
	if len(findings) == 0 {
		findings = append(findings, run.commandFinding(project.FindingSourceTest, project.FindingCategoryBugs, command, raw))
	}
	return findings
}

// commandFinding reports a failed command whose output names no place.
func (run *projectCheckRun) commandFinding(source string, category project.FindingCategory, command AnalysisCheckCommand, raw string) project.UnifiedFinding {
	title := command.Name + " fails"
	return run.finding(source, category, "high", command.Name, title, fmt.Sprintf("%s fails: %s exits with an error.", command.Name, strings.Join(command.Command, " ")),
		"", 0, sanitizeCheckOutput(raw, len(raw) > maxCheckOutputBytes, run.workspace, run.root))
}

func (run *projectCheckRun) semgrepFindings(raw string) ([]project.UnifiedFinding, error) {
	results, err := toolchain.ParseSemgrep([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("semgrep printed output Kasuri could not read; run it in a terminal to see why")
	}
	var findings []project.UnifiedFinding
	for _, result := range results {
		file := run.resolve("", result.Path, "")
		if file == "" || len(findings) == maxIssuesPerCheck {
			continue
		}
		category := project.FindingCategoryBugs
		if result.Security {
			category = project.FindingCategorySecurity
		}
		title := result.Message
		if result.CWE != "" {
			title = result.Rule + " (" + result.CWE + ")"
		}
		finding := run.finding(project.FindingSourceSemgrep, category, result.Severity, result.Rule, title, result.Message, file, result.StartLine, result.Message)
		finding.Location.EndLine = max(result.EndLine, finding.Location.StartLine)
		findings = append(findings, finding)
	}
	return findings, nil
}

func (run *projectCheckRun) finding(source string, category project.FindingCategory, severity, rule, title, message, file string, line int, evidence string) project.UnifiedFinding {
	finding := project.UnifiedFinding{Source: source, Category: category, Confidence: project.FindingConfidenceToolReported, Severity: severity, Rule: rule,
		Title: truncateFindingText(title, 160), Message: truncateFindingText(message, 1000), Evidence: truncateFindingText(evidence, maxCheckOutputBytes),
		Location: project.FindingLocation{Path: file, StartLine: line, EndLine: line}}
	if file != "" {
		finding.FileHash = run.hashes[file]
	}
	return finding
}

func truncateFindingText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}

func scanFileHashes(index *project.ProjectIndex) map[string]string {
	hashes := make(map[string]string, len(index.Files))
	for _, file := range index.Files {
		hashes[file.Path] = file.ContentHash
	}
	return hashes
}

func parserScanFindings(index *project.ProjectIndex) []project.UnifiedFinding {
	findings := []project.UnifiedFinding{}
	for _, file := range index.Files {
		for _, diagnostic := range file.Diagnostics {
			findings = append(findings, project.UnifiedFinding{Source: project.FindingSourceParser, Confidence: project.FindingConfidenceToolReported, Severity: "high", Title: "Parser diagnostic", Message: diagnostic.Message, Rule: "parse", FileHash: file.ContentHash, Location: project.FindingLocation{Path: file.Path, StartLine: diagnostic.Line, EndLine: diagnostic.Line}, Evidence: diagnostic.Message})
		}
	}
	return findings
}

func scanPhaseState(findings []project.UnifiedFinding) string {
	if len(findings) == 0 {
		return CheckPassed
	}
	return CheckFailed
}
