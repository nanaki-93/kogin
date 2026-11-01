package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestProjectExecutionTrustDeniesCommandsUntilExplicitlyConfirmed(t *testing.T) {
	root := t.TempDir()
	writeGoFixture(t, root)
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	service, err := New(scopedTestConfig("http://127.0.0.1:1"), manager)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "")
	if err != nil || trust.Trusted || len(trust.Commands) != 2 || strings.Join(trust.Commands[0], " ") != "go test ./..." || strings.Join(trust.Commands[1], " ") != "go vet ./..." {
		t.Fatalf("initial trust = %+v, %v", trust, err)
	}
	if err := service.requireProjectExecutionTrust(context.Background(), analysis.ProjectRevision); err == nil || !strings.Contains(err.Error(), "trusted local execution") {
		t.Fatalf("untrusted execution error = %v", err)
	}
	if _, err := service.TrustProjectExecution(context.Background(), analysis.ProjectRevision, false); err == nil {
		t.Fatal("denied confirmation authorized project execution")
	}
	trust, err = service.TrustProjectExecution(context.Background(), analysis.ProjectRevision, true)
	if err != nil || !trust.Trusted {
		t.Fatalf("trusted execution = %+v, %v", trust, err)
	}
	taskTrust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "TestRun")
	if err != nil || strings.Join(taskTrust.Commands[0], " ") != "go test ./... -run ^TestRun$" {
		t.Fatalf("task test scope = %+v, %v", taskTrust, err)
	}
	if _, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "not-a-test"); err == nil {
		t.Fatal("invalid task test scope was accepted")
	}
	if unicodeTrust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, "TestÅ"); err != nil || strings.Join(unicodeTrust.Commands[0], " ") != "go test ./... -run ^TestÅ$" {
		t.Fatalf("unicode task test scope = %+v, %v", unicodeTrust, err)
	}
	revoked, err := service.TrustProjectExecution(context.Background(), analysis.ProjectRevision, false)
	if err != nil || revoked.Trusted {
		t.Fatalf("revoked execution trust = %+v, %v", revoked, err)
	}
	if err := service.requireProjectExecutionTrust(context.Background(), analysis.ProjectRevision); err == nil || !strings.Contains(err.Error(), "trusted local execution") {
		t.Fatalf("revoked execution error = %v", err)
	}
}

func TestProjectExecutionTrustRebindsAfterReindex(t *testing.T) {
	service, _, _ := newTrustedService(t, map[string]string{"go.mod": "module fixture\n\ngo 1.22\n", "main.go": "package fixture\n"})
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	if trust, err := service.ExecutionTrust(context.Background(), analysis.ProjectRevision, ""); err != nil || !trust.Trusted || trust.WorkspaceHash == "" {
		t.Fatalf("reindexed trust = %+v, %v", trust, err)
	}
}

func TestProjectCodeProcessRechecksTheOriginalRevisionAtStart(t *testing.T) {
	service, root, originalRevision := newTrustedService(t, map[string]string{"go.mod": "module fixture\n\ngo 1.22\n", "main.go": "package fixture\n"})
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProjectRevision == originalRevision {
		t.Fatal("fixture did not change project revision")
	}
	if _, err := service.TrustProjectExecution(context.Background(), updated.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	_, err = service.runCheckCommand(context.Background(), t.TempDir(), []string{"go", "version"}, originalRevision, true)
	if !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("old project command started after project change: %v", err)
	}
}

func writeGoFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
