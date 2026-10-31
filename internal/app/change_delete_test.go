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

// appliedChangeFixture applies an approved task whose project has a passing test.
func appliedChangeFixture(t *testing.T) (*Service, string, *ChangeSession, ChangeIdentity) {
	t.Helper()
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/verification\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	test := "package main\nimport \"testing\"\nfunc TestRun(t *testing.T) { Run() }\n"
	if err := os.WriteFile(filepath.Join(root, "main_test.go"), []byte(test), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	receipt, err := service.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	identity := changeIdentity(session)
	identity.ProjectRevision = receipt.ProjectRevision
	return service, root, session, identity
}

func TestDeleteChangeKeepsATaskUndoNeeds(t *testing.T) {
	service, root, applied, _ := appliedChangeFixture(t)
	// The applied task's Apply can still be undone, and Undo restores it.
	stored, err := loadChangeSession(root, applied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteChange(context.Background(), applied.ID, changeIdentity(stored)); !errors.Is(err, ErrChangeInUse) {
		t.Fatalf("a task Undo needs was deleted: %v", err)
	}

}

func TestDeleteChangeRemovesAStaleTask(t *testing.T) {
	server := changeProvider(t, func() string { return workflowImplementation })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	stale := prepareChangeFixture(t, service, "new.go")
	// The task goes stale when a file it depends on changes.
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	wrong := changeIdentity(stale)
	wrong.Hash = "sha256:other"
	if err := service.DeleteChange(context.Background(), stale.ID, wrong); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("a different version was deleted: %v", err)
	}
	events, unsubscribe := service.SubscribeEvents()
	defer unsubscribe()
	if err := service.DeleteChange(context.Background(), stale.ID, changeIdentity(stale)); err != nil {
		t.Fatal(err)
	}
	if event := <-events; event.Kind != "workflow" || event.SessionID != stale.ID || event.Status != "deleted" {
		t.Fatalf("event: %+v", event)
	}
	if _, err := loadChangeSession(root, stale.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task file remains: %v", err)
	}
	history, err := service.ChangeHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range history {
		if entry.ID == stale.ID {
			t.Fatal("deleted task is still listed")
		}
	}
	if err := service.DeleteChange(context.Background(), stale.ID, changeIdentity(stale)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second delete: %v", err)
	}
}

// Tasks saved before the code scope was renamed name it "function". Their
// recovery journal must stay readable, or it blocks deleting any task.
func TestDeleteChangeReadsAJournalFromBeforeTheCodeScopeRename(t *testing.T) {
	outputs := [3]func() string{
		func() string { return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	}
	s, root, initial := workflowService(t, outputs)
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	session := finishedWorkflow(t, s, initial.ID)
	reviewed, err := s.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(session)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyChange(context.Background(), reviewed.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(reviewed), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".mini-orca", "changes", "last-mutation.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := string(data)
	for _, field := range []string{`"review":"code"`, `"scope":"code"`, `"profile":"code"`} {
		if !strings.Contains(legacy, field) {
			t.Fatalf("journal fixture does not name %s", field)
		}
		legacy = strings.ReplaceAll(legacy, field, strings.Replace(field, `"code"`, `"function"`, 1))
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ChangeRecovery(); err != nil {
		t.Fatalf("legacy journal is unreadable: %v", err)
	}
	journal, err := readChangeJournal(root)
	if err != nil || journal == nil || journal.Session.Workflow == nil {
		t.Fatalf("legacy journal: %+v, %v", journal, err)
	}
	if workflow := journal.Session.Workflow; workflow.Models.Review != "code" || workflow.Stages[2].Model.Scope != "code" || workflow.Stages[2].Model.Profile != "code" {
		t.Fatalf("legacy scope was not read as code: %+v", workflow)
	}
	other := openChangeFixture(t, s, "other.go")
	if err := s.DeleteChange(context.Background(), other.ID, changeIdentity(other)); err != nil {
		t.Fatalf("legacy journal blocked deleting another task: %v", err)
	}
	if kept, _ := os.ReadFile(path); string(kept) != legacy {
		t.Fatal("reading the legacy journal rewrote it")
	}
	request := workflowRequest(other)
	request.Models.Review = legacyCodeScope
	if err := request.Models.validate(); err == nil {
		t.Fatal("a new workflow request named the legacy scope")
	}
}
