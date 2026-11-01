package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

const groupedChangeResponse = `{"explanation":"Update Run and add documentation.","changes":[{"path":"main.go","content":"package main\nfunc Run() { println(\"updated\") }\n"},{"path":"docs/change.md","content":"A documented change.\n"}]}`

func TestChangeGroupedApplyUndoAndRestart(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	before, _ := os.ReadFile(filepath.Join(root, "main.go"))
	session := prepareChangeFixture(t, service, "main.go", "docs/change.md")
	request := ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}
	if _, err := service.ApplyChange(context.Background(), session.ID, request); err == nil {
		t.Fatal("Apply bypassed review")
	}
	approveChangeFixture(t, service, session)
	restarted, err := New(scopedTestConfig(server.URL), service.manager)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ApplyChange(context.Background(), session.ID, request); err == nil {
		t.Fatal("restart inherited approval")
	}
	request.Confirm = false
	if _, err := service.ApplyChange(context.Background(), session.ID, request); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	request.Confirm = true
	receipt, err := service.ApplyChange(context.Background(), session.ID, request)
	if err != nil || receipt.State != "applied" || !receipt.UndoAvailable || len(receipt.Warnings) != 0 {
		t.Fatalf("apply receipt: %+v, %v", receipt, err)
	}
	mode, _ := os.Stat(filepath.Join(root, "main.go"))
	if mode.Mode().Perm() != 0644 {
		t.Fatal("file permissions changed")
	}
	request.ProjectRevision = receipt.ProjectRevision
	undo, err := restarted.UndoChange(context.Background(), session.ID, request)
	if err != nil || undo.State != "undone" || undo.UndoAvailable {
		t.Fatalf("restart undo: %+v, %v", undo, err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if string(after) != string(before) {
		t.Fatal("Undo did not restore original content")
	}
	if _, err := os.Stat(filepath.Join(root, "docs/change.md")); !os.IsNotExist(err) {
		t.Fatal("Undo retained new file")
	}
}

func TestChangeApplySerializesApprovalChangesDuringWrites(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	entered, release := make(chan struct{}), make(chan struct{})
	applied := make(chan error, 1)
	go func() {
		_, err := service.applyChangeWithWriter(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}, func(path string, data []byte) error {
			if filepath.Base(path) == "main.go" {
				close(entered)
				<-release
			}
			return atomicWrite(path, data)
		})
		applied <- err
	}()
	<-entered
	reviewed := make(chan error, 1)
	go func() {
		_, err := service.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(session)})
		reviewed <- err
	}()
	select {
	case <-reviewed:
		close(release)
		t.Fatal("approval changed while reviewed files were being written")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
	<-reviewed
	result, err := service.ChangeSession(context.Background(), session.ID)
	if err != nil || result.State != "applied" {
		t.Fatalf("review lost applied state: %+v, %v", result, err)
	}
}

func TestChangeRecoveryRejectsOversizedMetadataBeforeWrites(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "main.go", "docs/change.md")
	session.Messages = []ChangeMessage{{Role: "user", Content: strings.Repeat("x", 2*1024*1024)}}
	if err := writeChangeJournal(root, &changeJournal{Version: 1, Session: *session, State: "prepared"}); err == nil {
		t.Fatal("oversized journal was accepted")
	}
	data, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if string(data) == session.Changes[0].Content {
		t.Fatal("metadata validation wrote source")
	}
}

func TestChangeApplyFailureRollsBackAndRetainsUnrelatedWrites(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(fmt.Sprint(unrelated), func(t *testing.T) {
			server := changeProvider(t, func() string { return groupedChangeResponse })
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			before, _ := os.ReadFile(filepath.Join(root, "main.go"))
			session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
			writes := 0
			writer := func(path string, data []byte) error {
				writes++
				if writes == 2 {
					if unrelated {
						_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// independent edit\n"), 0644)
					}
					return fmt.Errorf("injected write failure")
				}
				return atomicWrite(path, data)
			}
			receipt, err := service.applyChangeWithWriter(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}, writer)
			after, _ := os.ReadFile(filepath.Join(root, "main.go"))
			if unrelated {
				if err != nil || receipt.State != "recovery_required" || !strings.Contains(string(after), "independent edit") {
					t.Fatalf("partial write was hidden: %+v, %v", receipt, err)
				}
			} else if err == nil || receipt != nil || string(after) != string(before) {
				t.Fatalf("rollback: %+v, %v", receipt, err)
			}
		})
	}
}

func TestChangeInterruptedJournalRecoveryAndUndoFreshness(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "main.go", "docs/change.md")
	journal := &changeJournal{Version: 1, Session: *session, State: "prepared"}
	if err := writeChangeJournal(root, journal); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(root, "main.go"), []byte(session.Changes[0].Content)); err != nil {
		t.Fatal(err)
	}
	recovery, err := service.ChangeRecovery()
	if err != nil || recovery.State != "prepared" {
		t.Fatalf("recovery: %+v, %v", recovery, err)
	}
	if _, err := service.UndoChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
	fresh := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	receipt, err := service.ApplyChange(context.Background(), fresh.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(fresh), Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/change.md"), []byte("Independent documentation."), 0644); err != nil {
		t.Fatal(err)
	}
	identity := changeIdentity(fresh)
	identity.ProjectRevision = receipt.ProjectRevision
	if _, err := service.UndoChange(context.Background(), fresh.ID, ChangeApplyRequest{ChangeIdentity: identity, Confirm: true}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("Undo overwrote independent change: %v", err)
	}
}

func TestChangeApplyStaleSourceDoesNotWritePreparedJournal(t *testing.T) {
	server := changeProvider(t, func() string { return groupedChangeResponse })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := approveChangeFixture(t, service, prepareChangeFixture(t, service, "main.go", "docs/change.md"))
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() { println(\"external\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got: %v", err)
	}
	recovery, err := service.ChangeRecovery()
	if err != nil || recovery != nil {
		t.Fatalf("stale apply left recovery journal: %+v, %v", recovery, err)
	}
}
