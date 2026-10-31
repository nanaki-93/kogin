package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestScanSecurityFileStoresFreshDeterministicReport(t *testing.T) {
	source := "package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n"
	service, root, revision := newTrustedService(t, map[string]string{"main.go": source, "main_test.go": source, "empty.go": "package fixture\nfunc Empty() {}\n"})
	report, err := service.ScanSecurityFile(context.Background(), "main.go", revision)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != project.SecurityStatusCompleted || report.RuleSetVersion != project.SecurityRuleSetVersion || len(report.Findings) != 1 || report.Findings[0].EvidenceKind != "rule_match" {
		t.Fatalf("security report = %+v", report)
	}
	cached, err := service.ScanSecurityFile(context.Background(), "main.go", revision)
	if err != nil || cached.GeneratedAt != report.GeneratedAt {
		t.Fatalf("cached report = %+v, %v", cached, err)
	}
	empty, err := service.ScanSecurityFile(context.Background(), "main_test.go", revision)
	if err != nil || empty.Status != project.SecurityStatusCompleted || len(empty.Findings) != 1 {
		t.Fatalf("test fixture report = %+v, %v", empty, err)
	}
	noMatches, err := service.ScanSecurityFile(context.Background(), "empty.go", revision)
	if err != nil || noMatches.Status != project.SecurityStatusCompletedEmpty || len(noMatches.Findings) != 0 {
		t.Fatalf("empty security report = %+v, %v", noMatches, err)
	}
	input := project.SecurityReportInput{ProjectID: report.ProjectID, ProjectRevision: report.ProjectRevision, Path: report.Path, ContentHash: report.ContentHash, Source: report.Source, RuleSetVersion: report.RuleSetVersion, ContextPolicyVersion: report.ContextPolicyVersion}
	if stored, err := project.LoadSecurityFileReport(root, input); err != nil || stored == nil || stored.Status != project.SecurityStatusCompleted {
		t.Fatalf("stored deterministic report = %+v, %v", stored, err)
	}
}

func TestScanSecurityFileBoundsMatchesAndRevalidatesCacheHits(t *testing.T) {
	source := "package fixture\nimport \"os/exec\"\nfunc Run(input string) {\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n}\n"
	service, root, revision := newTrustedService(t, map[string]string{"main.go": source, "cached.go": "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"sh\", \"-c\", input) }\n"})
	report, err := service.ScanSecurityFile(context.Background(), "main.go", revision)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != project.SecurityStatusPartial || len(report.Findings) != 5 || report.Reason == "" {
		t.Fatalf("truncated report = %+v", report)
	}
	partialCached, err := service.ScanSecurityFile(context.Background(), "main.go", revision)
	if err != nil || partialCached.GeneratedAt != report.GeneratedAt || partialCached.Status != project.SecurityStatusPartial {
		t.Fatalf("partial cache hit = %+v, %v", partialCached, err)
	}
	if _, err := service.ScanSecurityFile(context.Background(), "cached.go", revision); err != nil {
		t.Fatal(err)
	}

	load := service.loadSecurityFileReport
	ctx, cancel := context.WithCancel(context.Background())
	loaded := false
	service.loadSecurityFileReport = func(cacheRoot string, input project.SecurityReportInput) (*project.SecurityFileReport, error) {
		loaded = true
		report, err := load(cacheRoot, input)
		cancel()
		return report, err
	}
	if _, err := service.ScanSecurityFile(ctx, "main.go", revision); !errors.Is(err, context.Canceled) || !loaded {
		t.Fatalf("canceled cache hit = %v, loader invoked = %t", err, loaded)
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
	if _, err := service.ScanSecurityFile(context.Background(), "main.go", revision); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("reindexed cache hit = %v", err)
	}
}

func TestScanSecurityFileRejectsProjectChangeAfterCacheLoad(t *testing.T) {
	source := "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"sh\", \"-c\", input) }\n"
	service, _, revision := newTrustedService(t, map[string]string{"main.go": source})
	if _, err := service.ScanSecurityFile(context.Background(), "main.go", revision); err != nil {
		t.Fatal(err)
	}
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
	if _, err := service.ScanSecurityFile(context.Background(), "main.go", revision); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("changed-project cache hit = %v", err)
	}
}

func TestScanSecurityFileDoesNotContactConfiguredProvider(t *testing.T) {
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
	analysis, err := manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ScanSecurityFile(context.Background(), "main.go", analysis.ProjectRevision); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("security scan sent %d provider requests", calls)
	}
}

func TestScanSecurityFileRejectsStaleExcludedSymlinkAndUnsupportedPaths(t *testing.T) {
	service, root, revision := newTrustedService(t, map[string]string{
		"main.go":          "package fixture\nfunc Run() {}\n",
		"notes.txt":        "run it\n",
		".mini-orcaignore": "excluded.go\n",
		"excluded.go":      "package fixture\nfunc Excluded() {}\n",
	})
	if _, err := service.ScanSecurityFile(context.Background(), "main.go", "stale"); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale scan = %v", err)
	}
	if _, err := service.ScanSecurityFile(context.Background(), "notes.txt", revision); !errors.Is(err, project.ErrSecurityRulesUnavailable) {
		t.Fatalf("unsupported scan = %v", err)
	}
	if _, err := service.ScanSecurityFile(context.Background(), "excluded.go", revision); !errors.Is(err, project.ErrExcludedFile) {
		t.Fatalf("excluded scan = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ScanSecurityFile(context.Background(), "linked.go", revision); !errors.Is(err, project.ErrExcludedFile) {
		t.Fatalf("symlink scan = %v", err)
	}
}
