package app

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/dupes"
	"github.com/nanaki-93/kogin/v2/internal/project/rules"
	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
)

// Project health answers "is this repository fit for its goal, and is the
// work converging?" from facts Kogin can measure without a model: the open
// issues at or above the finish line, the tests as they run, repeated code,
// code nothing calls, functions over their budgets, and whether fixes find
// more issues than they close.

// ProjectHealth is the active project's health.
type ProjectHealth struct {
	Profile    ProjectProfile   `json:"profile"`
	FinishLine HealthFinishLine `json:"finish_line"`
	Code       HealthCode       `json:"code"`
	Tests      *HealthTestRun   `json:"tests"`
	// TestsRunning reports a test run in progress.
	TestsRunning bool                   `json:"tests_running"`
	Unused       []HealthUnusedFunction `json:"unused"`
	Duplication  HealthDuplication      `json:"duplication"`
	Complexity   []HealthComplexity     `json:"complexity"`
	Trend        HealthTrend            `json:"trend"`
	// Tasks are drafts that would improve the health, best first.
	Tasks []HealthTask `json:"tasks"`
}

// HealthFinishLine is how far the project is from done: no open issue at or
// above the profile's finish line, and every test passing.
type HealthFinishLine struct {
	Severity string `json:"severity"`
	// Open counts the open issues that count as work, by severity.
	Open       int            `json:"open"`
	BySeverity map[string]int `json:"by_severity"`
	// OutOfScope counts the open issues the profile leaves out, by reason.
	OutOfScope map[string]int `json:"out_of_scope"`
	// TestsPass is nil until tests ran on this revision.
	TestsPass *bool `json:"tests_pass"`
	Done      bool  `json:"done"`
}

// HealthCode is the size of the code and its tests.
type HealthCode struct {
	SourceFiles int `json:"source_files"`
	SourceLines int `json:"source_lines"`
	TestFiles   int `json:"test_files"`
	TestLines   int `json:"test_lines"`
	// TestDirs are directories with more than two test files per source
	// file, most first.
	TestDirs []HealthTestDir `json:"test_dirs"`
}

// HealthTestDir is a directory whose tests are spread over many files.
type HealthTestDir struct {
	Dir         string `json:"dir"`
	SourceFiles int    `json:"source_files"`
	TestFiles   int    `json:"test_files"`
	TestLines   int    `json:"test_lines"`
}

// HealthUnusedFunction is a function nothing calls.
type HealthUnusedFunction struct {
	Path      string `json:"path"`
	Function  string `json:"function"`
	StartLine int    `json:"start_line"`
	TestOnly  bool   `json:"test_only,omitempty"`
}

// HealthDuplication is the code repeated across the source files. Tests
// are measured apart: some repeated setup there is cheaper than an
// abstraction, but a lot of it means tests were copied instead of extended.
type HealthDuplication struct {
	RepeatedLines int            `json:"repeated_lines"`
	TotalLines    int            `json:"total_lines"`
	Groups        []dupes.Group  `json:"groups"`
	ByFile        map[string]int `json:"by_file,omitempty"`
	// TestRepeatedLines and TestTotalLines are the same measure over tests.
	TestRepeatedLines int `json:"test_repeated_lines"`
	TestTotalLines    int `json:"test_total_lines"`
}

// HealthComplexity is a function over its complexity or length budget.
type HealthComplexity struct {
	Path       string `json:"path"`
	Function   string `json:"function"`
	StartLine  int    `json:"start_line"`
	Complexity int    `json:"complexity"`
	Lines      int    `json:"lines"`
	// Budget is the complexity budget; LineBudget the length budget (0: none).
	Budget     int `json:"budget"`
	LineBudget int `json:"line_budget,omitempty"`
}

// HealthTask is a task that would improve the health, drafted for the
// composer; nothing is saved until the user creates it.
type HealthTask struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	Why   string    `json:"why"`
	Draft IssueTask `json:"draft"`
}

const (
	maxHealthGroups     = 20
	maxHealthComplexity = 30
	maxHealthUnused     = 100
)

// ProjectHealth measures the active project. It reads every source file
// once; tests run only on request (RunHealthTests).
func (s *Service) ProjectHealth(ctx context.Context) (*ProjectHealth, error) {
	analysis, index, input, err := s.workspaceState()
	if err != nil {
		return nil, err
	}
	root := s.manager.Root()
	profile := s.activeProfile()
	findings, err := s.listFindings(analysis, index, input, FindingFilter{})
	if err != nil {
		return nil, err
	}
	sources, err := healthSources(ctx, root, index)
	if err != nil {
		return nil, err
	}
	health := &ProjectHealth{Profile: profile, Unused: []HealthUnusedFunction{}, Complexity: []HealthComplexity{}, Tasks: []HealthTask{}}
	health.Code = measureCode(root, sources)
	health.Duplication = measureDuplication(sources)
	health.Complexity = measureComplexity(root, sources)
	for _, fn := range s.unusedCode(index) {
		if len(health.Unused) < maxHealthUnused {
			health.Unused = append(health.Unused, HealthUnusedFunction{Path: fn.Path, Function: fn.Function, StartLine: fn.StartLine, TestOnly: fn.TestOnly})
		}
	}
	health.Tests, health.TestsRunning = s.healthTestRun(root, index.ProjectRevision)
	health.FinishLine = finishLine(profile, findings, health.Tests)
	completions, err := s.FindingCompletions()
	if err != nil {
		return nil, err
	}
	health.Trend = healthTrend(ctx, root, findings, completions)
	health.Tasks = healthTasks(root, health, sources, findings)
	return health, nil
}

// healthSource is one indexed source file and its content.
type healthSource struct {
	path    string
	content string
	// lines counts non-blank lines; fileLines every line, as file budgets do.
	lines     int
	fileLines int
	test      bool
}

func healthSources(ctx context.Context, root string, index *project.ProjectIndex) ([]healthSource, error) {
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return nil, err
	}
	resolver := rules.NewResolver(root)
	var sources []healthSource
	for _, file := range index.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.Binary || !hasSourceExtension(file.Path) || !policy.Decide(file.Path).Include {
			continue
		}
		info, err := project.GetFileInfo(root, file.Path)
		if err != nil || info.Binary {
			continue
		}
		sources = append(sources, healthSource{path: file.Path, content: info.Content, lines: codeLineCount(info.Content), fileLines: fileLineCount(info.Content), test: resolver.IsTestFile(file.Path)})
	}
	return sources, nil
}

func measureCode(root string, sources []healthSource) HealthCode {
	code := HealthCode{TestDirs: []HealthTestDir{}}
	dirs := map[string]*HealthTestDir{}
	for _, source := range sources {
		dir := path.Dir(source.path)
		if dirs[dir] == nil {
			dirs[dir] = &HealthTestDir{Dir: dir}
		}
		if source.test {
			code.TestFiles++
			code.TestLines += source.lines
			dirs[dir].TestFiles++
			dirs[dir].TestLines += source.lines
		} else {
			code.SourceFiles++
			code.SourceLines += source.lines
			dirs[dir].SourceFiles++
		}
	}
	for _, dir := range dirs {
		if dir.TestFiles > max(2, 2*dir.SourceFiles) {
			code.TestDirs = append(code.TestDirs, *dir)
		}
	}
	sort.Slice(code.TestDirs, func(i, j int) bool {
		return code.TestDirs[i].TestFiles-2*code.TestDirs[i].SourceFiles > code.TestDirs[j].TestFiles-2*code.TestDirs[j].SourceFiles
	})
	return code
}

func measureDuplication(sources []healthSource) HealthDuplication {
	var files, tests []dupes.File
	for _, source := range sources {
		if source.test {
			tests = append(tests, dupes.File{Path: source.path, Content: source.content})
		} else {
			files = append(files, dupes.File{Path: source.path, Content: source.content})
		}
	}
	report, testReport := dupes.Find(files), dupes.Find(tests)
	duplication := HealthDuplication{RepeatedLines: report.RepeatedLines, TotalLines: report.TotalLines, Groups: report.Groups, ByFile: map[string]int{},
		TestRepeatedLines: testReport.RepeatedLines, TestTotalLines: testReport.TotalLines}
	for _, group := range report.Groups {
		for _, place := range group.Places {
			duplication.ByFile[place.Path] += group.Lines
		}
	}
	if len(duplication.Groups) > maxHealthGroups {
		duplication.Groups = duplication.Groups[:maxHealthGroups]
	}
	if duplication.Groups == nil {
		duplication.Groups = []dupes.Group{}
	}
	return duplication
}

// measureComplexity lists the source functions over their AGENTS.md
// budgets (complexity 15 when none is set), worst first.
func measureComplexity(root string, sources []healthSource) []HealthComplexity {
	resolver := rules.NewResolver(root)
	result := []HealthComplexity{}
	for _, source := range sources {
		if source.test {
			continue
		}
		budgets := resolver.For(source.path).Budgets
		complexity := budgets.FunctionComplexity
		if complexity == 0 {
			complexity = rules.DefaultFunctionComplexity
		}
		for _, fn := range syntax.Analyze(source.path, []byte(source.content)).Functions {
			if fn.Complexity > complexity || budgets.FunctionLines > 0 && fn.Lines() > budgets.FunctionLines {
				result = append(result, HealthComplexity{Path: source.path, Function: fn.Name, StartLine: fn.StartLine, Complexity: fn.Complexity, Lines: fn.Lines(), Budget: complexity, LineBudget: budgets.FunctionLines})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Complexity-result[i].Budget > result[j].Complexity-result[j].Budget
	})
	if len(result) > maxHealthComplexity {
		result = result[:maxHealthComplexity]
	}
	return result
}

// finishLine counts the open issues that are still work.
func finishLine(profile ProjectProfile, findings []project.UnifiedFinding, tests *HealthTestRun) HealthFinishLine {
	line := HealthFinishLine{Severity: profile.FinishLine, BySeverity: map[string]int{}, OutOfScope: map[string]int{}}
	for _, finding := range findings {
		if finding.Status != project.FindingStatusOpen || finding.Freshness == project.FindingFreshnessStale {
			continue
		}
		if finding.OutOfScope != nil {
			line.OutOfScope[finding.OutOfScope.Reason]++
			continue
		}
		line.Open++
		line.BySeverity[strings.ToLower(finding.Severity)]++
	}
	if tests != nil && tests.Current {
		pass := tests.Passed
		line.TestsPass = &pass
	}
	line.Done = line.Open == 0 && line.TestsPass != nil && *line.TestsPass
	return line
}

// healthTasks drafts the tasks that would improve the health most: delete
// unused code, merge repeated blocks, gather spread tests and split the
// most complex functions. Each is small enough for one task.
func healthTasks(root string, health *ProjectHealth, sources []healthSource, findings []project.UnifiedFinding) []HealthTask {
	var tasks []HealthTask
	unusedFiles := map[string][]string{}
	for _, fn := range health.Unused {
		unusedFiles[fn.Path] = append(unusedFiles[fn.Path], fn.Function)
	}
	if len(unusedFiles) > 0 {
		tasks = append(tasks, unusedTask(root, unusedFiles, findings))
	}
	for i, group := range health.Duplication.Groups {
		if i == 3 {
			break
		}
		tasks = append(tasks, duplicationTask(root, group))
	}
	for i, group := range spreadTestGroups(root, health.Code.TestDirs, sources) {
		if i == 3 {
			break
		}
		tasks = append(tasks, testGatherTask(root, group))
	}
	for i, fn := range health.Complexity {
		if i == 3 {
			break
		}
		tasks = append(tasks, complexityTask(root, fn))
	}
	if health.Tests != nil {
		for i, pkg := range lowCoverage(health.Tests) {
			if i == 2 {
				break
			}
			tasks = append(tasks, coverageTask(pkg))
		}
	}
	return tasks
}

func unusedTask(root string, files map[string][]string, findings []project.UnifiedFinding) HealthTask {
	paths := slices.Sorted(maps.Keys(files))
	count := 0
	var lines []string
	for _, file := range paths {
		count += len(files[file])
		lines = append(lines, fmt.Sprintf("- %s: %s", file, joinNames(quoted(files[file]))))
	}
	limited := paths
	if len(limited) > 8 {
		limited = limited[:8]
	}
	draft := IssueTask{Kind: "fix", Title: fmt.Sprintf("Delete code nothing calls (%d %s)", count, plural(count, "function", "functions")), Paths: append([]string{}, limited...),
		Description:        "Nothing calls these functions from main, init or package-level code. Delete them, with the tests that only call them:\n" + strings.Join(lines, "\n"),
		AcceptanceCriteria: []string{"The listed functions are deleted, with tests that only call them", "The unused code check passes and no other behavior changes"}}
	for _, finding := range findings {
		if finding.Source == project.FindingSourceUnused && finding.Status == project.FindingStatusOpen && slices.Contains(limited, finding.Location.Path) {
			draft.FindingTargets = append(draft.FindingTargets, project.FindingTarget{ProjectID: finding.ProjectID, Kind: "semantic", ID: finding.ID, Path: finding.Location.Path})
		}
	}
	return HealthTask{ID: "unused", Kind: "unused", Title: draft.Title, Why: "Code that never runs still has to be read, analyzed and kept compiling.", Draft: draft}
}

func duplicationTask(root string, group dupes.Group) HealthTask {
	var places, paths []string
	for _, place := range group.Places {
		places = append(places, fmt.Sprintf("- %s:%d-%d", place.Path, place.StartLine, place.EndLine))
		if !slices.Contains(paths, place.Path) {
			paths = append(paths, place.Path)
		}
	}
	title := fmt.Sprintf("Merge a block repeated %d times (%d lines)", len(group.Places), group.Lines)
	draft := IssueTask{Kind: "fix", Title: title, Paths: withCompanionTests(root, paths),
		Description:        fmt.Sprintf("The same %d lines, starting `%s`, appear in:\n%s\nMove them into one function the places call, named for what it does. Keep each caller's behavior.", group.Lines, group.Preview, strings.Join(places, "\n")),
		AcceptanceCriteria: []string{"The block exists once and every place calls it", "Existing tests pass unchanged"}}
	return HealthTask{ID: fmt.Sprintf("duplicate:%s:%d", group.Places[0].Path, group.Places[0].StartLine), Kind: "duplication", Title: title, Why: "A bug fixed in one copy stays in the others.", Draft: draft}
}

// spreadTests are the test files of one source file: its companion and
// the files named after it with a suffix (add_boundary_test.go for add.go).
type spreadTests struct {
	source, companion string
	tests             []string
	// fileLines are each test file's lines, as file budgets count them.
	fileLines map[string]int
}

// spreadTestGroups finds, in the directories with too many test files, the
// source files whose tests are spread over three or more files, most first.
func spreadTestGroups(root string, dirs []HealthTestDir, sources []healthSource) []spreadTests {
	lines := map[string]int{}
	byDir := map[string][]healthSource{}
	for _, source := range sources {
		lines[source.path] = source.fileLines
		byDir[path.Dir(source.path)] = append(byDir[path.Dir(source.path)], source)
	}
	var groups []spreadTests
	for _, dir := range dirs {
		owners := map[string]*spreadTests{}
		var stems []string
		for _, source := range byDir[dir.Dir] {
			if !source.test {
				stem := testStem(source.path)
				stems = append(stems, stem)
				owners[stem] = &spreadTests{source: source.path, companion: rules.CompanionTest(root, source.path), fileLines: map[string]int{}}
			}
		}
		// Longest stems first, so add_name.go owns add_name_limit_test.go.
		sort.Slice(stems, func(i, j int) bool { return len(stems[i]) > len(stems[j]) })
		for _, source := range byDir[dir.Dir] {
			if !source.test {
				continue
			}
			stem := testStem(source.path)
			for _, owner := range stems {
				if stem == owner || strings.HasPrefix(stem, owner+"_") {
					owners[owner].tests = append(owners[owner].tests, source.path)
					owners[owner].fileLines[source.path] = lines[source.path]
					break
				}
			}
		}
		for _, owner := range owners {
			if len(owner.tests) >= 3 {
				sort.Strings(owner.tests)
				groups = append(groups, *owner)
			}
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].tests) != len(groups[j].tests) {
			return len(groups[i].tests) > len(groups[j].tests)
		}
		return groups[i].source < groups[j].source
	})
	return groups
}

// testGatherTask drafts gathering one source file's spread tests into as
// few files as its file line budget allows: its companion test, then
// x_<behavior>_test.go files for what does not fit, each with room left
// for the next fixes' tests.
func testGatherTask(root string, group spreadTests) HealthTask {
	home := group.companion
	if home == "" {
		home = strings.TrimSuffix(group.source, path.Ext(group.source)) + "_test" + path.Ext(group.source)
	}
	// A task holds at most eight files, the companion first; the rest are
	// the next task's once this one is merged.
	paths := []string{}
	if slices.Contains(group.tests, home) {
		paths = append(paths, home)
	}
	for _, test := range group.tests {
		if len(paths) < 8 && test != home {
			paths = append(paths, test)
		}
	}
	lines := 0
	for _, test := range paths {
		lines += group.fileLines[test]
	}
	budget := rules.NewResolver(root).For(home).Budgets.FileLines
	files := gatheredFiles(lines, budget)
	stem := strings.TrimSuffix(path.Base(home), "_test"+path.Ext(home))
	into := path.Base(home)
	if files > 1 {
		into = fmt.Sprintf("%d files", files)
	}
	title := fmt.Sprintf("Gather the %d test files of %s into %s", len(group.tests), path.Base(group.source), into)
	if len(paths) < len(group.tests) {
		title = fmt.Sprintf("Gather %d of the %d test files of %s into %s", len(paths), len(group.tests), path.Base(group.source), into)
	}
	description := fmt.Sprintf("The tests of %s are spread over %d files (%d lines). Move the tests of %s into %s (rename one of these files to it when it does not exist)", group.source, len(group.tests), group.totalLines(), strings.Join(quoted(paths), ", "), home)
	criteria := []string{fmt.Sprintf("The tests of %s are in %s only", path.Base(group.source), path.Base(home))}
	if files > 1 {
		description = fmt.Sprintf("The tests of %s are spread over %d files (%d lines). %s hold %d lines, more than one file's budget of %d lines. Gather them into at most %d files of at most %d lines each: %s first, then %s_<behavior>_test.go for each further group of related behaviors (for example %s_rollback_test.go), named for what its tests check, never for an issue",
			group.source, len(group.tests), group.totalLines(), strings.Join(quoted(paths), ", "), lines, budget, files, gatherFileLines(budget), home, stem, stem)
		criteria = []string{fmt.Sprintf("The tests of %s are in at most %d files, each at most %d lines", path.Base(group.source), files, gatherFileLines(budget))}
	}
	description += ". Merge cases that check the same behavior into one table test, delete tests that repeat another (fewer files when that makes room), and delete the emptied files. Do not change code that is not a test."
	if budget > 0 && files == 1 {
		criteria = append(criteria, fmt.Sprintf("%s is at most %d lines", path.Base(home), gatherFileLines(budget)))
	}
	draft := IssueTask{Kind: "fix", Title: title, Paths: append([]string{}, paths...), Description: description,
		AcceptanceCriteria: append(criteria, "Every behavior tested before is still tested once", "All tests pass")}
	return HealthTask{ID: "tests:" + group.source, Kind: "tests", Title: title, Why: "Tests spread over many files get copied instead of extended, and every copy is more to analyze and keep passing.", Draft: draft}
}

func (g spreadTests) totalLines() int {
	total := 0
	for _, lines := range g.fileLines {
		total += lines
	}
	return total
}

// gatherFileLines is how long a gathered test file may be: its budget less
// a tenth, so the next fixes' tests still fit; 0 without a budget.
func gatherFileLines(budget int) int {
	return budget - budget/10
}

// gatheredFiles is how many files lines of tests need under a file budget
// (one without a budget).
func gatheredFiles(lines, budget int) int {
	limit := gatherFileLines(budget)
	if limit <= 0 {
		return 1
	}
	return max(1, (lines+limit-1)/limit)
}

func complexityTask(root string, fn HealthComplexity) HealthTask {
	title := fmt.Sprintf("Split %s (complexity %d, budget %d)", fn.Function, fn.Complexity, fn.Budget)
	draft := IssueTask{Kind: "fix", Title: title, Paths: withCompanionTests(root, []string{fn.Path}),
		Description:        fmt.Sprintf("`%s` in %s (line %d) has complexity %d over %d lines. Split it into functions with one job each, named for what they do, so it is within its budget of %d. Keep its behavior.", fn.Function, fn.Path, fn.StartLine, fn.Complexity, fn.Lines, fn.Budget),
		AcceptanceCriteria: []string{fmt.Sprintf("%s is within its complexity budget", fn.Function), "Existing tests pass unchanged"}}
	return HealthTask{ID: fmt.Sprintf("complexity:%s:%s", fn.Path, fn.Function), Kind: "complexity", Title: title, Why: "Complex functions are where fixes break something else.", Draft: draft}
}

func coverageTask(pkg HealthPackage) HealthTask {
	title := fmt.Sprintf("Test the untested paths of %s (%.0f%% covered)", pkg.Name, *pkg.Coverage)
	draft := IssueTask{Kind: "fix", Title: title, Paths: []string{},
		Description:        fmt.Sprintf("Tests cover %.0f%% of the statements in %s. Add tests for its main behaviors that no test runs, one per behavior, in the existing test file of each source file. Do not test private helpers one by one.", *pkg.Coverage, pkg.Name),
		AcceptanceCriteria: []string{"The package's main untested behaviors have one test each", "No code other than tests changes"}}
	return HealthTask{ID: "coverage:" + pkg.Name, Kind: "coverage", Title: title, Why: "Code no test runs breaks without anyone noticing.", Draft: draft}
}

func withCompanionTests(root string, paths []string) []string {
	result := append([]string{}, paths...)
	for _, file := range paths {
		if companion := rules.CompanionTest(root, file); companion != "" && !slices.Contains(result, companion) {
			result = append(result, companion)
		}
	}
	if len(result) > 8 {
		result = result[:8]
	}
	return result
}

func quoted(names []string) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = "`" + name + "`"
	}
	return result
}
