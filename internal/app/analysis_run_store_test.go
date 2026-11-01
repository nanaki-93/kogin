package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

func TestAnalysisRunStoreRestoresInterruptedProgressWithoutConsentOrRequests(t *testing.T) {
	server, calls, started, release := analysisBlockingServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	s.conf().runtimes.bug.effective.RemoteProvider = true
	s.conf().runtimes.analyze.effective.RemoteProvider = true
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	startedRun, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
	if err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "request before restart fixture")
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: startedRun.Identity, Action: AnalysisRunPause}); err != nil {
		t.Fatal(err)
	}
	release()
	paused := completedAnalysisRun(t, s)
	// Simulate a process loss after reserving the next stage, before its result.
	paused.Status = AnalysisRunRunning
	paused.Files[0].Stages[1].Status = AnalysisStageRunning
	paused.Files[0].Stages[1].Attempts = 1
	refreshAnalysisSections(paused)
	data, err := json.Marshal(paused)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := New(scopedTestConfig(server.URL), s.manager)
	if err != nil {
		t.Fatal(err)
	}
	restored.conf().runtimes = s.conf().runtimes
	overview, err := restored.ProjectOverview()
	if err != nil {
		t.Fatal(err)
	}
	run := overview.Run
	if run == nil || run.Status != AnalysisRunInterrupted || run.Files[0].Stages[1].Attempts != 1 || run.Files[0].Stages[1].Status != AnalysisStageInterrupted || run.Files[0].Stages[0].FindingCount == nil || calls.Load() != 1 {
		t.Fatalf("restore=%+v calls=%d", run, calls.Load())
	}
	if restored.analysisRun.done != nil || restored.analysisRun.confirmations.SecurityReview || len(restored.analysisRun.confirmations.ProviderIDs) != 0 {
		t.Fatal("restore retained dispatch authority")
	}
	resume := analysisRunPreviewFor(t, restored, preview.Limits, &run.Identity)
	if resume.ExpectedModelRequests != 2 || resume.MaxModelRequests != 3 {
		t.Fatalf("resume estimate=%+v", resume)
	}
	for _, confirmations := range []AnalysisRunConfirmations{{}, {SecurityReview: true}, {ProviderIDs: analysisStartFor(resume).Confirmations.ProviderIDs}} {
		if _, err := restored.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err == nil || calls.Load() != 1 {
			t.Fatal("resume accepted missing fresh consent")
		}
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := restored.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, restored)
	if completed.Status != AnalysisRunCompletedEmpty || completed.Files[0].Stages[1].Attempts != 2 || calls.Load() != 3 {
		t.Fatalf("restored completion=%+v calls=%d", completed, calls.Load())
	}
	info, err := os.Stat(filepath.Join(root, analysisRunRelativePath))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("progress permissions=%v %v", info, err)
	}
}

func TestAnalysisRunStoreRejectsCorruptionAndKeepsTheOriginalBytes(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	valid := newAnalysisRun(*preview)
	for _, test := range []struct {
		name   string
		mutate func(*AnalysisRun)
	}{
		{"queue", func(run *AnalysisRun) { run.Identity.QueueID = "changed" }},
		{"attempts", func(run *AnalysisRun) { run.Files[0].Stages[0].Attempts = 3 }},
		{"stage", func(run *AnalysisRun) { run.Files[0].Stages[0].Stage = AnalysisStageSecurityAI }},
		{"status", func(run *AnalysisRun) { run.Files[0].Stages[0].Status = "invented" }},
		{"coverage", func(run *AnalysisRun) { run.Sections[0].Coverage.Succeeded++ }},
		{"source reason", func(run *AnalysisRun) { run.Files[0].Stages[0].Reason = "package main; secret input" }},
		{"traversal", func(run *AnalysisRun) { run.Files[0].Path = "../outside.go" }},
		{"empty claims", func(run *AnalysisRun) { run.Status = AnalysisRunCompletedEmpty }},
		{"unknown schema", func(run *AnalysisRun) { run.SchemaVersion = "future" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := cloneAnalysisRun(valid)
			test.mutate(run)
			data, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, analysisRunRelativePath)
			if err := storage.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAnalysisRun(root); !errors.Is(err, errAnalysisRunCorrupt) {
				t.Fatalf("corruption=%v", err)
			}
			after, err := os.ReadFile(name)
			if err != nil || string(after) != string(data) {
				t.Fatal("corrupt progress was discarded or rewritten")
			}
		})
	}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"schema_version":`, string(data) + "\n{}", strings.Replace(string(data), `"schema_version":"1"`, `"confirmations":{"security_review":true},"schema_version":"1"`, 1)} {
		if err := storage.WriteFile(filepath.Join(root, analysisRunRelativePath), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadAnalysisRun(root); !errors.Is(err, errAnalysisRunCorrupt) {
			t.Fatal("invalid or consent-bearing metadata accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("metadata validation contacted a provider")
	}
}

func TestAnalysisRunStoreCloneDoesNotExposeMutableControllerState(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	run.Files[0].Stages[0].Attempts = 99
	*run.Files[0].Stages[0].FindingCount = 99
	run.Plan.Files[0].Stages[0].MaxModelRequests = 99
	run.Plan.Providers[0].Stages[0] = AnalysisStageSecurityRules
	*run.Sections[0].FindingCount = 99
	current, err := s.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Files[0].Stages[0].Attempts != 1 || *current.Files[0].Stages[0].FindingCount != 0 || current.Plan.Files[0].Stages[0].MaxModelRequests != 2 || current.Plan.Providers[0].Stages[0] != AnalysisStageSemantic || *current.Sections[0].FindingCount != 0 {
		t.Fatal("response mutation reached the run owner")
	}
}

func TestAnalysisRunStoreResumeReusesPublishedEvidenceWithNoAttemptsRemaining(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
	// Preserve a successfully published semantic report while losing only its
	// progress update, as a crash or disk failure at that boundary can do.
	s.writeAnalysisRun = func(name string, data []byte, mode os.FileMode) error {
		var run AnalysisRun
		if err := json.Unmarshal(data, &run); err != nil {
			return err
		}
		if run.Files[0].Stages[0].FindingCount != nil {
			return errors.New("progress update failed after report publication")
		}
		return storage.WriteFile(name, data, mode)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitAnalysisWindow(t, s)
	restored, err := New(scopedTestConfig(server.URL), s.manager)
	if err != nil {
		t.Fatal(err)
	}
	restored.conf().runtimes = s.conf().runtimes
	interrupted, err := restored.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Files[0].Stages[0].Attempts != 1 || calls.Load() != 1 {
		t.Fatal("fixture did not retain the spent attempt")
	}
	resume := analysisRunPreviewFor(t, restored, preview.Limits, &interrupted.Identity)
	if !resume.Files[0].Stages[0].Cached || resume.Files[0].Stages[0].MaxModelRequests != 0 || resume.ExpectedModelRequests != 2 {
		t.Fatalf("resume cache estimate=%+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := restored.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: interrupted.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, restored)
	stage := completed.Files[0].Stages[0]
	if completed.Status != AnalysisRunCompletedEmpty || stage.Status != AnalysisStageCompletedEmpty || !stage.Cached || stage.Attempts != 1 || calls.Load() != 3 {
		t.Fatalf("published evidence with exhausted transport allowance: run=%s stage=%s cached=%t attempts=%d calls=%d; want completed_empty, cached, one retained attempt and three total calls", completed.Status, stage.Status, stage.Cached, stage.Attempts, calls.Load())
	}
	if _, err := os.Stat(filepath.Join(root, analysisRunRelativePath)); err != nil {
		t.Fatal(err)
	}
}

func TestAnalysisRunDetachmentRetainsFailureOnlyForCapturedProject(t *testing.T) {
	server, _, started, release := analysisBlockingServer(t)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	root := s.manager.Root()
	original, _ := s.manager.Analysis()
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "request before detachment")
	s.analysisRun.mu.Lock()
	s.writeAnalysisRun = func(path string, data []byte, mode os.FileMode) error {
		var run AnalysisRun
		_ = json.Unmarshal(data, &run)
		if path == filepath.Join(root, analysisRunRelativePath) && run.Status == AnalysisRunInterrupted {
			return errors.New("private interruption write failure")
		}
		return storage.WriteFile(path, data, mode)
	}
	s.analysisRun.mu.Unlock()
	next := t.TempDir()
	if err := os.WriteFile(filepath.Join(next, "next.go"), []byte("package next\nfunc Next() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateProject(next, &project.Analysis{Name: "next", Path: next}); err != nil {
		t.Fatal(err)
	}
	release()
	waitAnalysisWindow(t, s)
	if run, err := s.currentTestAnalysisRun(context.Background()); err != nil || run != nil {
		t.Fatalf("new project inherited fault: %+v %v", run, err)
	}
	if err := s.ActivateProject(root, original); err != nil {
		t.Fatal(err)
	}
	retained, err := s.currentTestAnalysisRun(context.Background())
	if err != errAnalysisRunPersistence || retained == nil || retained.Status != AnalysisRunInterrupted {
		t.Fatalf("lost detached fault: %+v %v", retained, err)
	}
	s.writeAnalysisRun = nil
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: retained.Identity, Action: AnalysisRunCancel}); err != nil {
		t.Fatal(err)
	}
}

func TestAnalysisRunSkipsIneligibleStagesAndRestoresLegacyCoverage(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/fixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 5 {
		t.Fatalf("run=%+v calls=%d", completed, calls.Load())
	}
	for _, section := range completed.Sections {
		if section.Coverage.Unavailable != 0 || section.Status != AnalysisRunCompletedEmpty {
			t.Fatalf("ineligible stages affected coverage: %+v", section)
		}
	}
	for i, file := range completed.Files {
		for j := range file.Stages {
			if !completed.Plan.Files[i].Stages[j].Eligible {
				if file.Stages[j].Status != AnalysisStageSkipped {
					t.Fatalf("ineligible stage=%+v", file.Stages[j])
				}
				completed.Files[i].Stages[j].Status = AnalysisStageUnavailable
			}
		}
	}
	// Original schema-1 files counted these unavailable stages in the denominator.
	refreshAnalysisSectionsWithEligibility(completed, true)
	completed.Status = analysisFinishedStatus(completed)
	refreshAnalysisSectionsWithEligibility(completed, true)
	if completed.Status != AnalysisRunPartial {
		t.Fatal("invalid legacy fixture")
	}
	writeLegacy := func(run *AnalysisRun) {
		t.Helper()
		data, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, analysisRunRelativePath), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacy(completed)
	restored, err := loadAnalysisRun(root)
	if err != nil || restored.Status != AnalysisRunCompletedEmpty {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	if err := validateStoredAnalysisRun(restored); err != nil {
		t.Fatalf("normalized run invalid: %v", err)
	}
	// Compatibility must not discard corrupt aggregate counts while normalizing.
	completed.Sections[0].Coverage.Total++
	writeLegacy(completed)
	if _, err := loadAnalysisRun(root); !errors.Is(err, errAnalysisRunCorrupt) {
		t.Fatalf("corrupt legacy aggregate accepted: %v", err)
	}
	if calls.Load() != 5 {
		t.Fatal("restore dispatched model requests")
	}
}

func TestAnalysisRecoverStoredPlanConstraintsAndLegacyEncoding(t *testing.T) {
	server, _ := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	limits := AnalysisRunLimits{100, 30, 2}
	name := filepath.Join(root, analysisRunRelativePath)
	write := func(run *AnalysisRun) {
		t.Helper()
		data, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	legacyPreview := analysisRunPreviewFor(t, s, limits, nil)
	valid := newAnalysisRun(*recoveryPreviewFor(t, s, limits, nil))
	write(valid)
	if restored, err := loadAnalysisRun(root); err != nil || !restored.Plan.RecoverIncomplete {
		t.Fatalf("valid recovery run=%+v %v", restored, err)
	}
	for label, mutate := range map[string]func(*AnalysisRunPreview){
		"refresh":  func(plan *AnalysisRunPreview) { plan.Refresh = true },
		"retry":    func(plan *AnalysisRunPreview) { plan.RetryStaleFailed = true },
		"features": func(plan *AnalysisRunPreview) { plan.Features = &AnalysisFeaturePlan{} },
	} {
		run := cloneAnalysisRun(valid)
		mutate(&run.Plan)
		// Rebind the queue so only the recovery identity rule can reject the run.
		queue, err := analysisQueueFingerprint(&run.Plan)
		if err != nil {
			t.Fatal(err)
		}
		run.Plan.Identity.QueueID = queue
		run.Identity.AnalysisQueueIdentity = run.Plan.Identity
		if validStoredAnalysisIdentity(run) {
			t.Fatalf("%s recovery plan accepted", label)
		}
		write(run)
		if _, err := loadAnalysisRun(root); !errors.Is(err, errAnalysisRunCorrupt) {
			t.Fatalf("%s recovery plan restored: %v", label, err)
		}
	}
	// Plans without recovery encode exactly as before, keeping stored queue fingerprints.
	legacy := newAnalysisRun(*legacyPreview)
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "recover_incomplete") {
		t.Fatal("non-recovery plan encodes the recovery option")
	}
	write(legacy)
	if restored, err := loadAnalysisRun(root); err != nil || restored.Identity.QueueID != legacy.Identity.QueueID {
		t.Fatalf("legacy run=%+v %v", restored, err)
	}
}
