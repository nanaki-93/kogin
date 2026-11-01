package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// securityRulesStageResult scans one file as an analysis run's security_rules
// stage does.
func securityRulesStageResult(t *testing.T, s *Service, path string) (analysisFileStageResult, error) {
	t.Helper()
	request := analysisStageRequestFor(t, s, path, AnalysisStageSecurityRules)
	return s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
}

func TestSecurityRulesStageStoresFreshDeterministicReport(t *testing.T) {
	source := "package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n"
	service, root, _ := newTrustedService(t, map[string]string{"main.go": source, "main_test.go": source, "empty.go": "package fixture\nfunc Empty() {}\n"})
	result, err := securityRulesStageResult(t, service, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	report := result.Security
	if result.Progress.Status != AnalysisStageCompleted || result.Progress.Cached || report == nil || report.Status != project.SecurityStatusCompleted || report.RuleSetVersion != project.SecurityRuleSetVersion || len(report.Findings) != 1 || report.Findings[0].EvidenceKind != "rule_match" {
		t.Fatalf("security result = %+v", result)
	}
	cached, err := securityRulesStageResult(t, service, "main.go")
	if err != nil || !cached.Progress.Cached || cached.Security == nil || cached.Security.GeneratedAt != report.GeneratedAt {
		t.Fatalf("cached result = %+v, %v", cached, err)
	}
	fixture, err := securityRulesStageResult(t, service, "main_test.go")
	if err != nil || fixture.Security == nil || fixture.Security.Status != project.SecurityStatusCompleted || len(fixture.Security.Findings) != 1 {
		t.Fatalf("test fixture result = %+v, %v", fixture, err)
	}
	noMatches, err := securityRulesStageResult(t, service, "empty.go")
	if err != nil || noMatches.Progress.Status != AnalysisStageCompletedEmpty || noMatches.Security == nil || noMatches.Security.Status != project.SecurityStatusCompletedEmpty || len(noMatches.Security.Findings) != 0 {
		t.Fatalf("empty security result = %+v, %v", noMatches, err)
	}
	input := project.SecurityReportInput{ProjectID: report.ProjectID, ProjectRevision: report.ProjectRevision, Path: report.Path, ContentHash: report.ContentHash, Source: report.Source, RuleSetVersion: report.RuleSetVersion, ContextPolicyVersion: report.ContextPolicyVersion}
	if stored, err := project.LoadSecurityFileReport(root, input); err != nil || stored == nil || stored.Status != project.SecurityStatusCompleted {
		t.Fatalf("stored deterministic report = %+v, %v", stored, err)
	}
}

func TestSecurityRulesStageBoundsMatchesAndRevalidatesCacheHits(t *testing.T) {
	source := "package fixture\nimport \"os/exec\"\nfunc Run(input string) {\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n}\n"
	service, root, _ := newTrustedService(t, map[string]string{"main.go": source, "cached.go": "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"sh\", \"-c\", input) }\n"})
	result, err := securityRulesStageResult(t, service, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	report := result.Security
	if result.Progress.Status != AnalysisStagePartial || report == nil || report.Status != project.SecurityStatusPartial || len(report.Findings) != 5 || report.Reason == "" {
		t.Fatalf("truncated result = %+v", result)
	}
	partialCached, err := securityRulesStageResult(t, service, "main.go")
	if err != nil || !partialCached.Progress.Cached || partialCached.Progress.Status != AnalysisStagePartial || partialCached.Security == nil || partialCached.Security.GeneratedAt != report.GeneratedAt {
		t.Fatalf("partial cache hit = %+v, %v", partialCached, err)
	}
	if _, err := securityRulesStageResult(t, service, "cached.go"); err != nil {
		t.Fatal(err)
	}

	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSecurityRules)
	load := service.loadSecurityFileReport
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loaded := false
	service.loadSecurityFileReport = func(cacheRoot string, input project.SecurityReportInput) (*project.SecurityFileReport, error) {
		loaded = true
		report, err := load(cacheRoot, input)
		cancel()
		return report, err
	}
	if canceled, err := service.analyzeFileStage(ctx, request, analysisStageAuthorityFor(t, request)); !errors.Is(err, context.Canceled) || !loaded || canceled.Security != nil {
		t.Fatalf("canceled cache hit = %+v, %v, loader invoked = %t", canceled, err, loaded)
	}

	service.loadSecurityFileReport = func(cacheRoot string, input project.SecurityReportInput) (*project.SecurityFileReport, error) {
		report, err := load(cacheRoot, input)
		if writeErr := os.WriteFile(filepath.Join(root, "changed.go"), []byte("package fixture\n"), 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, reindexErr := service.Reindex(); reindexErr != nil {
			t.Fatal(reindexErr)
		}
		return report, err
	}
	if stale, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); !errors.Is(err, project.ErrRevisionConflict) || stale.Progress.Status != AnalysisStageStale || stale.Security != nil {
		t.Fatalf("reindexed cache hit = %+v, %v", stale, err)
	}
}

func TestSecurityRulesStageRejectsProjectChangeAfterCacheLoad(t *testing.T) {
	source := "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"sh\", \"-c\", input) }\n"
	service, _, _ := newTrustedService(t, map[string]string{"main.go": source})
	if _, err := securityRulesStageResult(t, service, "main.go"); err != nil {
		t.Fatal(err)
	}
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSecurityRules)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "other.go"), []byte("package other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	load := service.loadSecurityFileReport
	service.loadSecurityFileReport = func(root string, input project.SecurityReportInput) (*project.SecurityFileReport, error) {
		report, err := load(root, input)
		if setErr := service.manager.Set(other, &project.Analysis{Name: "other", Path: other}); setErr != nil {
			t.Fatal(setErr)
		}
		return report, err
	}
	if result, err := service.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); !errors.Is(err, project.ErrRevisionConflict) || result.Security != nil {
		t.Fatalf("changed-project cache hit = %+v, %v", result, err)
	}
}

func TestSecurityRulesStageDoesNotContactConfiguredProvider(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer provider.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig(provider.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStageSecurityRules)
	authority := analysisStageAuthorityFor(t, request)
	authority.BeforeAttempt = func(context.Context, AnalysisRunIdentity, AnalysisFileIdentity, AnalysisStage) error {
		t.Error("security rules reserved a model request")
		return nil
	}
	result, err := service.analyzeFileStage(context.Background(), request, authority)
	if err != nil || result.Security == nil || result.Progress.Attempts != 0 {
		t.Fatalf("security rules result = %+v, %v", result, err)
	}
	if calls != 0 {
		t.Fatalf("security scan sent %d provider requests", calls)
	}
}

func TestSecurityRulesScanRejectsStaleExcludedSymlinkAndUnsupportedPaths(t *testing.T) {
	service, root, revision := newTrustedService(t, map[string]string{
		"main.go":       "package fixture\nfunc Run() {}\n",
		"notes.txt":     "run it\n",
		".kasuriignore": "excluded.go\n",
		"excluded.go":   "package fixture\nfunc Excluded() {}\n",
	})
	if _, err := service.prepareSecurityRulesScan("main.go", "stale"); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale scan = %v", err)
	}
	if _, err := service.prepareSecurityRulesScan("notes.txt", revision); !errors.Is(err, project.ErrSecurityRulesUnavailable) {
		t.Fatalf("unsupported scan = %v", err)
	}
	if _, err := service.prepareSecurityRulesScan("excluded.go", revision); !errors.Is(err, project.ErrExcludedFile) {
		t.Fatalf("excluded scan = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.prepareSecurityRulesScan("linked.go", revision); !errors.Is(err, project.ErrExcludedFile) {
		t.Fatalf("symlink scan = %v", err)
	}
}
