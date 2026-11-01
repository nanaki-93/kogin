package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

// newTrustedService opens a project with files and lets checks run its code.
func newTrustedService(t *testing.T, files map[string]string) (*Service, string, string) {
	t.Helper()
	root := t.TempDir()
	writeFixtureFiles(t, root, files)
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	analysis, err := manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig("http://127.0.0.1:1")
	cfg.Timeouts = config.TimeoutConfig{FocusedCheckSeconds: 60}
	service, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), analysis.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	return service, root, analysis.ProjectRevision
}

func writeFixtureFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

var checkedProject = map[string]string{
	"go.mod":    "module example.com/fixture\n\ngo 1.22\n",
	"AGENTS.md": "```kasuri\nbudgets:\n  file_lines: 12\n```\n",
	"store.go":  "package fixture\n\nimport \"fmt\"\n\nfunc Save(id int) string {\n\treturn fmt.Sprint(id + 1)\n\tpanic(\"unreachable\")\n}\n",
	"store_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestSave(t *testing.T) {\n\tif Save(1) != \"1\" {\n" +
		"\t\tt.Fatal(\"saved the wrong id\")\n\t}\n}\n",
	"long.go": "package fixture\n\n" + strings.Repeat("// line\n", 14),
}

func issuesBySource(t *testing.T, s *Service, revision string) map[string][]project.UnifiedFinding {
	t.Helper()
	store, err := project.NewFindingStore(s.manager.Root())
	if err != nil {
		t.Fatal(err)
	}
	findings, err := store.Load(project.FindingInput{ProjectID: projectIDOf(t, s), ProjectRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string][]project.UnifiedFinding{}
	for _, finding := range findings {
		if finding.Status == project.FindingStatusOpen {
			bySource[finding.Source] = append(bySource[finding.Source], finding)
		}
	}
	return bySource
}

func projectIDOf(t *testing.T, s *Service) string {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	return analysis.ProjectID
}

func TestProjectChecksTurnFailuresIntoIssuesAndCloseTheFixedOnes(t *testing.T) {
	s, root, revision := newTrustedService(t, checkedProject)
	preview := &AnalysisRunPreview{Files: []AnalysisPlannedFile{{}}}
	s.planAnalysisChecks(context.Background(), root, revision, preview)
	var planned []string
	for _, command := range preview.Checks.Commands {
		planned = append(planned, command.Name+"="+strings.Join(command.Command, " "))
	}
	if strings.Join(planned, ", ") != "lint=go vet ./..., tests=go test ./..." || preview.Checks.Reason != "" {
		t.Fatalf("plan = %+v", preview.Checks)
	}
	checks := s.runProjectChecks(context.Background(), revision, *preview.Checks)
	states := map[string]string{}
	for _, check := range checks.Checks {
		states[check.Name] = check.State
	}
	if checks.Status != "completed" || states["parse"] != CheckPassed || states["code budgets"] != CheckFailed || states["lint"] != CheckFailed || states["tests"] != CheckFailed || checks.Issues != 3 {
		t.Fatalf("checks = %+v", checks)
	}
	issues := issuesBySource(t, s, revision)
	lint, tests, budgets := issues[project.FindingSourceLint], issues[project.FindingSourceTest], issues[project.FindingSourceRules]
	if len(lint) != 1 || lint[0].Location.Path != "store.go" || lint[0].Location.StartLine != 7 || lint[0].Location.Symbol != "Save" || !strings.Contains(lint[0].Message, "unreachable") {
		t.Fatalf("lint issues = %+v", lint)
	}
	if len(tests) != 1 || tests[0].Title != "Test fails: example.com/fixture.TestSave" || tests[0].Location.Path != "store_test.go" || tests[0].Location.StartLine != 7 || tests[0].Category != project.FindingCategoryBugs {
		t.Fatalf("test issues = %+v", tests)
	}
	if len(budgets) != 1 || budgets[0].Location.Path != "long.go" {
		t.Fatalf("budget issues = %+v", budgets)
	}

	// Fixing the code closes its issues on the next run.
	writeFixtureFiles(t, root, map[string]string{
		"store.go": "package fixture\n\nimport \"fmt\"\n\nfunc Save(id int) string {\n\treturn fmt.Sprint(id)\n}\n",
		"long.go":  "package fixture\n",
	})
	index, err := s.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustProjectExecution(context.Background(), index.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	checks = s.runProjectChecks(context.Background(), index.ProjectRevision, *preview.Checks)
	if checks.Issues != 0 {
		t.Fatalf("checks after the fix = %+v", checks)
	}
	if open := issuesBySource(t, s, index.ProjectRevision); len(open) != 0 {
		t.Fatalf("open issues after the fix = %+v", open)
	}
}

func TestChecksRunProjectCodeOnlyWhenTheProjectAllowsIt(t *testing.T) {
	s, root, revision := newTrustedService(t, map[string]string{"go.mod": "module example.com/fixture\n\ngo 1.22\n", "main.go": "package fixture\n", ".semgrep.yml": "rules: []\n"})
	if _, err := s.TrustProjectExecution(context.Background(), revision, false); err != nil {
		t.Fatal(err)
	}
	previous := lookPath
	lookPath = func(name string) (string, error) {
		if name == "semgrep" {
			return "/usr/bin/semgrep", nil
		}
		return "", errors.New("not found")
	}
	defer func() { lookPath = previous }()
	preview := &AnalysisRunPreview{Files: []AnalysisPlannedFile{{}}}
	s.planAnalysisChecks(context.Background(), root, revision, preview)
	var planned []string
	for _, command := range preview.Checks.Commands {
		planned = append(planned, command.Name)
	}
	if strings.Join(planned, ", ") != "lint, semgrep" || preview.Checks.Reason != checksUntrustedReason {
		t.Fatalf("untrusted plan = %+v", preview.Checks)
	}
	// A plan made while trusted skips project code once trust is revoked.
	checks := s.runProjectChecks(context.Background(), revision, AnalysisChecksPlan{Commands: []AnalysisCheckCommand{{Name: "tests", Kind: checkKindTest, Command: []string{"go", "test", "./..."}}}})
	if last := checks.Checks[len(checks.Checks)-1]; last.State != CheckSkipped || last.Output != checksUntrustedReason {
		t.Fatalf("revoked trust = %+v", checks)
	}
	empty := &AnalysisRunPreview{}
	if s.planAnalysisChecks(context.Background(), root, revision, empty); empty.Checks != nil {
		t.Fatal("a run without files planned checks")
	}
}

func TestSemgrepMatchesBecomeIssuesOnTheirLines(t *testing.T) {
	s, root, revision := newTrustedService(t, map[string]string{"app/db.py": "def get(id):\n    return db.execute('SELECT ' + id)\n"})
	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	run := &projectCheckRun{s: s, root: root, revision: revision, index: index, hashes: scanFileHashes(index)}
	findings, err := run.semgrepFindings(`{"results":[{"check_id":"rules.sql","path":"app/db.py","start":{"line":2},"end":{"line":2},
		"extra":{"message":"SQL built from strings","severity":"ERROR","metadata":{"cwe":"CWE-89: SQL Injection"}}},
		{"check_id":"rules.sql","path":"gone.py","start":{"line":1},"end":{"line":1},"extra":{"message":"x","severity":"INFO"}}]}`)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings = %+v, %v", findings, err)
	}
	finding := findings[0]
	if finding.Source != project.FindingSourceSemgrep || finding.Category != project.FindingCategorySecurity || finding.Severity != "high" ||
		finding.Title != "rules.sql (CWE-89)" || finding.Location.StartLine != 2 || finding.FileHash == "" {
		t.Fatalf("finding = %+v", finding)
	}
	if _, err := run.semgrepFindings(`{"results": [`); err == nil {
		t.Fatal("unreadable semgrep output was accepted")
	}
}

func TestStoredChecksAreValidated(t *testing.T) {
	valid := &AnalysisRun{Plan: AnalysisRunPreview{Checks: &AnalysisChecksPlan{Commands: []AnalysisCheckCommand{{Name: "lint", Kind: checkKindLint, Command: []string{"go", "vet", "./..."}}}}},
		Checks: &AnalysisChecks{Status: "completed", Checks: []AnalysisCheckResult{{Name: "lint", State: CheckFailed, Issues: 1}}, Issues: 1}}
	if err := validateStoredAnalysisChecks(valid); err != nil {
		t.Fatal(err)
	}
	for name, corrupt := range map[string]func(*AnalysisRun){
		"unknown kind":  func(run *AnalysisRun) { run.Plan.Checks.Commands[0].Kind = "deploy" },
		"empty command": func(run *AnalysisRun) { run.Plan.Checks.Commands[0].Command = nil },
		"unknown state": func(run *AnalysisRun) { run.Checks.Checks[0].State = "maybe" },
		"free reason":   func(run *AnalysisRun) { run.Checks.Reason = "anything" },
	} {
		run := cloneAnalysisRun(valid)
		corrupt(run)
		if err := validateStoredAnalysisChecks(run); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestAnalysisRunRunsTheChecksBeforeTheDocs(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	writeFixtureFiles(t, root, map[string]string{"AGENTS.md": "```kasuri\nbudgets:\n  file_lines: 3\n```\n"})
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if preview.Checks == nil || len(preview.Checks.Commands) != 0 {
		t.Fatalf("checks plan = %+v", preview.Checks)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if run.Checks == nil || run.Checks.Status != "completed" || run.Checks.Issues != 1 || len(run.Checks.Checks) != 2 {
		t.Fatalf("checks = %+v", run.Checks)
	}
	stored, err := s.currentTestAnalysisRun(context.Background())
	if err != nil || stored.Checks == nil || stored.Checks.Issues != 1 {
		t.Fatalf("stored checks = %+v, %v", stored, err)
	}
}
