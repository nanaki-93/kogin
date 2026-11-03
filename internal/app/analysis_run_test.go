package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestAnalysisRunStagesShareAgentBudget(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	s.conf().agentRuntime.Limits.ModelCalls = 1
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if calls.Load() != 1 || run.Status == AnalysisRunCompleted || run.Status == AnalysisRunCompletedEmpty || !strings.Contains(run.Reason, "allowance") {
		t.Fatalf("analysis stages refreshed budget: calls=%d status=%s", calls.Load(), run.Status)
	}
}

func TestAnalysisJournalAdmissionFailureLeavesInterruptedRun(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kogin", "agents"), []byte("unavailable journal directory"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
	if !errors.Is(err, agentstate.ErrPersistence) || run == nil || run.Status != AnalysisRunInterrupted || calls.Load() != 0 {
		t.Fatalf("run=%+v error=%v calls=%d", run, err, calls.Load())
	}
	s.analysisRun.mu.Lock()
	active := s.analysisRun.done != nil
	s.analysisRun.mu.Unlock()
	stored, err := s.currentTestAnalysisRun(context.Background())
	if err != nil || active || stored.Status != AnalysisRunInterrupted {
		t.Fatalf("failed admission retained a phantom worker: %+v %v", stored, err)
	}
}

func TestAnalysisRunCapturesActualInventoryBeyond500AndRejectsChangedPreview(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	for i := 0; i < 500; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("source-%03d.go", i)), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, source := range map[string]string{"excluded.go": "package main", "large.go": strings.Repeat("x", maxSemanticAnalysisBytes+1), ".koginignore": "excluded.go\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 900, 4}, nil)
	if len(preview.Files) != 502 || len(preview.Excluded) != 2 || preview.ExpectedModelRequests != 1505 || preview.MaxModelRequests != 3010 || calls.Load() != 0 {
		t.Fatalf("inventory files=%d excluded=%d estimates=%d/%d calls=%d", len(preview.Files), len(preview.Excluded), preview.ExpectedModelRequests, preview.MaxModelRequests, calls.Load())
	}
	if preview.Excluded[0].Path != "excluded.go" || preview.Excluded[1].Path != "large.go" {
		t.Fatalf("excluded=%+v", preview.Excluded)
	}
	for i := 1; i < len(preview.Files); i++ {
		if preview.Files[i-1].Path >= preview.Files[i].Path {
			t.Fatal("queue is not deterministic")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); !errors.Is(err, project.ErrRevisionConflict) || calls.Load() != 0 {
		t.Fatalf("stale admission=%v, calls=%d", err, calls.Load())
	}
}

func TestAnalysisRunBatchesRequireExplicitContinuationAndReuseCaches(t *testing.T) {
	server, calls := analysisResponseServer(t, emptyAnalysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "z.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	limits := AnalysisRunLimits{1, 30, 2}
	preview := analysisRunPreviewFor(t, s, limits, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || paused.WindowFilesCompleted != 1 || calls.Load() != 3 || len(paused.Files) != 2 || !analysisFileFinished(paused.Files[0]) || analysisFileFinished(paused.Files[1]) {
		t.Fatalf("batch=%+v calls=%d", paused, calls.Load())
	}
	resume := analysisRunPreviewFor(t, s, limits, &paused.Identity)
	if resume.Identity != preview.Identity || resume.ExpectedModelRequests != 3 {
		t.Fatalf("resume=%+v", resume)
	}
	confirmations := analysisStartFor(resume).Confirmations
	next, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations})
	if err != nil {
		t.Fatal(err)
	}
	if next.Identity.ID != paused.Identity.ID || next.Identity.Generation == paused.Identity.Generation {
		t.Fatal("resume lost run identity or reused generation")
	}
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunCancel}); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatal("old generation controlled resumed run")
	}
	run := completedAnalysisRun(t, s)
	if run.Status != AnalysisRunCompletedEmpty || calls.Load() != 6 {
		t.Fatalf("completed=%+v calls=%d", run, calls.Load())
	}
	cached := analysisRunPreviewFor(t, s, limits, nil)
	if cached.Identity != preview.Identity || cached.ExpectedModelRequests != 0 || cached.MaxModelRequests != 0 {
		t.Fatalf("cached=%+v", cached)
	}
	data, err := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"confirmations", "provider_ids", "package main", "fmt.Println", "prompt\":"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("source or consent persisted: %s", secret)
		}
	}
}

func TestAnalysisRunTimeBudgetAndRetryAllowanceRemainCumulative(t *testing.T) {
	server, calls, started, _ := analysisBlockingServer(t)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	// The window must outlast the resumed requests even under the race
	// detector; the first window ends because its request never answers.
	const budget = 3
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, budget, 1}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "time-bounded request")
	paused := completedAnalysisRun(t, s)
	if paused.Status != AnalysisRunPaused || paused.ElapsedSeconds != budget || paused.Files[0].Stages[0].Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("timeout=%+v calls=%d", paused, calls.Load())
	}
	resume := analysisRunPreviewFor(t, s, preview.Limits, &paused.Identity)
	confirmations := analysisStartFor(resume).Confirmations
	if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: paused.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Status != AnalysisRunPartial || completed.Files[0].Stages[0].Attempts != 1 || completed.Files[0].Stages[0].Status != AnalysisStageFailed || calls.Load() != 3 {
		t.Fatalf("bounded resume=%+v calls=%d", completed, calls.Load())
	}
}

func TestAnalysisRunKeepsCategoryCountsAndPartialEvidenceAcrossFailedStages(t *testing.T) {
	server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
		if stage == AnalysisStagePerformance {
			return "invalid response"
		}
		return analysisReply(stage)
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	if run.Status != AnalysisRunPartial || calls.Load() != 3 {
		t.Fatalf("partial run=%+v calls=%d", run, calls.Load())
	}
	for _, section := range run.Sections {
		expected := 1
		status := AnalysisRunCompleted
		if section.Category == project.FindingCategoryPerformance {
			expected = 0
			status = AnalysisRunPartial
		}
		if section.Category == project.FindingCategorySecurity {
			expected = 2
		}
		if section.FindingCount == nil || *section.FindingCount != expected || section.Status != status {
			t.Fatalf("section=%+v", section)
		}
	}
	stored, err := loadAnalysisRun(s.manager.Root())
	if err != nil || !reflect.DeepEqual(stored.Sections, run.Sections) {
		t.Fatalf("stored sections=%+v %v", stored, err)
	}
	overview, err := s.ProjectOverview()
	if err != nil || overview.Run.Identity.ProjectID != overview.ProjectID || overview.Run.Identity.ProjectRevision != overview.ProjectRevision || !reflect.DeepEqual(overview.Run.Sections, run.Sections) {
		t.Fatalf("overview=%+v %v", overview, err)
	}
}
