package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nanaki-93/mini-orca/v2/internal/app/agentstate"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

func TestAnalysisRunCoverageNeverTurnsMissingEvidenceIntoZeroFindings(t *testing.T) {
	zero, three := 0, 3
	for _, test := range []struct {
		name     string
		status   AnalysisRunStatus
		coverage AnalysisRunCoverage
		count    *int
		valid    bool
	}{
		{"queued", AnalysisRunQueued, AnalysisRunCoverage{Total: 2, Pending: 2}, nil, true},
		{"running with one empty report", AnalysisRunRunning, AnalysisRunCoverage{Total: 2, Succeeded: 1, Running: 1}, &zero, true},
		{"completed empty", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 2, Succeeded: 2}, &zero, true},
		{"completed with findings", AnalysisRunCompleted, AnalysisRunCoverage{Total: 2, Succeeded: 2}, &three, true},
		{"partial evidence", AnalysisRunPartial, AnalysisRunCoverage{Total: 3, Succeeded: 1, Partial: 1, Failed: 1}, &three, true},
		{"failed", AnalysisRunFailed, AnalysisRunCoverage{Total: 1, Failed: 1}, nil, true},
		{"unavailable", AnalysisRunUnavailable, AnalysisRunCoverage{Total: 1, Unavailable: 1}, nil, true},
		{"no eligible work", AnalysisRunUnavailable, AnalysisRunCoverage{}, nil, true},
		{"paused", AnalysisRunPaused, AnalysisRunCoverage{Total: 2, Pending: 1, Succeeded: 1}, &zero, true},
		{"interrupted", AnalysisRunInterrupted, AnalysisRunCoverage{Total: 1, Pending: 1}, nil, true},
		{"stale retained evidence", AnalysisRunStale, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &three, true},
		{"failed is not empty", AnalysisRunFailed, AnalysisRunCoverage{Total: 1, Failed: 1}, &zero, false},
		{"no work is not completed empty", AnalysisRunCompletedEmpty, AnalysisRunCoverage{}, &zero, false},
		{"coverage mismatch", AnalysisRunRunning, AnalysisRunCoverage{Total: 1, Pending: 2}, nil, false},
		{"negative coverage", AnalysisRunRunning, AnalysisRunCoverage{Total: 1, Pending: 2, Failed: -1}, nil, false},
		{"incomplete completed", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 2, Succeeded: 1, Skipped: 1}, &zero, false},
		{"lost successful count", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 1, Succeeded: 1}, nil, false},
		{"mislabeled empty", AnalysisRunCompleted, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &zero, false},
		{"mislabeled findings", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &three, false},
		{"partial hides pending", AnalysisRunPartial, AnalysisRunCoverage{Total: 2, Succeeded: 1, Pending: 1}, &three, false},
		{"failed hides evidence", AnalysisRunFailed, AnalysisRunCoverage{Total: 2, Succeeded: 1, Failed: 1}, &three, false},
		{"failed hides pending", AnalysisRunFailed, AnalysisRunCoverage{Total: 2, Pending: 1, Failed: 1}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			progress := AnalysisSectionProgress{Category: project.FindingCategoryBugs, Status: test.status, Coverage: test.coverage, FindingCount: test.count}
			if err := progress.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid %t", err, test.valid)
			}
		})
	}
}

func TestAnalysisRunPreviewBindsTheWholeProjectWithoutTruncatingItToABatch(t *testing.T) {
	request := AnalysisPreviewRequest{ProjectID: "project", ProjectRevision: "revision", Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{BatchFiles: 100, BudgetSeconds: 900, MaxAttemptsPerStage: 2}}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Scope = "file"
	if err := request.Validate(); err == nil {
		t.Fatal("accepted file-scoped analysis")
	}
	request.Scope = AnalysisRunScopeProject
	for _, limits := range []AnalysisRunLimits{{}, {501, 900, 2}, {100, 3601, 2}, {100, 900, 5}} {
		request.Limits = limits
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted invalid bounds: %+v", limits)
		}
	}
	identity := AnalysisQueueIdentity{ProjectID: "project", ProjectRevision: "revision", PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	runIdentity := AnalysisRunIdentity{AnalysisQueueIdentity: identity, ID: "run", Generation: "generation"}
	if err := runIdentity.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Limits = AnalysisRunLimits{100, 900, 2}
	request.ResumeRun = &runIdentity
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.ProjectRevision = "replacement"
	if err := request.Validate(); err == nil {
		t.Fatal("accepted resume preview for a replacement revision")
	}
	preview := AnalysisRunPreview{SchemaVersion: AnalysisRunSchemaVersion, PreviewID: "preview", Identity: identity, Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{100, 900, 2}, Excluded: []AnalysisExcludedFile{}, Providers: []AnalysisProviderRequirement{{ID: "bug-provider", Stages: []AnalysisStage{AnalysisStageSemantic}, Model: EffectiveModel{Scope: "bug"}}}, ExpectedModelRequests: 600, MaxModelRequests: 1200}
	for index := 0; index < 600; index++ {
		preview.Files = append(preview.Files, AnalysisPlannedFile{AnalysisFileIdentity: AnalysisFileIdentity{Path: fmt.Sprintf("file-%03d.go", index), ContentHash: fmt.Sprintf("hash-%d", index), Language: "Go"}, SizeBytes: 40, Stages: []AnalysisStagePlan{{Stage: AnalysisStageSemantic, Eligible: true, ProviderID: "bug-provider", MaxModelRequests: 2}}})
	}
	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var restored AnalysisRunPreview
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Files) != 600 || restored.Limits.BatchFiles != 100 || restored.Files[599].ContentHash != "hash-599" {
		t.Fatalf("lost captured coverage: %+v", restored)
	}
	for _, forbidden := range []string{`"source"`, `"content"`, `"prompt"`, `"confirmations"`, `"api_key"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("preview contains %s", forbidden)
		}
	}
}

func TestAnalysisRunAdmissionAndControlRequireCompleteIdentityAndFreshIntent(t *testing.T) {
	queue := AnalysisQueueIdentity{ProjectID: "project", ProjectRevision: "revision", PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}
	identity := AnalysisRunIdentity{AnalysisQueueIdentity: queue, ID: "run", Generation: "generation"}
	start := AnalysisRunStartRequest{Identity: queue, PreviewID: "preview", Limits: AnalysisRunLimits{100, 900, 2}}
	if err := start.Validate(); err != nil {
		t.Fatal(err)
	}
	start.PreviewID = ""
	if err := start.Validate(); err == nil {
		t.Fatal("accepted start without preview")
	}
	confirmation := &AnalysisRunConfirmations{ProviderIDs: []string{}, SecurityReview: true}
	for _, test := range []struct {
		name          string
		action        AnalysisRunAction
		preview       string
		confirmations *AnalysisRunConfirmations
		valid         bool
	}{
		{"pause", AnalysisRunPause, "", nil, true},
		{"cancel", AnalysisRunCancel, "", nil, true},
		{"resume", AnalysisRunResume, "fresh-preview", confirmation, true},
		{"resume without preview", AnalysisRunResume, "", confirmation, false},
		{"resume without intent", AnalysisRunResume, "fresh-preview", nil, false},
		{"pause with intent", AnalysisRunPause, "", confirmation, false},
		{"cancel with preview", AnalysisRunCancel, "fresh-preview", nil, false},
		{"unknown action", "retry", "", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := AnalysisRunControlRequest{Identity: identity, Action: test.action, PreviewID: test.preview, Confirmations: test.confirmations}
			if err := request.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid %t", err, test.valid)
			}
		})
	}
	for _, guard := range []*string{&identity.ProjectID, &identity.ProjectRevision, &identity.PolicyFingerprint, &identity.ProviderFingerprint, &identity.QueueID, &identity.ID, &identity.Generation} {
		previous := *guard
		*guard = ""
		if err := (AnalysisRunControlRequest{Identity: identity, Action: AnalysisRunCancel}).Validate(); err == nil {
			t.Fatal("accepted control with an incomplete identity")
		}
		*guard = previous
	}
}

func TestAnalysisRunKeepsConsentTransientAndProducerEvidenceTyped(t *testing.T) {
	confirmation := AnalysisRunConfirmations{ProviderIDs: []string{"analyze-provider", "bug-provider"}, SecurityReview: true}
	start, err := json.Marshal(AnalysisRunStartRequest{Confirmations: confirmation})
	if err != nil || !strings.Contains(string(start), `"security_review":true`) {
		t.Fatalf("missing explicit Security intent: %s, %v", start, err)
	}
	persisted, err := json.Marshal(AnalysisRun{SchemaVersion: AnalysisRunSchemaVersion, Status: AnalysisRunInterrupted})
	if err != nil || strings.Contains(string(persisted), `"confirmations"`) || strings.Contains(string(persisted), `"provider_ids"`) {
		t.Fatalf("durable consent: %s, %v", persisted, err)
	}
	two := 2
	results := AnalysisSectionResults{
		Progress:     AnalysisSectionProgress{Category: project.FindingCategorySecurity, Status: AnalysisRunCompleted, Coverage: AnalysisRunCoverage{Total: 2, Succeeded: 2}, FindingCount: &two},
		Semantic:     []project.UnifiedFinding{{ID: "semantic", Category: project.FindingCategorySecurity, Source: project.FindingSourceAI, Confidence: project.FindingConfidenceSuggested, Status: project.FindingStatusDismissed}},
		Performance:  []project.PerformanceFileReport{},
		Security:     []project.SecurityFileReport{{Path: "main.go", Source: project.SecuritySourceDeterministic, Findings: []project.SecurityFinding{{ID: "rule", Triage: project.SecurityTriageAccepted, VerificationState: project.SecurityVerificationUnverified}}}},
		Unclassified: []project.UnifiedFinding{{ID: "old", Status: project.FindingStatusFixed}},
	}
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	var restored AnalysisSectionResults
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Semantic[0].Confidence != project.FindingConfidenceSuggested || restored.Semantic[0].Status != project.FindingStatusDismissed || restored.Security[0].Source != project.SecuritySourceDeterministic || restored.Security[0].Findings[0].Triage != project.SecurityTriageAccepted || restored.Unclassified[0].Category.Valid() || restored.Unclassified[0].Status != project.FindingStatusFixed {
		t.Fatalf("lost evidence identity: %+v", restored)
	}
	if restored.Progress.Category != project.FindingCategorySecurity || len(restored.Performance) != 0 {
		t.Fatal("Security read mixed result sections")
	}
	if got := AnalysisStageSemantic.Categories(); !reflect.DeepEqual(got, []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity}) {
		t.Fatalf("semantic consumers = %v", got)
	}
	if got := AnalysisStageSecurityRules.Categories(); !reflect.DeepEqual(got, []project.FindingCategory{project.FindingCategorySecurity}) {
		t.Fatalf("Security consumers = %v", got)
	}
}

func TestAnalysisRunOpenAPIContractsAreRegisteredAndResolvable(t *testing.T) {
	data, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, planned := document["x-unified-analysis-contract"]; planned {
		t.Fatal("registered operations remain in a planned extension")
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/projects/current/analysis/preview", "/api/projects/current/analysis/run", "/api/projects/current/analysis/run/control", "/api/projects/current/analysis/results"} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("missing live route: %s", path)
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	category := schemas["FindingCategory"].(map[string]any)["enum"]
	if !reflect.DeepEqual(category, []any{"bugs", "performance", "security"}) {
		t.Fatalf("category schema = %v", category)
	}
	properties := schemas["AnalysisRunPreview"].(map[string]any)["properties"].(map[string]any)
	if _, truncated := properties["files"].(map[string]any)["maxItems"]; truncated {
		t.Fatal("preview schema truncates project inventory")
	}
	var checkRefs func(any)
	checkRefs = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
				var node any = document
				for _, part := range parts {
					object, ok := node.(map[string]any)
					if !ok {
						t.Fatalf("unresolved reference %s", ref)
					}
					node = object[part]
				}
				if node == nil {
					t.Fatalf("unresolved reference %s", ref)
				}
			}
			for _, child := range value {
				checkRefs(child)
			}
		case []any:
			for _, child := range value {
				checkRefs(child)
			}
		}
	}
	checkRefs(paths)
	for name, schema := range schemas {
		if strings.HasPrefix(name, "Analysis") {
			checkRefs(schema)
		}
	}
}

func analysisRunPreviewFor(t *testing.T, s *Service, limits AnalysisRunLimits, resume *AnalysisRunIdentity) *AnalysisRunPreview {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAnalysisRun(context.Background(), AnalysisPreviewRequest{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Scope: AnalysisRunScopeProject, Limits: limits, ResumeRun: resume})
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func analysisStartFor(preview *AnalysisRunPreview) AnalysisRunStartRequest {
	confirmations := AnalysisRunConfirmations{SecurityReview: true}
	for _, provider := range preview.Providers {
		confirmations.ProviderIDs = append(confirmations.ProviderIDs, provider.ID)
	}
	return AnalysisRunStartRequest{Models: cloneAnalysisModels(preview.Models), IncludeFeatures: preview.Features != nil, Identity: preview.Identity, PreviewID: preview.PreviewID, Limits: preview.Limits, RetryStaleFailed: preview.RetryStaleFailed, StaleOnly: preview.StaleOnly, StalePath: preview.StalePath, RecoverIncomplete: preview.RecoverIncomplete, Refresh: preview.Refresh, ClearResults: preview.ClearResults, Confirmations: confirmations}
}

func waitAnalysisWindow(t *testing.T, s *Service) {
	t.Helper()
	c := s.analysisRun
	c.mu.Lock()
	done, cancel := c.done, c.cancel
	deadline := c.windowStart.Add(c.windowBudget + 5*time.Second)
	c.mu.Unlock()
	if done == nil {
		return
	}
	// Durable stages can legitimately outlast the generic single-signal helper.
	// Keep the domain deadline and allow bounded journal cleanup, then join the
	// canceled worker before TempDir cleanup can race its final writes.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	case <-t.Context().Done():
	}
	if cancel != nil {
		cancel()
	}
	cleanup := time.NewTimer(5 * time.Second)
	defer cleanup.Stop()
	select {
	case <-done:
	case <-cleanup.C:
		t.Fatal("analysis worker did not join after cancellation")
	}
	t.Fatal("analysis window exceeded its admitted deadline and journal cleanup allowance")
}

func TestAnalysisWindowWaitUsesAdmittedDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		s := &Service{analysisRun: &analysisRunController{done: done, windowStart: time.Now(), windowBudget: 10 * time.Second}}
		go func() { time.Sleep(4 * time.Second); close(done) }()
		waitAnalysisWindow(t, s)
	})
}

func completedAnalysisRun(t *testing.T, s *Service) *AnalysisRun {
	t.Helper()
	waitAnalysisWindow(t, s)
	run, err := s.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func emptyAnalysisReply(stage AnalysisStage) string {
	if stage == AnalysisStageSemantic {
		return validSemanticAnalysis
	}
	return `{"findings":[]}`
}

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
	if err := os.MkdirAll(filepath.Join(root, ".mini-orca"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mini-orca", "agents"), []byte("unavailable journal directory"), 0600); err != nil {
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
	for name, source := range map[string]string{"excluded.go": "package main", "large.go": strings.Repeat("x", maxSemanticAnalysisBytes+1), ".mini-orcaignore": "excluded.go\n"} {
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

func analysisBlockingServer(t *testing.T) (*httptest.Server, *atomic.Int32, chan struct{}, func()) {
	t.Helper()
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return
		}
		if reply, ok := docsRefreshReply(request); ok {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
			return
		}
		call := calls.Add(1)
		if call == 1 {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		reply := `{"findings":[]}`
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			reply = validSemanticAnalysis
		}
		if request.ResponseFormat != nil && request.ResponseFormat.JSONSchema != nil && request.ResponseFormat.JSONSchema.Name == "feature_suggestions" {
			reply = validFeatureResponse
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); server.Close() })
	return server, &calls, started, unblock
}

func TestAnalysisRunPauseWaitsForStageAndCancelInterruptsActiveRequest(t *testing.T) {
	for _, action := range []AnalysisRunAction{AnalysisRunPause, AnalysisRunCancel} {
		t.Run(string(action), func(t *testing.T) {
			server, calls, started, release := analysisBlockingServer(t)
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			run, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
			if err != nil {
				t.Fatal(err)
			}
			waitForTestSignal(t, started, "first analysis request")
			changed, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: action})
			if err != nil {
				t.Fatal(err)
			}
			if action == AnalysisRunPause {
				if changed.Status != AnalysisRunPausing {
					t.Fatalf("pause=%+v", changed)
				}
				release()
			}
			settled := completedAnalysisRun(t, s)
			expected := AnalysisRunPaused
			if action == AnalysisRunCancel {
				expected = AnalysisRunCanceled
			}
			if settled.Status != expected || calls.Load() != 1 || settled.Files[0].Stages[0].Attempts != 1 {
				t.Fatalf("settled=%+v calls=%d", settled, calls.Load())
			}
			if action == AnalysisRunPause && settled.Files[0].Stages[0].FindingCount == nil {
				t.Fatal("pause discarded current stage evidence")
			}
			if action == AnalysisRunCancel && settled.Files[0].Stages[0].FindingCount != nil {
				t.Fatal("canceled request published evidence")
			}
		})
	}
}

func TestAnalysisRunConcurrentStartsAndControlsHaveOneWriter(t *testing.T) {
	server, calls, started, release := analysisBlockingServer(t)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); results <- err }()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, errAnalysisRunBusy) {
			t.Fatal(err)
		}
	}
	waitForTestSignal(t, started, "single admitted request")
	if successes != 1 || calls.Load() != 1 {
		t.Fatalf("starts=%d calls=%d", successes, calls.Load())
	}
	run, err := s.currentTestAnalysisRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: run.Identity, Action: AnalysisRunPause})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	release()
	if got := completedAnalysisRun(t, s); got.Status != AnalysisRunPaused || calls.Load() != 1 {
		t.Fatalf("concurrent pause=%+v", got)
	}
}

func TestAnalysisRunFailedSavesStopRequestsAndRecoverWithoutResettingEvidence(t *testing.T) {
	for _, point := range []string{"admission", "reservation", "result", "completion"} {
		t.Run(point, func(t *testing.T) {
			server, calls := analysisResponseServer(t, emptyAnalysisReply)
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			var fault atomic.Bool
			fault.Store(true)
			s.writeAnalysisRun = func(name string, data []byte, mode os.FileMode) error {
				var run AnalysisRun
				if err := json.Unmarshal(data, &run); err != nil {
					return err
				}
				first := run.Files[0].Stages[0]
				fail := point == "admission" && run.Status == AnalysisRunQueued || point == "reservation" && first.Attempts == 1 || point == "result" && first.FindingCount != nil || point == "completion" && run.Status == AnalysisRunCompletedEmpty
				if fault.Load() && fail {
					return errors.New("private-path / secret-provider-value")
				}
				return storage.WriteFile(name, data, mode)
			}
			_, startErr := s.StartAnalysisRun(context.Background(), analysisStartFor(preview))
			if (startErr != nil) != (point == "admission") {
				t.Fatalf("start=%v", startErr)
			}
			waitAnalysisWindow(t, s)
			stopped, err := s.currentTestAnalysisRun(context.Background())
			if !errors.Is(err, errAnalysisRunPersistence) || stopped == nil || stopped.Status != AnalysisRunInterrupted {
				t.Fatalf("fault=%+v %v", stopped, err)
			}
			expected := int32(0)
			if point == "result" {
				expected = 1
			}
			if point == "completion" {
				expected = 3
			}
			if calls.Load() != expected || strings.Contains(stopped.Reason, "private") || strings.Contains(stopped.Reason, "secret-provider") {
				t.Fatalf("fault calls=%d progress=%+v", calls.Load(), stopped)
			}
			overview, err := s.ProjectOverview()
			if err != nil || overview.Run.Status != AnalysisRunInterrupted || overview.Coverage.Total != 1 || calls.Load() != expected {
				t.Fatalf("overview lost retained progress after save failure: %+v %v", overview, err)
			}
			if point == "result" {
				cached, err := s.CachedFileAnalysis("main.go")
				if err != nil || cached.Status != project.AnalysisStatusFresh {
					t.Fatal("completed evidence lost after failed progress save")
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			resume := analysisRunPreviewFor(t, s, preview.Limits, &stopped.Identity)
			after, _ := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
			if string(before) != string(after) || calls.Load() != expected {
				t.Fatal("preview retried fault or dispatched requests")
			}
			fault.Store(false)
			confirmations := analysisStartFor(resume).Confirmations
			if _, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: stopped.Identity, Action: AnalysisRunResume, PreviewID: resume.PreviewID, Confirmations: &confirmations}); err != nil {
				t.Fatal(err)
			}
			completed := completedAnalysisRun(t, s)
			if completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 {
				t.Fatalf("recovered=%+v calls=%d", completed, calls.Load())
			}
			if point == "reservation" && completed.Files[0].Stages[0].Attempts != 2 {
				t.Fatal("in-memory reservation allowance reset")
			}
		})
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

func TestAnalysisRunRejectsSourcePolicyProviderAndProjectReplacement(t *testing.T) {
	for _, change := range []string{"source", "policy", "provider", "reindex", "project"} {
		t.Run(change, func(t *testing.T) {
			server, calls, started, release := analysisBlockingServer(t)
			s, root := newSemanticAnalysisService(t, server.URL, 0)
			preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
			if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
				t.Fatal(err)
			}
			waitForTestSignal(t, started, "request before replacement")
			switch change {
			case "source":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed(){}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "policy":
				if err := os.WriteFile(filepath.Join(root, ".mini-orcaignore"), []byte("main.go\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "provider":
				// No provider metadata is read during the blocked HTTP response; release
				// synchronizes the subsequent validation with this simulated config change.
				s.analysisRun.mu.Lock()
				s.conf().runtimes.analyze.effective.ReasoningEffort = "replacement"
				s.analysisRun.mu.Unlock()
			case "reindex":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Changed(){}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			case "project":
				other := t.TempDir()
				if err := os.WriteFile(filepath.Join(other, "main.go"), []byte("package other\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.ActivateProject(other, &project.Analysis{Name: "replacement"}); err != nil {
					t.Fatal(err)
				}
			}
			release()
			waitAnalysisWindow(t, s)
			run, err := s.currentTestAnalysisRun(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if change == "project" {
				if run != nil {
					t.Fatal("old progress attached to replacement project")
				}
			} else if run == nil || run.Status != AnalysisRunStale {
				t.Fatalf("stale=%+v", run)
			}
			entries, err := os.ReadDir(filepath.Join(root, ".mini-orca/file-analysis"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 || calls.Load() != 1 {
				t.Fatalf("late evidence=%v calls=%d", entries, calls.Load())
			}
		})
	}
}

func TestAnalysisRunCancelRecoversAWriteFaultAfterSourceInvalidation(t *testing.T) {
	server, calls, started, _ := analysisBlockingServer(t)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	var fault atomic.Bool
	s.writeAnalysisRun = func(name string, data []byte, mode os.FileMode) error {
		if fault.Load() {
			return errors.New("unavailable metadata storage")
		}
		return storage.WriteFile(name, data, mode)
	}
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	waitForTestSignal(t, started, "request before invalidation failure")
	fault.Store(true)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	waitAnalysisWindow(t, s)
	stale, err := s.currentTestAnalysisRun(context.Background())
	if !errors.Is(err, errAnalysisRunPersistence) || stale == nil || stale.Status != AnalysisRunStale || calls.Load() != 1 {
		t.Fatalf("stale fault=%+v %v", stale, err)
	}
	fault.Store(false)
	recovered, err := s.ControlAnalysisRun(context.Background(), AnalysisRunControlRequest{Identity: stale.Identity, Action: AnalysisRunCancel})
	if err != nil || recovered.Status != AnalysisRunStale || calls.Load() != 1 {
		t.Fatalf("cancel recovery=%+v %v", recovered, err)
	}
	fresh := analysisRunPreviewFor(t, s, preview.Limits, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(fresh)); err != nil {
		t.Fatal(err)
	}
	completed := completedAnalysisRun(t, s)
	if completed.Identity.ID == stale.Identity.ID || completed.Status != AnalysisRunCompletedEmpty || calls.Load() != 4 {
		t.Fatalf("replacement=%+v calls=%d", completed, calls.Load())
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

func TestAnalysisRunSectionReadsPreserveCategoriesTriageAndDoNotDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		reply, docs := docsRefreshReply(request)
		if docs {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
			return
		}
		calls.Add(1)
		reply = `{"findings":[]}`
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			reply = `{"purpose":"Explains this file.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[{"category":"bugs","severity":"high","summary":"Error handling needs review."},{"category":"performance","severity":"low","summary":"Repeated work needs review."},{"category":"security","severity":"medium","summary":"Input trust needs review."}],"suggestions":[]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: reply}}}})
	}))
	defer server.Close()
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 2}, nil)
	if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
		t.Fatal(err)
	}
	run := completedAnalysisRun(t, s)
	findings, err := s.ListFindings(FindingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("semantic fixtures=%+v", findings)
	}
	if err := s.UpdateFindingStatus(run.Identity.ProjectRevision, findings[0].ID, project.FindingStatusDismissed); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity} {
		result, err := s.ReadAnalysisSection(context.Background(), run.Identity, category, "main.go")
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Semantic) != 1 || result.Semantic[0].Category != category || result.Progress.FindingCount == nil || *result.Progress.FindingCount != 1 {
			t.Fatalf("section=%+v", result)
		}
		if result.Semantic[0].ID == findings[0].ID && result.Semantic[0].Status != project.FindingStatusDismissed {
			t.Fatal("triage lost")
		}
		if category != project.FindingCategoryPerformance && len(result.Performance) != 0 || category != project.FindingCategorySecurity && len(result.Security) != 0 {
			t.Fatal("producer crossed sections")
		}
		if category == project.FindingCategorySecurity && len(result.Security) != 2 {
			t.Fatalf("lost typed Security reports: %+v", result)
		}
	}
	changed := run.Identity
	changed.Generation = "old"
	if _, err := s.ReadAnalysisSection(context.Background(), changed, project.FindingCategoryBugs, ""); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("old generation=%v", err)
	}
	if _, err := s.ReadAnalysisSection(context.Background(), run.Identity, project.FindingCategoryBugs, "missing.go"); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("uncaptured file=%v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, analysisRunRelativePath))
	if string(after) != string(metadata) || calls.Load() != 3 {
		t.Fatalf("read mutated progress or dispatched: calls=%d", calls.Load())
	}
}
