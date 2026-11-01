package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

func TestRecoveryJournalErrorsNameTheFileAndRemedy(t *testing.T) {
	service, root := newSemanticAnalysisService(t, "http://127.0.0.1:1", 0)
	path := filepath.Join(root, ".kasuri", "changes", "last-mutation.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for content, want := range map[string]string{
		"broken":        "is unreadable",
		`{"version":7}`: "written by a newer Kasuri",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := service.ChangeRecovery()
		if err == nil || !strings.Contains(err.Error(), ".kasuri/changes/last-mutation.json") || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", content, err)
		}
		if kept, _ := os.ReadFile(path); string(kept) != content {
			t.Fatal("recovery journal was moved or changed")
		}
	}
}

func TestNewerTaskFilesAreSkippedNotSetAside(t *testing.T) {
	server := changeProvider(t, func() string { return workflowImplementation })
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "new.go")
	path := filepath.Join(root, ".kasuri", "changes", session.ID+".json")
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
	if err != nil || len(report.Files) != 0 || len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0].Remedy, "Upgrade Kasuri") {
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
	broken := `{"schema_version":"1","written_by":"an older Kasuri"}`
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
	if record.Kind != "analysis_run" || record.File != analysisRunRelativePath || !strings.HasPrefix(record.QuarantinedAs, ".kasuri/analysis/run.quarantined-") {
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
