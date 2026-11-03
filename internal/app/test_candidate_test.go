package app

import (
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestTestCandidatesAreCheckedInEveryLanguage(t *testing.T) {
	for _, test := range []struct {
		name                string
		target              project.IndexFile
		path, test, content string
		kept                bool
	}{
		{"python beside", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "app/test_cart.py", "test_total_rounds", "def test_total_rounds():\n    assert total([1.005]) == 1.01\n", true},
		{"typescript test", project.IndexFile{Path: "src/cart.ts", Language: "TypeScript"}, "src/cart.test.ts", "rounds the total", "import { total } from './cart';\n\nit('rounds the total', () => {\n  expect(total([1.005])).toBe(1.01);\n});\n", true},
		{"java mirror", project.IndexFile{Path: "src/main/java/a/Cart.java", Language: "Java"}, "src/test/java/a/CartTest.java", "roundsTheTotal", "package a;\nclass CartTest {\n  @Test void roundsTheTotal() { }\n}\n", true},
		{"other language", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "app/cart.test.ts", "rounds", "it('rounds', () => {});\n", false},
		{"not a test file", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "app/cart_helpers.py", "test_x", "def test_x():\n    pass\n", false},
		{"does not parse", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "app/test_cart.py", "test_x", "def test_x(:\n", false},
		{"name not in it", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "app/test_cart.py", "test_y", "def test_x():\n    pass\n", false},
		{"outside the project", project.IndexFile{Path: "app/cart.py", Language: "Python"}, "../test_cart.py", "test_x", "def test_x():\n    pass\n", false},
	} {
		candidate := validateTestCandidate(&project.TestCandidateSpec{Path: test.path, Name: test.test, Content: test.content}, test.target, "")
		if (candidate != nil) != test.kept {
			t.Fatalf("%s: candidate = %+v, want kept %v", test.name, candidate, test.kept)
		}
	}
}

func TestRiskAnchorsFindTheirLine(t *testing.T) {
	source := "package main\n\nfunc Run() {\n\tdata, _ := os.ReadFile(path)\n\t_ = data\n}\n"
	for anchor, want := range map[string][2]int{
		"data, _ := os.ReadFile(path)":           {4, 4},
		"  data,   _ := os.ReadFile(path)  ":     {4, 4},
		"data, _ := os.ReadFile(path)\n_ = data": {4, 5},
		"missing line":                           {0, 0},
		"_":                                      {0, 0},
	} {
		if start, end := anchoredLines(source, anchor); start != want[0] || end != want[1] {
			t.Fatalf("anchor %q = %d-%d, want %v", anchor, start, end, want)
		}
	}
}

func TestIssueTasksStartFromTheProposedTest(t *testing.T) {
	root := t.TempDir()
	finding := project.UnifiedFinding{Title: "Total rounds down", Message: "total() truncates.", Category: project.FindingCategoryBugs,
		Location: project.FindingLocation{Path: "app/cart.py", StartLine: 3},
		TaskSpec: &project.BugTaskSpec{TargetPath: "app/cart.py", AcceptanceCriteria: []string{"Round half up."},
			TestCandidate: &project.TestCandidateSpec{Path: "app/test_cart.py", Name: "test_total_rounds", Content: "def test_total_rounds():\n    assert total([1.005]) == 1.01\n"}}}
	task := issueTask(root, finding)
	if strings.Join(task.Paths, ",") != "app/cart.py,app/test_cart.py" || !strings.Contains(task.Description, "a new test file app/test_cart.py") ||
		!strings.Contains(task.Description, "assert total([1.005]) == 1.01") || task.AcceptanceCriteria[len(task.AcceptanceCriteria)-1] != "test_total_rounds in app/test_cart.py fails before the fix and passes after it" {
		t.Fatalf("task = %+v", task)
	}
}
