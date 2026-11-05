package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestTestBudgetFailsAPerIssueTestFileOnceADirectoryHasTwoPerSourceFile(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"calc.go": "package calc\n", "calc_test.go": "package calc\n", "calc_add_test.go": "package calc\n", "parse.go": "package calc\n"} {
		writeTestFile(t, root, name, content)
	}
	test := func(path string) ChangeEdit {
		return ChangeEdit{Path: path, Status: EditAdded, Content: "package calc\n\nfunc TestX(t *testing.T) {}\n"}
	}
	newTarget := func(path string) ChangeTarget { return ChangeTarget{Path: path, Test: true} }
	// 2 source files, 2 test files: a third and fourth not named after a source file still fit.
	if check := testBudgetCheck(root, []ChangeTarget{newTarget("calc_sub_test.go")}, []ChangeEdit{test("calc_sub_test.go")}); check == nil || check.State != CheckPassed {
		t.Fatalf("within two per source file: %+v", check)
	}
	writeTestFile(t, root, "calc_sub_test.go", "package calc\n")
	writeTestFile(t, root, "calc_mul_test.go", "package calc\n")
	check := testBudgetCheck(root, []ChangeTarget{newTarget("calc_div_zero_test.go")}, []ChangeEdit{test("calc_div_zero_test.go"), {Path: "calc.go", Content: "package calc\n\nfunc Div() {}\n"}})
	if check == nil || check.State != CheckFailed || !strings.Contains(check.Output, "calc_div_zero_test.go would make 5 test files for 2 source files") || !strings.Contains(check.Output, "add its tests to calc_test.go") {
		t.Fatalf("per-issue file: %+v", check)
	}
	for _, allowed := range []string{"parse_test.go", "main_test.go"} {
		if check := testBudgetCheck(root, []ChangeTarget{newTarget(allowed)}, []ChangeEdit{test(allowed)}); check.State != CheckPassed {
			t.Errorf("%s is named after a source file or shared: %+v", allowed, check)
		}
	}
	if check := testBudgetCheck(root, nil, []ChangeEdit{{Path: "calc.go", Content: "package calc\n\nfunc A() {}\n"}}); check != nil {
		t.Fatalf("a change without test edits has no test budget check: %+v", check)
	}
}

func TestTestBudgetFailsTestsThatGrowFarFasterThanTheCode(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "calc.go", "package calc\n")
	writeTestFile(t, root, "calc_test.go", "package calc\n")
	grown := "package calc\n" + strings.Repeat("func TestCase(t *testing.T) { check(t) }\n", 130)
	edits := []ChangeEdit{{Path: "calc.go", Content: "package calc\n\nfunc Div(a, b int) int { return a / b }\n"}, {Path: "calc_test.go", Content: grown}}
	targets := []ChangeTarget{{Path: "calc.go", Exists: true, Content: "package calc\n"}, {Path: "calc_test.go", Exists: true, Content: "package calc\n", Test: true}}
	check := testBudgetCheck(root, targets, edits)
	if check == nil || check.State != CheckFailed || !strings.Contains(check.Output, "Tests grow by 130 lines for 1 changed lines of code (at most 120)") {
		t.Fatalf("check = %+v", check)
	}
	edits[1].Content = "package calc\n" + strings.Repeat("func TestCase(t *testing.T) { check(t) }\n", 100)
	if check := testBudgetCheck(root, targets, edits); check.State != CheckPassed {
		t.Fatalf("100 lines fit the minimum budget: %+v", check)
	}
}

func TestParseGoTestCoverReadsEachPackageAndTheProfileTotal(t *testing.T) {
	out := strings.Join([]string{
		"ok  \texample.test/app/internal/utils\t0.378s\tcoverage: 85.2% of statements",
		"ok  \texample.test/app/internal/model\t(cached)\tcoverage: 100.0% of statements",
		"--- FAIL: TestGet (0.00s)",
		"FAIL\texample.test/app/cmd\t0.10s",
		"?   \texample.test/app/tools\t[no test files]",
		"\texample.test/app\t\tcoverage: 0.0% of statements",
	}, "\n")
	packages := parseGoTestCover(out, "example.test/app", "")
	got := map[string]HealthPackage{}
	for _, pkg := range packages {
		got[pkg.Name] = pkg
	}
	if len(packages) != 5 || *got["internal/utils"].Coverage != 85.2 || got["cmd"].Passed || got["cmd"].Coverage != nil || !got["tools"].NoTests || !got["."].NoTests || *got["."].Coverage != 0 {
		t.Fatalf("packages = %+v", packages)
	}
	profile := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(profile, []byte("mode: set\na.go:1.1,2.2 3 1\na.go:3.1,4.2 1 0\nb.go:1.1,2.2 6 0\na.go:3.1,4.2 1 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if covered, total := coverProfileTotals(profile); covered != 4 || total != 10 {
		t.Fatalf("covered %d of %d", covered, total)
	}
}

func TestTrendVerdictSaysWhetherFixingConverges(t *testing.T) {
	for _, test := range []struct {
		found, fixed int
		want         string
	}{{0, 0, "quiet"}, {310, 109, "growing"}, {6, 1, "growing"}, {4, 9, "converging"}, {5, 4, "steady"}} {
		if got := trendVerdict(test.found, test.fixed); got != test.want {
			t.Errorf("found %d fixed %d: %s, want %s", test.found, test.fixed, got, test.want)
		}
	}
	if countsInTrend(project.UnifiedFinding{Source: project.FindingSourceAI, OutOfScope: &project.FindingScope{Reason: ScopeTest}}) || !countsInTrend(project.UnifiedFinding{Source: project.FindingSourceAI, OutOfScope: &project.FindingScope{Reason: ScopeBelowFinishLine}}) {
		t.Fatal("the trend leaves out test and platform issues only")
	}
}

func TestFinishLineIsDoneWhenNoCountedIssueIsOpenAndTheTestsPass(t *testing.T) {
	profile := ProjectProfile{FinishLine: "medium"}
	findings := []project.UnifiedFinding{
		{Status: project.FindingStatusOpen, Severity: "low", OutOfScope: &project.FindingScope{Reason: ScopeBelowFinishLine}},
		{Status: project.FindingStatusDismissed, Severity: "high"},
	}
	if line := finishLine(profile, findings, &HealthTestRun{Current: true, Passed: true}); !line.Done || line.Open != 0 || line.OutOfScope[ScopeBelowFinishLine] != 1 {
		t.Fatalf("line = %+v", line)
	}
	if line := finishLine(profile, findings, &HealthTestRun{Current: false, Passed: true}); line.Done || line.TestsPass != nil {
		t.Fatalf("tests of an older revision do not count: %+v", line)
	}
	findings = append(findings, project.UnifiedFinding{Status: project.FindingStatusOpen, Severity: "Medium"})
	if line := finishLine(profile, findings, &HealthTestRun{Current: true, Passed: true}); line.Done || line.Open != 1 || line.BySeverity["medium"] != 1 {
		t.Fatalf("line = %+v", line)
	}
}

func TestHealthDraftsGatherTasksForTestsSpreadOverManyFiles(t *testing.T) {
	root := t.TempDir()
	sources := []healthSource{{path: "cmd/add.go"}, {path: "cmd/add_name.go"}, {path: "cmd/get.go"}}
	for _, test := range []string{"cmd/add_test.go", "cmd/add_boundary_test.go", "cmd/add_rollback_test.go", "cmd/add_name_test.go", "cmd/add_name_limit_test.go", "cmd/get_test.go"} {
		sources = append(sources, healthSource{path: test, test: true, lines: 10})
	}
	for _, source := range sources {
		writeTestFile(t, root, source.path, "package cmd\n")
	}
	code := measureCode(root, sources)
	if len(code.TestDirs) != 0 {
		t.Fatalf("6 test files for 3 source files is two per source file: %+v", code.TestDirs)
	}
	groups := spreadTestGroups(root, []HealthTestDir{{Dir: "cmd"}}, sources)
	if len(groups) != 1 || groups[0].source != "cmd/add.go" || strings.Join(groups[0].tests, ",") != "cmd/add_boundary_test.go,cmd/add_rollback_test.go,cmd/add_test.go" {
		t.Fatalf("groups = %+v", groups)
	}
	task := testGatherTask(groups[0])
	if task.Title != "Gather the 3 test files of add.go into add_test.go" || task.Draft.Paths[0] != "cmd/add_test.go" || len(task.Draft.Paths) != 3 || task.Draft.Kind != "fix" {
		t.Fatalf("task = %+v", task)
	}
}
