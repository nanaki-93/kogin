package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/logging"
	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

// Health tests run the project's tests in a copy of the project, as checks
// do: Go modules with coverage (go test -cover ./...), other subprojects
// with their own test command, which reports pass or fail only.

const healthTestsRelativePath = ".kogin/health/tests.json"

// ErrHealthTestsBusy refuses a second test run while one is running.
var ErrHealthTestsBusy = errors.New("the project's tests are already running")

// HealthTestRun is the latest run of the project's tests.
type HealthTestRun struct {
	Revision string `json:"revision"`
	// Current reports that the run is of the project as it is now.
	Current    bool      `json:"current"`
	RanAt      time.Time `json:"ran_at"`
	DurationMS int64     `json:"duration_ms"`
	Passed     bool      `json:"passed"`
	// Coverage is the share of Go statements the tests run, in percent;
	// nil without a Go module.
	Coverage *float64            `json:"coverage"`
	Packages []HealthPackage     `json:"packages"`
	Commands []HealthTestCommand `json:"commands"`
}

// HealthPackage is one Go package's test result.
type HealthPackage struct {
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	NoTests  bool     `json:"no_tests,omitempty"`
	Coverage *float64 `json:"coverage"`
}

// HealthTestCommand is one command the run ran.
type HealthTestCommand struct {
	Dir     string   `json:"dir"`
	Command []string `json:"command"`
	State   string   `json:"state"`
	// Output is the end of a failed command's output.
	Output     string `json:"output,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type storedHealthTests struct {
	SchemaVersion string        `json:"schema_version"`
	Run           HealthTestRun `json:"run"`
}

var healthTestsRunning = struct {
	sync.Mutex
	roots map[string]bool
}{roots: map[string]bool{}}

// RunHealthTests starts a run of the project's tests in the background; the
// health shows it running, then its result. It needs execution trust.
func (s *Service) RunHealthTests(ctx context.Context, revision string) error {
	if err := s.requireProjectExecutionTrust(ctx, revision); err != nil {
		return err
	}
	root := s.manager.Root()
	healthTestsRunning.Lock()
	if healthTestsRunning.roots[root] {
		healthTestsRunning.Unlock()
		return ErrHealthTestsBusy
	}
	healthTestsRunning.roots[root] = true
	healthTestsRunning.Unlock()
	go func() {
		defer func() {
			healthTestsRunning.Lock()
			delete(healthTestsRunning.roots, root)
			healthTestsRunning.Unlock()
		}()
		run := s.runHealthTests(context.Background(), root, revision)
		if err := storeHealthTests(root, run); err != nil {
			logging.Error("Could not save the test run", "error", err)
		}
	}()
	return nil
}

func (s *Service) runHealthTests(ctx context.Context, root, revision string) HealthTestRun {
	started := time.Now()
	run := HealthTestRun{Revision: revision, RanAt: started.UTC(), Passed: true, Packages: []HealthPackage{}, Commands: []HealthTestCommand{}}
	workspace, err := os.MkdirTemp("", "kogin-health-tests-")
	if err != nil {
		return failedHealthRun(run, err)
	}
	defer os.RemoveAll(workspace)
	if err := copyCheckWorkspace(ctx, root, workspace); err != nil {
		return failedHealthRun(run, err)
	}
	var covered, statements int
	goDirs := map[string]bool{}
	for _, profile := range s.projectProfiles(root) {
		dir := filepath.Join(workspace, filepath.FromSlash(profile.Dir))
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if goDirs[profile.Dir] {
				continue
			}
			goDirs[profile.Dir] = true
			profileFile := filepath.Join(dir, ".kogin-coverage.out")
			command := []string{"go", "test", "-cover", "-coverprofile=" + profileFile, "./..."}
			result, raw := s.healthCommand(ctx, dir, revision, profile, command)
			run.Commands = append(run.Commands, result)
			run.Passed = run.Passed && result.State == CheckPassed
			run.Packages = append(run.Packages, parseGoTestCover(raw, goModulePath(dir), profile.Dir)...)
			c, n := coverProfileTotals(profileFile)
			covered, statements = covered+c, statements+n
			continue
		}
		if len(profile.Test) == 0 {
			continue
		}
		result, _ := s.healthCommand(ctx, dir, revision, profile, profile.Test)
		run.Commands = append(run.Commands, result)
		run.Passed = run.Passed && result.State == CheckPassed
	}
	if statements > 0 {
		total := roundPercent(float64(covered) * 100 / float64(statements))
		run.Coverage = &total
	}
	sort.Slice(run.Packages, func(i, j int) bool { return run.Packages[i].Name < run.Packages[j].Name })
	run.DurationMS = time.Since(started).Milliseconds()
	return run
}

func (s *Service) healthCommand(ctx context.Context, dir, revision string, profile toolchain.Profile, command []string) (HealthTestCommand, string) {
	check, raw := s.runCheckWithOutput(ctx, dir, revision, "tests", true, command, false, true)
	shown := append([]string{}, command...)
	for i, arg := range shown {
		if strings.HasPrefix(arg, "-coverprofile=") {
			shown[i] = "-coverprofile=coverage.out"
		}
	}
	result := HealthTestCommand{Dir: profile.Dir, Command: shown, State: check.State, DurationMS: check.DurationMS}
	if check.State != CheckPassed {
		result.Output = tailLines(check.Output, 40)
	}
	return result, raw
}

func failedHealthRun(run HealthTestRun, err error) HealthTestRun {
	run.Passed = false
	run.Commands = append(run.Commands, HealthTestCommand{State: CheckFailed, Output: err.Error()})
	return run
}

// parseGoTestCover reads `go test -cover ./...` output: one line per
// package, with its coverage when it has tests.
func parseGoTestCover(out, module, dir string) []HealthPackage {
	var packages []HealthPackage
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		status, name := fields[0], fields[1]
		if status != "ok" && status != "FAIL" && status != "?" {
			if !strings.Contains(scanner.Text(), "coverage:") || strings.HasPrefix(status, "---") {
				continue
			}
			status, name = "", fields[0]
		}
		if !strings.Contains(name, "/") && name != module && !strings.Contains(name, ".") {
			continue
		}
		pkg := HealthPackage{Name: packageName(name, module, dir), Passed: status != "FAIL", NoTests: status == "?" || status == ""}
		if index := strings.Index(scanner.Text(), "coverage: "); index >= 0 {
			value := strings.TrimSuffix(strings.Fields(scanner.Text()[index+len("coverage: "):])[0], "%")
			if percent, err := strconv.ParseFloat(value, 64); err == nil {
				pkg.Coverage = &percent
			}
		}
		if !seen[pkg.Name] {
			seen[pkg.Name] = true
			packages = append(packages, pkg)
		}
	}
	return packages
}

// packageName is a package's directory in the project ("." for the root).
func packageName(name, module, dir string) string {
	relative := strings.TrimPrefix(strings.TrimPrefix(name, module), "/")
	if module == "" || !strings.HasPrefix(name, module) {
		relative = name
	}
	if joined := path.Join(dir, relative); joined != "" {
		return joined
	}
	return "."
}

func goModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

// coverProfileTotals counts the covered and all statements of a Go cover
// profile; a block listed twice counts once, covered if either run hit it.
func coverProfileTotals(file string) (int, int) {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, 0
	}
	type block struct{ statements, count int }
	blocks := map[string]block{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		fields := strings.Fields(string(line))
		if len(fields) != 3 || fields[0] == "mode:" {
			continue
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		previous := blocks[fields[0]]
		blocks[fields[0]] = block{statements: statements, count: max(previous.count, count)}
	}
	covered, total := 0, 0
	for _, b := range blocks {
		total += b.statements
		if b.count > 0 {
			covered += b.statements
		}
	}
	return covered, total
}

func roundPercent(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// lowCoverage are the Go packages with statements that tests cover least,
// under 40%, least first.
func lowCoverage(run *HealthTestRun) []HealthPackage {
	var low []HealthPackage
	for _, pkg := range run.Packages {
		if pkg.Coverage != nil && *pkg.Coverage < 40 {
			low = append(low, pkg)
		}
	}
	sort.SliceStable(low, func(i, j int) bool { return *low[i].Coverage < *low[j].Coverage })
	return low
}

// healthTestRun is the stored test run, marked current when it ran on the
// project's revision, and whether a run is in progress.
func (s *Service) healthTestRun(root, revision string) (*HealthTestRun, bool) {
	healthTestsRunning.Lock()
	running := healthTestsRunning.roots[root]
	healthTestsRunning.Unlock()
	run, err := loadHealthTests(root)
	if err != nil || run == nil {
		return nil, running
	}
	run.Current = run.Revision == revision
	return run, running
}

func storeHealthTests(root string, run HealthTestRun) error {
	file, err := storage.MetadataPath(root, healthTestsRelativePath)
	if err != nil {
		return err
	}
	data, err := json.Marshal(storedHealthTests{SchemaVersion: "1", Run: run})
	if err != nil {
		return err
	}
	return storage.WriteFile(file, data, 0o600)
}

func loadHealthTests(root string) (*HealthTestRun, error) {
	file, err := storage.MetadataPath(root, healthTestsRelativePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored storedHealthTests
	if err := json.Unmarshal(data, &stored); err != nil || stored.SchemaVersion != "1" {
		return nil, fmt.Errorf("the saved test run is unreadable; run the tests again")
	}
	return &stored.Run, nil
}
