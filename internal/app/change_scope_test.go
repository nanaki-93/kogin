package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const scopedChange = `{"explanation":"Add Helper with a regression test.","changes":[{"path":"pkg/helper.go","content":"package pkg\n\nfunc Helper() int { return 2 }\n"},{"path":"pkg/helper_test.go","content":"package pkg\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) {\n\tif Helper() != 2 {\n\t\tt.Fatal(\"wrong result\")\n\t}\n}\n"}]}`

// scopedFixture is a Go module with the task's package, an unrelated package,
// and a generated file in the task's package that .gitignore excludes.
func scopedFixture(t *testing.T) (*Service, string, *ChangeSession) {
	t.Helper()
	server := changeProvider(t, func() string { return scopedChange })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	for name, content := range map[string]string{
		"go.mod":              "module example.test/scoped\n\ngo 1.22\n",
		".gitignore":          "pkg/zz_generated.go\n",
		"pkg/existing.go":     "package pkg\n\nfunc Existing() int { return 1 }\n",
		"other/other.go":      "package other\n\nfunc Other() int { return 3 }\n",
		"docs/notes.md":       "# Notes\n",
		"pkg/zz_generated.go": "package pkg\n\nconst generated = 1\n",
	} {
		writeFixtureFile(t, root, name, content)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := prepareChangeFixture(t, service, "pkg/helper.go", "pkg/helper_test.go")
	if _, err := service.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	return service, root, session
}

func writeFixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scopePaths(session *ChangeSession) []string {
	var paths []string
	for _, file := range session.Scope {
		paths = append(paths, file.Path)
	}
	return paths
}

func TestFreshnessScopeRecordsPackageFilesAndManifests(t *testing.T) {
	_, _, session := scopedFixture(t)
	if session.ScopeVersion != changeScopeVersion {
		t.Fatalf("scope not captured: %+v", session.ScopeVersion)
	}
	want := []string{"go.mod", "pkg/existing.go"}
	if got := scopePaths(session); !slices.Equal(got, want) {
		t.Fatalf("scope = %v, want %v (targets, ignored and unrelated files excluded)", got, want)
	}
}

func TestUnrelatedChangesKeepTheProposalCurrentButOutdateChecks(t *testing.T) {
	service, root, session := scopedFixture(t)
	ctx := context.Background()
	checked, err := service.CheckChange(ctx, session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session), CheckOptions: CheckOptions{RunTests: true}})
	if err != nil || !requiredChecksPassed(checked.Checks) || checked.ChecksWorkspaceHash == "" {
		t.Fatalf("checks: %+v %v", checked, err)
	}
	writeFixtureFile(t, root, "other/other.go", "package other\n\nfunc Other() int { return 4 }\n")
	writeFixtureFile(t, root, "docs/notes.md", "# Notes\n\nChanged.\n")
	writeFixtureFile(t, root, "pkg/zz_generated.go", "package pkg\n\nconst generated = 2\n")
	index, err := service.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if index.ProjectRevision == session.ProjectRevision {
		t.Fatal("fixture did not change the project revision")
	}
	read, err := service.ChangeSession(ctx, session.ID)
	if err != nil || read.Freshness != "current" || read.StaleReason != "" || !read.ChecksOutdated {
		t.Fatalf("unrelated edits: freshness=%q reason=%q outdated=%v err=%v", read.Freshness, read.StaleReason, read.ChecksOutdated, err)
	}
	history, err := service.ChangeHistory(ctx)
	if err != nil || len(history) != 1 || history[0].Freshness != "current" {
		t.Fatalf("listed freshness: %+v %v", history, err)
	}
	current := changeIdentity(checked)
	current.ProjectRevision = index.ProjectRevision
	if _, err := service.ReviewChange(ctx, session.ID, ChangeReviewRequest{ChangeIdentity: current}); !errors.Is(err, errChecksOutdated) {
		t.Fatalf("approval used checks from an older tree: %v", err)
	}
	rechecked, err := service.CheckChange(ctx, session.ID, ChangeCheckRequest{ChangeIdentity: current, CheckOptions: CheckOptions{RunTests: true}})
	if err != nil || !requiredChecksPassed(rechecked.Checks) {
		t.Fatalf("checks on the current tree: %+v %v", rechecked, err)
	}
	if _, err := service.ReviewChange(ctx, session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(rechecked)}); err != nil {
		t.Fatalf("approval after fresh checks: %v", err)
	}
	if _, err := service.ApplyChange(ctx, session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(rechecked), Confirm: true}); !errors.Is(err, ErrCheckoutTask) {
		t.Fatalf("the approved task did not pass the gate after unrelated edits: %v", err)
	}
}

func TestScopeChangesMakeTheProposalStale(t *testing.T) {
	for name, change := range map[string]struct{ path, content, reason string }{
		"manifest":    {"go.mod", "module example.test/scoped\n\ngo 1.23\n", "go.mod changed"},
		"package":     {"pkg/existing.go", "package pkg\n\nfunc Existing() int { return 5 }\n", "pkg/existing.go changed"},
		"new sibling": {"pkg/added.go", "package pkg\n\nfunc Helper() int { return 9 }\n", "pkg/added.go changed"},
	} {
		t.Run(name, func(t *testing.T) {
			service, root, session := scopedFixture(t)
			writeFixtureFile(t, root, change.path, change.content)
			read, err := service.ChangeSession(context.Background(), session.ID)
			if err != nil || read.Freshness != "stale" || !strings.HasPrefix(read.StaleReason, change.reason) {
				t.Fatalf("freshness=%q reason=%q err=%v", read.Freshness, read.StaleReason, err)
			}
			if _, err := service.CheckChange(context.Background(), session.ID, ChangeCheckRequest{ChangeIdentity: changeIdentity(session)}); err == nil {
				t.Fatal("stale proposal checked")
			}
			history, err := service.ChangeHistory(context.Background())
			if err != nil || history[0].Freshness != "stale" {
				t.Fatalf("listed freshness: %+v %v", history, err)
			}
		})
	}
}

func TestLegacySessionsKeepWholeTreeFreshness(t *testing.T) {
	service, root, session := scopedFixture(t)
	session.ScopeVersion, session.Scope = 0, nil
	if err := storeChangeSession(root, session); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "docs/notes.md", "# Notes\n\nChanged.\n")
	read, err := service.ChangeSession(context.Background(), session.ID)
	if err != nil || read.Freshness != "stale" {
		t.Fatalf("legacy session ignored an unrelated change: %q %v", read.Freshness, err)
	}
}

func TestStoredScopeMustBeSortedAndLocal(t *testing.T) {
	for _, scope := range [][]ChangeScopeFile{
		{{Path: "b.go", Hash: "h"}, {Path: "a.go", Hash: "h"}},
		{{Path: "../escape.go", Hash: "h"}},
		{{Path: "a.go"}},
	} {
		if err := validateStoredScope(&ChangeSession{ScopeVersion: changeScopeVersion, Scope: scope}); err == nil {
			t.Fatalf("corrupt scope accepted: %+v", scope)
		}
	}
	if err := validateStoredScope(&ChangeSession{Scope: []ChangeScopeFile{{Path: "a.go", Hash: "h"}}}); err == nil {
		t.Fatal("unversioned scope accepted")
	}
}
