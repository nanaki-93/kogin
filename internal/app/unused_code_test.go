package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

// unusedProgram is a program whose generateKey nothing calls; only a test does.
const unusedProgram = `package main

func main() { run() }

func run() {}

func generateKey() int {
	return 1
}
`

func TestCodeNothingCallsIsOneDeleteIssueAndItsAnalysisIsLeftOut(t *testing.T) {
	s, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	writeProjectFile(t, root, "main.go", unusedProgram)
	writeProjectFile(t, root, "main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestKey(t *testing.T) { generateKey() }\n")
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	findings, err := s.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var unused *project.UnifiedFinding
	for i, finding := range findings {
		if finding.Source == project.FindingSourceUnused {
			unused = &findings[i]
		}
	}
	if unused == nil || unused.Title != "Delete unused code in main.go" || unused.Location.StartLine != 7 || unused.Location.Symbol != "generateKey" {
		t.Fatalf("unused-code issue = %+v (all %+v)", unused, findings)
	}
	if brief := unused.Brief; brief == nil || !strings.Contains(brief.Problem, "`generateKey` (line 7) has no caller") || !strings.Contains(brief.Impact, "Only tests call `generateKey`") || !strings.HasPrefix(brief.Fix, "Delete them") {
		t.Fatalf("brief = %+v", unused.Brief)
	}
	task := issueTask(root, *unused)
	if task.Title != "Delete unused code in main.go" || !strings.HasPrefix(task.AcceptanceCriteria[0], "Delete `generateKey` from main.go") {
		t.Fatalf("task = %+v", task)
	}

	// An analysis issue inside unused code is left out: fixing it is work for nothing.
	inside := project.UnifiedFinding{Location: project.FindingLocation{Path: "main.go", StartLine: 8}}
	outside := project.UnifiedFinding{Location: project.FindingLocation{Path: "main.go", StartLine: 5}}
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	kept := withoutUnused([]project.UnifiedFinding{inside, outside}, unusedByPath(s.unusedCode(index)))
	if len(kept) != 1 || kept[0].Location.StartLine != 5 {
		t.Fatalf("kept = %+v", kept)
	}
	if brief := unusedCodeBrief(unusedByPath(s.unusedCode(index))["main.go"]); !strings.Contains(brief, "nothing calls `generateKey` (lines 7-9)") {
		t.Fatalf("analysis brief = %q", brief)
	}

	// Once something calls it, the issue closes.
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Replace(unusedProgram, "run() }", "run(); generateKey() }", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	findings, err = s.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Source == project.FindingSourceUnused && finding.Status == project.FindingStatusOpen {
			t.Fatalf("still open: %+v", finding)
		}
	}
}

func TestAChangeMayNotLeaveCodeNothingCalls(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.test/app\n")
	writeProjectFile(t, root, "main.go", unusedProgram)
	target := ChangeTarget{Path: "main.go", Exists: true, Content: unusedProgram}
	// Code that was unused before the change is not the change's.
	same := []ChangeEdit{{Path: "main.go", Content: strings.Replace(unusedProgram, "func run() {}", "func run() { _ = 2 }", 1)}}
	if check := unusedCodeCheck(root, []ChangeTarget{target}, same); check == nil || check.State != CheckPassed || !check.Required {
		t.Fatalf("check = %+v", check)
	}
	// A new helper nothing calls, and run left without its caller, fail it.
	worse := strings.Replace(unusedProgram, "func main() { run() }", "func main() {}\n\nfunc newHelper() {}", 1)
	check := unusedCodeCheck(root, []ChangeTarget{target}, []ChangeEdit{{Path: "main.go", Content: worse}})
	if check == nil || check.State != CheckFailed || !strings.Contains(check.Output, "main.go:5: newHelper has no caller") || !strings.Contains(check.Output, ": run has no caller") || strings.Contains(check.Output, "generateKey") {
		t.Fatalf("check = %+v", check)
	}
	// A change without Go code has nothing to check.
	if check := unusedCodeCheck(root, nil, []ChangeEdit{{Path: "README.md", Content: "x"}}); check != nil {
		t.Fatalf("check = %+v", check)
	}
}
