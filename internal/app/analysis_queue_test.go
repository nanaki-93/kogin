package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestAnalysisSelectionPersistsAndBindsAdmission(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	analysis, _ := s.manager.Analysis()
	ctx := context.Background()
	selection, err := s.ReadAnalysisSelection(ctx, analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	before := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if len(before.Files) == 0 {
		t.Fatal("missing fixture source")
	}
	ignored := before.Files[0].Path
	request := AnalysisSelectionRequest{analysis.ProjectID, analysis.ProjectRevision, selection.SelectionID, []string{ignored}}
	saved, err := s.SaveAnalysisSelection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if saved.SelectionID == selection.SelectionID {
		t.Fatal("selection identity did not change")
	}
	if _, err := s.SaveAnalysisSelection(ctx, request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if _, err := s.StartAnalysisRun(ctx, analysisStartFor(before)); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("stale admission: %v", err)
	}
	// A new service and manager restore selection from disk, independently of client state.
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, analysis); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(scopedTestConfig(server.URL), manager)
	if err != nil {
		t.Fatal(err)
	}
	restoredAnalysis, _ := manager.Analysis()
	restored, err := reopened.ReadAnalysisSelection(ctx, restoredAnalysis.ProjectID, restoredAnalysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.ExcludedPaths, []string{ignored}) {
		t.Fatalf("restored: %+v", restored)
	}
	after := analysisRunPreviewFor(t, reopened, before.Limits, nil)
	for _, file := range after.Files {
		if file.Path == ignored {
			t.Fatal("ignored file planned")
		}
	}
	found := false
	for _, file := range after.Excluded {
		if file.Path == ignored && file.Reason == analysisSelectionExclusion {
			found = true
		}
	}
	if !found || calls.Load() != 0 {
		t.Fatalf("exclusion missing or dispatched: %+v calls=%d", after.Excluded, calls.Load())
	}
	// Restore inclusion, including the explicit empty list, durably.
	_, err = reopened.SaveAnalysisSelection(ctx, AnalysisSelectionRequest{restored.ProjectID, restored.ProjectRevision, restored.SelectionID, []string{}})
	if err != nil {
		t.Fatal(err)
	}
	again := analysisRunPreviewFor(t, reopened, before.Limits, nil)
	if len(again.Files) != len(before.Files) {
		t.Fatal("reselected file absent")
	}
}

func TestAnalysisSelectionFailuresPreservePriorChoice(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	analysis, _ := s.manager.Analysis()
	ctx := context.Background()
	selection, err := s.ReadAnalysisSelection(ctx, analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{nil, {"../secret"}, {"."}, {"main.go", "main.go"}} {
		request := AnalysisSelectionRequest{analysis.ProjectID, analysis.ProjectRevision, selection.SelectionID, paths}
		if _, err := s.SaveAnalysisSelection(ctx, request); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
	request := AnalysisSelectionRequest{analysis.ProjectID, analysis.ProjectRevision, selection.SelectionID, []string{"absent.go"}}
	if _, err := s.SaveAnalysisSelection(ctx, request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("unknown path: %v", err)
	}
	request.ExcludedPaths = []string{}
	s.analysisRun.run = &AnalysisRun{Status: AnalysisRunPaused}
	if _, err := s.SaveAnalysisSelection(ctx, request); !errors.Is(err, errAnalysisRunBusy) {
		t.Fatalf("paused save: %v", err)
	}
	s.analysisRun.run = nil
	file := filepath.Join(root, analysisSelectionRelativePath)
	if err := os.MkdirAll(file, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveAnalysisSelection(root, []string{"main.go"}); err == nil {
		t.Fatal("write failure hidden")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{`, `{"schema_version":"2","excluded_paths":[]}`, `{"schema_version":"1","excluded_paths":["../outside"]}`, `{"schema_version":"1","excluded_paths":[]} {}`} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadAnalysisSelection(ctx, analysis.ProjectID, analysis.ProjectRevision); err == nil {
			t.Fatalf("accepted corrupt %s", data)
		}
		if _, err := s.PreviewAnalysisRun(ctx, AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: "project", Limits: AnalysisRunLimits{100, 900, 2}}); err == nil {
			t.Fatal("corrupt selection broadened analysis")
		}
	}
}

func TestAnalysisSelectionInventoryKeepsEligibilityAndNewFiles(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	for name, source := range map[string]string{".env": "TOKEN=private", "ignored.go": "package main", ".koginignore": "ignored.go\n", "asset.bin": "\x00\x01", "new.go": "package main"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if err := saveAnalysisSelection(root, []string{"absent.go", "main.go"}); err != nil {
		t.Fatal(err)
	}
	analysis, _ := s.manager.Analysis()
	selection, err := s.ReadAnalysisSelection(context.Background(), analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, file := range selection.Files {
		reasons[file.Path] = file.Reason
	}
	for _, name := range []string{".env", "ignored.go", "asset.bin"} {
		if reasons[name] == "" {
			t.Fatalf("unavailable file selectable: %s", name)
		}
	}
	if reason, ok := reasons["new.go"]; !ok || reason != "" {
		t.Fatalf("new file not eligible: %v", reasons)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	found := false
	for _, file := range preview.Files {
		if file.Path == "main.go" {
			t.Fatal("ignored file planned")
		}
		if file.Path == "new.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("new file not selected by default")
	}
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, selection.ExcludedPaths}); err != nil {
		t.Fatalf("absent exclusion lost: %v", err)
	}
}

func TestAnalysisSelectionRunExecutesOnlySelectedFilesAndRestoresProgress(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisServiceWithHelper(t, server.URL, 0)
	analysis, _ := s.manager.Analysis()
	selection, err := s.ReadAnalysisSelection(context.Background(), analysis.ProjectID, analysis.ProjectRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAnalysisSelection(context.Background(), AnalysisSelectionRequest{selection.ProjectID, selection.ProjectRevision, selection.SelectionID, []string{"helper.go"}}); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if len(run.Files) != 1 || run.Files[0].Path != "main.go" || calls.Load() != 3 {
		t.Fatalf("run files=%v calls=%d", run.Files, calls.Load())
	}
	restored, err := loadAnalysisRun(root)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Identity != run.Identity || restored.Plan.Excluded[0].Reason != analysisSelectionExclusion {
		t.Fatal("selection scope not retained")
	}
}
