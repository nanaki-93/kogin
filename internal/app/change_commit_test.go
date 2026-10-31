package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	value, err := gitRead(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(value)
}

func initializeChangeGit(t *testing.T, service *Service, root string) {
	t.Helper()
	testGit(t, root, "init", "-b", "main")
	testGit(t, root, "config", "user.name", "Mini Orca Test")
	testGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".mini-orca/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", ".")
	testGit(t, root, "commit", "-m", "Initial fixture")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
}

func TestChangeCommitScopesStagingAndSurvivesRestart(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	initializeChangeGit(t, service, root)
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("staged by user\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "unrelated.txt")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	before := testGit(t, root, "rev-parse", "HEAD")
	receipt, err := service.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), GitIdentity: *session.Git, Confirm: true, CommitMessage: "Fix Run and document behavior"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Commit == nil || receipt.Commit.State != "committed" || receipt.Commit.Error != "" {
		t.Fatalf("commit receipt: %+v", receipt.Commit)
	}
	if got := testGit(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != "docs/change.md\nmain.go" {
		t.Fatalf("committed paths: %s", got)
	}
	if got := testGit(t, root, "diff", "--cached", "--name-only"); got != "unrelated.txt" {
		t.Fatalf("staging changed: %s", got)
	}
	if testGit(t, root, "rev-parse", "HEAD^") != before {
		t.Fatal("unexpected parent")
	}
	restarted, err := New(scopedTestConfig(server.URL), service.manager)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.ChangeRecovery()
	if err != nil || recovered.Commit.OID != receipt.Commit.OID {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	history, err := restarted.ChangeHistory(context.Background())
	if err != nil || history[0].Commit.OID != receipt.Commit.OID {
		t.Fatalf("history: %+v %v", history, err)
	}
	identity := changeIdentity(session)
	identity.ProjectRevision = receipt.ProjectRevision
	if _, err := restarted.UndoChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: identity, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if testGit(t, root, "rev-parse", "HEAD") != receipt.Commit.OID {
		t.Fatal("Undo rewrote Git history")
	}
}

func TestChangeCommitFailureKeepsAppliedState(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	initializeChangeGit(t, service, root)
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	testGit(t, root, "config", "user.name", "")
	request := ChangeApplyRequest{ChangeIdentity: changeIdentity(session), GitIdentity: *session.Git, Confirm: true, CommitMessage: "Fix behavior"}
	receipt, err := service.ApplyChange(context.Background(), session.ID, request)
	if err != nil || receipt.State != "applied" || receipt.Commit.State != "failed" {
		t.Fatalf("failed commit lost applied state: %+v %v (commit %+v)", receipt, err, receipt.Commit)
	}
	if testGit(t, root, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("failed commit created a commit")
	}
}

func TestChangeCommitRejectsDirtyTargetAndChangedBranch(t *testing.T) {
	for _, mode := range []string{"dirty", "staged", "mode", "branch", "head"} {
		t.Run(mode, func(t *testing.T) {
			server := changeProvider(t, func() string { return groupedChangeResponse })
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			initializeChangeGit(t, service, root)
			if mode == "dirty" || mode == "staged" {
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() { /* user edit */ }\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if mode == "staged" {
					testGit(t, root, "add", "main.go")
				}
				if _, err := service.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "mode" {
				if err := os.Chmod(filepath.Join(root, "main.go"), 0755); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
			original, _ := os.ReadFile(filepath.Join(root, "main.go"))
			if mode == "branch" {
				testGit(t, root, "switch", "-c", "other")
			}
			if mode == "head" {
				testGit(t, root, "commit", "--allow-empty", "-m", "External commit")
			}
			_, err := service.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), GitIdentity: *session.Git, Confirm: true, CommitMessage: "Fix"})
			if err == nil {
				t.Fatal("stale/dirty proposal accepted")
			}
			after, _ := os.ReadFile(filepath.Join(root, "main.go"))
			if string(after) != string(original) {
				t.Fatal("rejected commit changed source")
			}
			if mode == "branch" || mode == "head" {
				if !errors.Is(err, project.ErrRevisionConflict) {
					t.Fatalf("stale identity: %v", err)
				}
			}
		})
	}
}
