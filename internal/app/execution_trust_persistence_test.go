package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func reopenTrustFixture(t *testing.T, original *Service, root string) (*Service, string) {
	t.Helper()
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(original.global, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreActiveProject(root, &project.Analysis{Name: "reopened"}); err != nil {
		t.Fatal(err)
	}
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return s, index.ProjectRevision
}

func TestRememberedTrustSurvivesRestartAndRevocation(t *testing.T) {
	s, _, revision := newTrustedService(t, map[string]string{"go.mod": "module fixture\n\ngo 1.22\n", "main.go": "package fixture\n"})
	root := s.manager.Root()
	path, err := executionTrustPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(path, root+string(filepath.Separator)) {
		t.Fatal("consent was saved inside the project")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("saved consent is not private: %v %v", info, err)
	}
	first, _ := s.ExecutionTrust(context.Background(), revision, "")
	reopened, revision := reopenTrustFixture(t, s, root)
	trust, err := reopened.ExecutionTrust(context.Background(), revision, "TestRun")
	if err != nil || !trust.Trusted || trust.WorkspaceHash != first.WorkspaceHash {
		t.Fatalf("reopen lost trust: %+v %v", trust, err)
	}
	if _, err := reopened.TrustProjectExecution(context.Background(), revision, false); err != nil {
		t.Fatal(err)
	}
	reopened, revision = reopenTrustFixture(t, s, root)
	trust, err = reopened.ExecutionTrust(context.Background(), revision, "")
	if err != nil || trust.Trusted || trust.WorkspaceHash != "" {
		t.Fatalf("revocation did not survive restart: %+v %v", trust, err)
	}
	if err := reopened.requireProjectExecutionTrust(context.Background(), revision); err == nil {
		t.Fatal("revoked consent allowed project code")
	}
}

func TestRememberedTrustRevalidatesSourcesAndDoesNotCrossProjects(t *testing.T) {
	s, root, originalRevision := newTrustedService(t, map[string]string{"main.go": "package fixture\n"})
	first, _ := s.ExecutionTrust(context.Background(), originalRevision, "")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package fixture\nfunc NewCode() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.resetExecutionTrust(true)
	if _, err := s.ExecutionTrust(context.Background(), originalRevision, ""); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("remembered trust accepted stale indexed sources: %v", err)
	}
	updated, err := s.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := s.ExecutionTrust(context.Background(), updated.ProjectRevision, "")
	if err != nil || !trust.Trusted || trust.WorkspaceHash == first.WorkspaceHash {
		t.Fatalf("reindex reused old execution authority: %+v %v", trust, err)
	}
	if err := s.requireProjectExecutionTrust(context.Background(), originalRevision); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("old job retained execution authority: %v", err)
	}
	other := t.TempDir()
	writeGoFixture(t, other)
	if err := s.RestoreActiveProject(other, &project.Analysis{Name: "other"}); err != nil {
		t.Fatal(err)
	}
	index, _ := s.manager.Index()
	if trust, err := s.ExecutionTrust(context.Background(), index.ProjectRevision, ""); err != nil || trust.Trusted {
		t.Fatalf("project inherited another project's consent: %+v %v", trust, err)
	}
	if err := s.RestoreActiveProject(root, &project.Analysis{Name: "original"}); err != nil {
		t.Fatal(err)
	}
	if trust, err := s.ExecutionTrust(context.Background(), updated.ProjectRevision, ""); err != nil || !trust.Trusted {
		t.Fatalf("returning to trusted project lost consent: %+v %v", trust, err)
	}
	current, _ := workspaceFingerprint(context.Background(), root)
	if trust, _ := s.ExecutionTrust(context.Background(), updated.ProjectRevision, ""); trust.WorkspaceHash != current {
		t.Fatal("restored authority does not match current workspace")
	}
}

func TestRememberedTrustRejectsIncompatibleOrCorruptSettings(t *testing.T) {
	for _, variant := range []string{"root", "project", "policy", "commands", "corrupt", "trailing", "incomplete", "symlink"} {
		t.Run(variant, func(t *testing.T) {
			s, _, _ := newTrustedService(t, map[string]string{"main.go": "package fixture\n"})
			root := s.manager.Root()
			path, _ := executionTrustPath(root)
			saved, err := readRememberedExecutionTrust(root)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "root":
				saved.Root = t.TempDir()
			case "project":
				saved.ProjectID = "other"
			case "policy":
				saved.Policy = "retired-policy"
			case "commands":
				saved.Commands = [][]string{{"make", "test"}}
			}
			data, _ := json.Marshal(saved)
			switch variant {
			case "corrupt":
				data = []byte("{")
			case "trailing":
				data = append(data, []byte(" {}")...)
			case "incomplete":
				data = []byte(`{"trusted":true}`)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if variant == "symlink" {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			reopened, revision := reopenTrustFixture(t, s, root)
			trust, err := reopened.ExecutionTrust(context.Background(), revision, "")
			if err == nil && trust.Trusted {
				t.Fatalf("%s settings granted trust", variant)
			}
			if (variant == "corrupt" || variant == "trailing" || variant == "incomplete" || variant == "symlink") && err == nil {
				t.Fatalf("%s settings failure was hidden", variant)
			}
		})
	}
}

func TestRememberedTrustWriteFailureDoesNotGrantExecution(t *testing.T) {
	s, _, revision := newTrustedService(t, map[string]string{"main.go": "package fixture\n"})
	s.clearExecutionTrust()
	root := s.manager.Root()
	path, _ := executionTrustPath(root)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustProjectExecution(context.Background(), revision, true); err == nil || !strings.Contains(err.Error(), "save execution trust") {
		t.Fatalf("failed persistence reported consent: %v", err)
	}
	if trust, err := s.ExecutionTrust(context.Background(), revision, ""); err != nil || trust.Trusted {
		t.Fatalf("failed persistence granted execution: %+v %v", trust, err)
	}
}

func TestCanceledTrustDoesNotRestoreOrPersistConsent(t *testing.T) {
	s, _, revision := newTrustedService(t, map[string]string{"main.go": "package fixture\n"})
	root := s.manager.Root()
	path, _ := executionTrustPath(root)
	s.resetExecutionTrust(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ExecutionTrust(ctx, revision, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrustProjectExecution(ctx, revision, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled confirmation accepted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("canceled confirmation saved consent")
	}
}
