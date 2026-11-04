package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

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
