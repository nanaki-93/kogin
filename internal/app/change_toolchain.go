package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nanaki-93/kogin/v2/internal/app/archgate"
	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
)

// projectProfiles finds how each subproject under root is tested and linted.
func (s *Service) projectProfiles(root string) []toolchain.Profile {
	var files []string
	if index, err := s.manager.Index(); err == nil && s.manager.Root() == root {
		for _, file := range index.Files {
			files = append(files, file.Path)
		}
	}
	// The index leaves out some files without an extension; the root's own
	// manifests are always looked at.
	for _, name := range []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py", "setup.cfg", "pytest.ini", "pom.xml", "build.gradle", "build.gradle.kts", "justfile", "Justfile", "Taskfile.yml", "Taskfile.yaml", "Makefile", "AGENTS.md"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && info.Mode().IsRegular() {
			files = append(files, name)
		}
	}
	return toolchain.Detect(root, files, rules.NewResolver(root))
}

// changedPaths lists every path a proposal touches.
func changedPaths(edits []ChangeEdit) []string {
	paths := make([]string, 0, len(edits))
	for _, edit := range edits {
		paths = append(paths, edit.Path)
	}
	return paths
}

// profileCheckName names a check after its subproject: "tests" for the root,
// "tests: web" for web/.
func profileCheckName(kind string, profile toolchain.Profile) string {
	if profile.Dir == "" {
		return kind
	}
	return kind + ": " + profile.Dir
}

// checkRun runs a task's checks per affected subproject. A failure is
// compared with the same command on the tree before the proposal, so tests
// or lint findings that already failed there do not block the task.
type checkRun struct {
	s           *Service
	ctx         context.Context
	root        string
	revision    string
	fingerprint string
	session     *ChangeSession
	// revert turns a copy of root into the tree before the change; nil when
	// root is that tree (a task in the main checkout holds no proposal).
	revert func(dir string) error
	// baseKey names that tree for the baseline cache, besides fingerprint.
	baseKey string
	baseDir string
	baseErr error
}

func (run *checkRun) cleanup() {
	if run.baseDir != "" {
		_ = os.RemoveAll(run.baseDir)
	}
}

// base returns a copy of the tree the proposal was made against.
func (run *checkRun) base() (string, error) {
	if run.baseDir != "" || run.baseErr != nil {
		return run.baseDir, run.baseErr
	}
	dir, err := os.MkdirTemp("", "kogin-change-base-")
	if err != nil {
		run.baseErr = err
		return "", err
	}
	run.baseDir = dir
	if err := copyCheckWorkspace(run.ctx, run.root, dir); err != nil {
		run.baseErr = err
		return "", err
	}
	fingerprint, err := fingerprintCheckWorkspace(run.ctx, dir)
	if err == nil && fingerprint != run.fingerprint {
		err = fmt.Errorf("%w: the project changed while checks ran", project.ErrRevisionConflict)
	}
	if err == nil && run.revert != nil {
		err = run.revert(dir)
	}
	run.baseErr = err
	return dir, err
}

// baselineResult is a check command's outcome on the tree before a proposal.
type baselineResult struct {
	check  CheckResult
	output string
}

// checkBaselines remembers baseline runs by tree fingerprint and command, so
// repeated checks of a task do not run an unchanged baseline again.
type checkBaselines struct {
	mu      sync.Mutex
	results map[string]baselineResult
	order   []string
}

const maxCheckBaselines = 32

func (b *checkBaselines) get(key string) (baselineResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, ok := b.results[key]
	return result, ok
}

func (b *checkBaselines) put(key string, result baselineResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.results == nil {
		b.results = map[string]baselineResult{}
	}
	if _, known := b.results[key]; !known {
		b.order = append(b.order, key)
	}
	b.results[key] = result
	for len(b.order) > maxCheckBaselines {
		delete(b.results, b.order[0])
		b.order = b.order[1:]
	}
}

// baseline runs a command on the tree before the proposal, once per tree.
func (run *checkRun) baseline(profile toolchain.Profile, name string, command []string) (baselineResult, bool) {
	key := run.fingerprint + "\x00" + run.baseKey + "\x00" + profile.Dir + "\x00" + strings.Join(command, "\x00")
	if result, ok := run.s.checkBaselines.get(key); ok {
		return result, true
	}
	dir, err := run.base()
	if err != nil {
		return baselineResult{}, false
	}
	workdir := filepath.Join(dir, filepath.FromSlash(profile.Dir))
	check, output := run.s.runCheckWithOutput(run.ctx, workdir, run.revision, name+" before this change", false, command, false, toolchain.ExecutesProjectCode(command))
	if check.State == CheckCanceled || check.State == CheckUnavailable {
		return baselineResult{}, false
	}
	result := baselineResult{check: check, output: output}
	run.s.checkBaselines.put(key, result)
	return result, true
}

// compared runs a test or lint command on the proposal. When it fails, the
// failures are compared with the same command before the proposal: only new
// failures block. readFailures names failures in the command's output.
func (run *checkRun) compared(workspace string, profile toolchain.Profile, kind string, command []string, readFailures func(string) []string) CheckResult {
	name := profileCheckName(kind, profile)
	workdir := filepath.Join(workspace, filepath.FromSlash(profile.Dir))
	after, output := run.s.runCheckWithOutput(run.ctx, workdir, run.revision, name, true, command, false, toolchain.ExecutesProjectCode(command))
	if after.State != CheckFailed {
		return after
	}
	before, ok := run.baseline(profile, kind, command)
	if !ok {
		return after
	}
	if before.check.State == CheckPassed {
		after.Output = "This passed before the change.\n\n" + after.Output
		return after
	}
	known, found := readFailures(before.output), readFailures(output)
	added := toolchain.NewFailures(known, found)
	switch {
	case len(known) > 0 && len(found) > 0 && len(added) == 0:
		after.State, after.Outcome = CheckPassed, "known_failures"
		after.Output = fmt.Sprintf("Only failures that already happened before this change (%s); they do not block this task.\n\n%s", listNames(found), after.Output)
	case len(known) > 0 && len(found) > 0:
		after.Output = fmt.Sprintf("New failures: %s. Already failing before this change: %s.\n\n%s", listNames(added), listNames(known), after.Output)
	default:
		after.Outcome = "also_failed_before"
		after.Output = "This also failed before the change, and Kogin could not tell which failures are new, so it still blocks. Fix or skip the failing checks, or declare a narrower command in AGENTS.md.\n\n" + after.Output
	}
	return after
}

func listNames(names []string) string {
	const shown = 8
	if len(names) <= shown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:shown], ", ") + fmt.Sprintf(" and %d more", len(names)-shown)
}

// regressionBaselines runs each affected subproject's tests on the tree
// before the proposal with only the new test files added: a new test that
// fails there reproduces what the task fixes or adds.
func (run *checkRun) regressionBaselines(affected []toolchain.Profile, newTests []ChangeEdit) ([]CheckResult, error) {
	var relevant []toolchain.Profile
	known := map[string][]string{}
	// Every baseline runs before the new tests are added to the base tree.
	for _, profile := range affected {
		if len(profile.Test) == 0 || !coversAny(profile, newTests) {
			continue
		}
		relevant = append(relevant, profile)
		if before, ok := run.baseline(profile, "tests", profile.Test); ok {
			known[profile.Dir] = toolchain.Failures(before.output)
		}
	}
	if len(relevant) == 0 {
		return nil, nil
	}
	dir, err := run.base()
	if err != nil {
		return nil, nil
	}
	if err := stageChangeEdits(dir, newTests); err != nil {
		return nil, err
	}
	var checks []CheckResult
	for _, profile := range relevant {
		known := known[profile.Dir]
		check, output := run.s.runCheckWithOutput(run.ctx, filepath.Join(dir, filepath.FromSlash(profile.Dir)), run.revision, profileCheckName("regression baseline", profile), false, profile.Test, false, true)
		classifyRegressionBaseline(&check, func(CheckResult) bool { return newTestsReproduced(known, output) })
		checks = append(checks, check)
	}
	return checks, nil
}

func coversAny(profile toolchain.Profile, edits []ChangeEdit) bool {
	for _, edit := range edits {
		if profile.Covers(edit.Path) {
			return true
		}
	}
	return false
}

// runProfileChecks stages the proposal into workspace and runs the lint and
// test commands of every subproject it touches.
func (s *Service) runProfileChecks(run *checkRun, workspace string, options CheckOptions) ([]CheckResult, error) {
	session := run.session
	profiles := s.projectProfiles(run.root)
	affected := toolchain.Affected(profiles, changedPaths(session.Changes))
	if err := stageChangeEdits(workspace, session.Changes); err != nil {
		return nil, err
	}
	checks := run.commandChecks(workspace, affected, options)
	if options.RunTests && !anyTestCommand(affected) {
		checks = append(checks, CheckResult{Name: "tests", Required: true, State: CheckUnavailable, Outcome: "could_not_execute",
			Output: "No test command covers the changed files. Declare one under checks in the AGENTS.md of their subproject, or add a test target or the ecosystem's test setup."})
	}
	if note := unaffectedNote(profiles, affected); note != "" && (options.RunTests || options.RunLint) {
		checks = append(checks, CheckResult{Name: "other subprojects", Required: false, State: CheckSkipped, Outcome: "not_affected", Output: note})
	}
	if options.RunTests {
		regression, err := run.regressionBaselines(affected, newChangeTestEdits(session))
		if err != nil {
			return nil, err
		}
		checks = append(checks, regression...)
	}
	return checks, run.ctx.Err()
}

// commandChecks runs the lint and test commands of each affected subproject,
// and gitleaks when the project configures it.
func (run *checkRun) commandChecks(workspace string, affected []toolchain.Profile, options CheckOptions) []CheckResult {
	var checks []CheckResult
	if options.RunLint && archgate.HasOptionalGitleaks(workspace) {
		checks = append(checks, run.s.runCheck(run.ctx, workspace, run.revision, "gitleaks", true, []string{"gitleaks", "detect", "--no-git", "--source", ".", "--redact"}, false, true))
	}
	for _, profile := range affected {
		if options.RunLint && len(profile.Lint) > 0 {
			checks = append(checks, run.compared(workspace, profile, "lint", profile.Lint, toolchain.Diagnostics))
		}
		if options.RunTests && len(profile.Test) > 0 {
			checks = append(checks, run.compared(workspace, profile, "tests", profile.Test, toolchain.Failures))
		}
	}
	return checks
}

func anyTestCommand(profiles []toolchain.Profile) bool {
	for _, profile := range profiles {
		if len(profile.Test) > 0 {
			return true
		}
	}
	return false
}

func unaffectedNote(profiles, affected []toolchain.Profile) string {
	ran := map[string]bool{}
	for _, profile := range affected {
		ran[profile.Dir] = true
	}
	var skipped []string
	for _, profile := range profiles {
		if !ran[profile.Dir] {
			label := profile.Dir
			if label == "" {
				label = "the root project"
			}
			skipped = append(skipped, label)
		}
	}
	if len(skipped) == 0 {
		return ""
	}
	return "Not run, since no changed file is in them: " + strings.Join(skipped, ", ") + "."
}

// profileCommands lists the commands of every subproject, for the execution
// trust the user grants.
func profileCommands(profiles []toolchain.Profile) [][]string {
	var commands [][]string
	for _, profile := range profiles {
		for _, command := range [][]string{profile.Test, profile.Lint} {
			if len(command) == 0 {
				continue
			}
			display := append([]string(nil), command...)
			if profile.Dir != "" {
				display = append([]string{"(in " + profile.Dir + ")"}, display...)
			}
			commands = append(commands, display)
		}
	}
	return commands
}
