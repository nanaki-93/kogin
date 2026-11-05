package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestProjectProfileFillsKindDefaultsAndRejectsUnknownValues(t *testing.T) {
	personal, err := normalizeProjectProfile(ProjectProfile{Kind: ProfilePersonal, Platforms: []string{"macos", "linux", "macos"}, Purpose: "  my  password\nmanager "})
	if err != nil || personal.Security != SecurityBasic || personal.FinishLine != "medium" || strings.Join(personal.Platforms, ",") != "linux,macos" || personal.Purpose != "my password manager" || personal.Shape != "other" {
		t.Fatalf("personal = %+v, %v", personal, err)
	}
	if enterprise, _ := normalizeProjectProfile(ProjectProfile{Kind: ProfileEnterprise}); enterprise.Security != SecurityStrict {
		t.Fatalf("enterprise = %+v", enterprise)
	}
	for name, profile := range map[string]ProjectProfile{
		"kind":     {Kind: "hobby"},
		"shape":    {Shape: "mobile"},
		"security": {Security: "paranoid"},
		"finish":   {FinishLine: "info"},
		"platform": {Platforms: []string{"bsd"}},
		"purpose":  {Purpose: strings.Repeat("x", maxProfilePurposeRunes+1)},
	} {
		if _, err := normalizeProjectProfile(profile); !errors.Is(err, ErrInvalidProfile) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestProjectProfileIsSavedLoadedAndReportsADamagedFile(t *testing.T) {
	root := t.TempDir()
	if profile, err := loadProjectProfile(root); err != nil || profile.Saved || profile.Kind != ProfileTeam || profile.FinishLine != "medium" {
		t.Fatalf("default = %+v, %v", profile, err)
	}
	service := &Service{manager: newProfileTestManager(t, root)}
	saved, err := service.SaveProjectProfile(ProjectProfile{Kind: ProfilePersonal, Shape: "gui_backend", Platforms: []string{"macos"}})
	if err != nil || !saved.Saved {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	loaded, err := service.ProjectProfile()
	if err != nil || !loaded.Saved || loaded.Shape != "gui_backend" || loaded.Security != SecurityBasic {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kogin", "profile.json"), []byte(`{"schema_version":"1","profile":{"kind":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProjectProfile(); err == nil || !strings.Contains(err.Error(), "save the profile again") {
		t.Fatalf("damaged err = %v", err)
	}
	if fallback := profileFor(root); fallback.Saved {
		t.Fatalf("a damaged profile falls back to the default, got %+v", fallback)
	}
}

func TestProfileBriefTellsAnalysisWhatIsInScopeForItsSecurityLevel(t *testing.T) {
	if brief := DefaultProjectProfile().analysisBrief(); brief != "" {
		t.Fatalf("the default profile adds nothing to prompts, got %q", brief)
	}
	brief := ProjectProfile{Kind: ProfilePersonal, Shape: "gui_backend", Platforms: []string{"macos"}, Security: SecurityBasic, FinishLine: "medium", Purpose: "Notes app.", Saved: true}.analysisBrief()
	for _, want := range []string{"a personal app with its own frontend and backend that runs on macOS", "Purpose: Notes app.", "frontend and backend on this machine trust each other", "wiping secrets from memory", "platform it does not run on"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	if strict := (ProjectProfile{Kind: ProfileEnterprise, Shape: "web_service", Security: SecurityStrict, Saved: true}).analysisBrief(); !strings.Contains(strict, "every boundary between components") {
		t.Fatalf("strict brief = %s", strict)
	}
}

func TestFindingScopeLeavesOutTestsOtherPlatformsAndIssuesBelowTheFinishLine(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "clip_windows.go", "package clip\n")
	writeTestFile(t, root, "perm_other.go", "//go:build !unix\n\npackage clip\n")
	writeTestFile(t, root, "perm_unix.go", "//go:build unix\n\npackage clip\n")
	profile := ProjectProfile{Kind: ProfilePersonal, Platforms: []string{"macos"}, FinishLine: "medium", Saved: true}
	scoper := newFindingScoper(root, profile, nil, map[string]string{}, newLineAges())
	finding := func(path, severity string, test bool) project.UnifiedFinding {
		return project.UnifiedFinding{Source: project.FindingSourceAI, Status: project.FindingStatusOpen, Severity: severity, Test: test, Location: project.FindingLocation{Path: path, StartLine: 1}}
	}
	for _, test := range []struct {
		finding project.UnifiedFinding
		want    string
	}{
		{finding("clip_windows.go", "high", false), ScopePlatform},
		{finding("perm_other.go", "high", false), ScopePlatform},
		{finding("perm_unix.go", "medium", false), ""},
		{finding("perm_unix_test.go", "high", true), ScopeTest},
		{finding("perm_unix.go", "low", false), ScopeBelowFinishLine},
		{project.UnifiedFinding{Source: project.FindingSourceTest, Severity: "low", Location: project.FindingLocation{Path: "clip_windows.go"}}, ""},
	} {
		got := ""
		if scope := scoper.scope(test.finding); scope != nil {
			got = scope.Reason
		}
		if got != test.want {
			t.Errorf("%s %s: scope = %q, want %q", test.finding.Location.Path, test.finding.Severity, got, test.want)
		}
	}
	profile.AnalyzeTests = true
	if scope := newFindingScoper(root, profile, nil, nil, newLineAges()).scope(finding("perm_unix_test.go", "high", true)); scope != nil {
		t.Fatalf("with test analysis on, a high test issue counts: %+v", scope)
	}
}

func TestFindingScopeCallsANewIssueOnLinesUnchangedSinceTheLastAnalysisASecondLook(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n")
	testGit(t, root, "init", "-b", "main")
	testGit(t, root, "config", "user.name", "Kogin Test")
	testGit(t, root, "config", "user.email", "test@example.com")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "calc", "--date", "2026-01-01T00:00:00Z")
	analyzed := time.Now().Add(time.Minute)
	writeTestFile(t, root, "calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b + 0 }\n")
	previous := project.UnifiedFinding{Source: project.FindingSourceAI, Status: project.FindingStatusRetired, FileHash: "old", DetectedAt: analyzed, Location: project.FindingLocation{Path: "calc.go", StartLine: 3}}
	later := analyzed.Add(time.Hour)
	onOldLine := project.UnifiedFinding{Source: project.FindingSourceAI, Status: project.FindingStatusOpen, Severity: "medium", FileHash: "new", DetectedAt: later, Location: project.FindingLocation{Path: "calc.go", StartLine: 3}}
	onChangedLine := onOldLine
	onChangedLine.Location.StartLine = 5
	high := onOldLine
	high.Severity = "high"
	scoper := newFindingScoper(root, ProjectProfile{FinishLine: "medium"}, []project.UnifiedFinding{previous, onOldLine}, map[string]string{"calc.go": "new"}, newLineAges())
	if scope := scoper.scope(onOldLine); scope == nil || scope.Reason != ScopeUnchanged {
		t.Fatalf("an issue on a line the last analysis saw unchanged is a second look, got %+v", scope)
	}
	if scope := scoper.scope(onChangedLine); scope != nil {
		t.Fatalf("an issue on a changed (uncommitted) line counts, got %+v", scope)
	}
	if scope := scoper.scope(high); scope != nil {
		t.Fatalf("a high issue always counts, got %+v", scope)
	}
}

func TestParseBlameAgesDatesUncommittedLinesAfterEveryAnalysis(t *testing.T) {
	committed := strings.Repeat("a", 40)
	out := committed + " 1 1 1\nauthor A\ncommitter-time 1700000000\n\tline one\n" + strings.Repeat("0", 40) + " 2 2 1\nauthor Not Committed Yet\ncommitter-time 1800000000\n\tline two\n" + committed + " 3 3\n\tline three\n"
	ages := parseBlameAges(out)
	if len(ages) != 3 || ages[0].Unix() != 1700000000 || !ages[1].Equal(uncommitted) || ages[2].Unix() != 1700000000 {
		t.Fatalf("ages = %v", ages)
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newProfileTestManager(t *testing.T, root string) *project.Manager {
	t.Helper()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAnalysisLeavesTestFilesOutUnlessTheProfileHasThemAnalyzed(t *testing.T) {
	root := t.TempDir()
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	exclusions, err := newAnalysisExclusions(root, policy)
	if err != nil {
		t.Fatal(err)
	}
	test := project.IndexFile{Path: "calc_test.go", Language: "Go"}
	if reason := exclusions.reason(test); reason != analysisTestFileExclusion {
		t.Fatalf("test file reason = %q", reason)
	}
	if reason := exclusions.reason(project.IndexFile{Path: "calc.go", Language: "Go"}); reason != "" {
		t.Fatalf("source file reason = %q", reason)
	}
	service := &Service{manager: newProfileTestManager(t, root)}
	if _, err := service.SaveProjectProfile(ProjectProfile{Kind: ProfileTeam, AnalyzeTests: true}); err != nil {
		t.Fatal(err)
	}
	if exclusions, _ = newAnalysisExclusions(root, policy); exclusions.reason(test) != "" {
		t.Fatal("with test analysis on, test files are analyzed")
	}
}
