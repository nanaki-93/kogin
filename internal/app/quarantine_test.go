package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

func TestNewerTaskFilesAreSkippedNotSetAside(t *testing.T) {
	server := changeProvider(t, func() string { return workflowImplementation })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "new.go")
	path := filepath.Join(root, ".kogin", "changes", session.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 2`, 1)
	if newer == string(data) {
		t.Fatal("fixture schema version not found")
	}
	if err := os.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := service.ChangeHistory(context.Background())
	if err != nil || len(history) != 0 {
		t.Fatalf("newer task listed or failed the list: %+v %v", history, err)
	}
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 0 || len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0].Remedy, "Upgrade Kogin") {
		t.Fatalf("report: %+v %v", report, err)
	}
	if kept, _ := os.ReadFile(path); string(kept) != newer {
		t.Fatal("newer task file was moved")
	}
}

func TestUnreadableAnalysisProgressIsSetAsideSoAnalysisCanStart(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	path := filepath.Join(root, analysisRunRelativePath)
	broken := `{"schema_version":"1","written_by":"an older Kogin"}`
	if err := storage.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	overview, err := service.ProjectOverview()
	if err != nil || overview.Run != nil {
		t.Fatalf("unreadable progress blocked the overview: %+v %v", overview, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable progress was not moved: %v", err)
	}
	analysisRunPreviewFor(t, service, AnalysisRunLimits{1, 30, 1}, nil)
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 1 || len(report.Skipped) != 0 {
		t.Fatalf("report: %+v %v", report, err)
	}
	record := report.Files[0]
	if record.Kind != "analysis_run" || record.File != analysisRunRelativePath || !strings.HasPrefix(record.QuarantinedAs, ".kogin/analysis/run.quarantined-") {
		t.Fatalf("record: %+v", record)
	}
	if kept, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(record.QuarantinedAs))); err != nil || string(kept) != broken {
		t.Fatalf("set-aside bytes: %q %v", kept, err)
	}
}

func TestNewerAnalysisProgressIsKeptAndNamed(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	path := filepath.Join(root, analysisRunRelativePath)
	newer := `{"schema_version":"2","status":"running"}`
	if err := storage.WriteFile(path, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if overview, err := service.ProjectOverview(); err != nil || overview.Run != nil {
		t.Fatalf("newer progress blocked the overview: %+v %v", overview, err)
	}
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	request := AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{1, 30, 1}}
	if _, err := service.PreviewAnalysisRun(context.Background(), request); !errors.Is(err, ErrNewerAnalysisRun) {
		t.Fatalf("preview over newer progress: %v", err)
	}
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 0 || len(report.Skipped) != 1 || report.Skipped[0].Kind != "analysis_run" {
		t.Fatalf("report: %+v %v", report, err)
	}
	if kept, _ := os.ReadFile(path); string(kept) != newer {
		t.Fatal("newer progress was moved or changed")
	}
}

func deleteRequest(t *testing.T, service *Service, quarantinedAs string) QuarantineDeleteRequest {
	t.Helper()
	index, err := service.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	return QuarantineDeleteRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, QuarantinedAs: quarantinedAs}
}

func TestSetAsideAnalysisProgressIsDeletedWithItsRecord(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := storage.WriteFile(filepath.Join(root, analysisRunRelativePath), []byte(`{"schema_version":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProjectOverview(); err != nil {
		t.Fatal(err)
	}
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 1 {
		t.Fatalf("report: %+v %v", report, err)
	}
	setAside := report.Files[0].QuarantinedAs
	stale := deleteRequest(t, service, setAside)
	stale.ProjectRevision = "stale"
	if _, err := service.DeleteQuarantined(context.Background(), stale); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale identity: %v", err)
	}
	live := filepath.Join(root, ".kogin", "index.json")
	for _, other := range []string{".kogin/index.json", ".kogin/analysis/run.json", "../outside.quarantined-1.json", ".kogin/agents/x/journal.quarantined-1.json"} {
		if _, err := service.DeleteQuarantined(context.Background(), deleteRequest(t, service, other)); !errors.Is(err, ErrQuarantineNotListed) {
			t.Fatalf("%s: %v", other, err)
		}
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("a file that was not set aside was touched: %v", err)
	}
	left, err := service.DeleteQuarantined(context.Background(), deleteRequest(t, service, setAside))
	if err != nil || len(left.Files) != 0 {
		t.Fatalf("delete: %+v %v", left, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(setAside))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-aside file kept: %v", err)
	}
	if records, err := setAsideRecords(root); err != nil || len(records) != 0 {
		t.Fatalf("records: %+v %v", records, err)
	}
	if _, err := service.DeleteQuarantined(context.Background(), deleteRequest(t, service, setAside)); !errors.Is(err, ErrQuarantineNotListed) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestSetAsideRecordsFromMiniOrcaNameWhereTheFilesAreNow(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	kept := ".kogin/analysis/run.quarantined-20261028T130610.402215000Z.json"
	if err := storage.WriteFile(filepath.Join(root, filepath.FromSlash(kept)), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	records := `[{"kind":"analysis_run","file":".mini-orca/analysis/run.json","quarantined_as":".mini-orca/analysis/run.quarantined-20261028T130610.402215000Z.json","reason":"saved analysis progress is unreadable","remedy":"Mini-Orca no longer reads the set-aside file.","at":"2026-10-28T13:06:10Z"},` +
		`{"kind":"task","file":".mini-orca/changes/change-1.json","quarantined_as":".mini-orca/changes/change-1.quarantined-20261027T000000.000000000Z.json","reason":"gone","remedy":"","at":"2026-10-27T00:00:00Z"}]`
	if err := storage.WriteFile(filepath.Join(root, setAsideLogPath), []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := service.Quarantine(context.Background())
	if err != nil || len(report.Files) != 1 {
		t.Fatalf("a record without its file is listed, or the report failed: %+v %v", report, err)
	}
	if file := report.Files[0]; file.File != analysisRunRelativePath || file.QuarantinedAs != kept || file.Remedy != remedySetAside {
		t.Fatalf("record: %+v", file)
	}
	if left, err := service.DeleteQuarantined(context.Background(), deleteRequest(t, service, kept)); err != nil || len(left.Files) != 0 {
		t.Fatalf("delete: %+v %v", left, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(kept))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-aside file kept: %v", err)
	}
}
