package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

var analysisTestStages = []AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityRules, AnalysisStageSecurityAI}

func analysisStageRequestFor(t *testing.T, s *Service, path string, stage AnalysisStage) analysisFileStageRequest {
	t.Helper()
	analysis, err := s.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.manager.IndexedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return analysisFileStageRequest{Run: AnalysisRunIdentity{AnalysisQueueIdentity: AnalysisQueueIdentity{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}, ID: "run", Generation: "generation"}, File: AnalysisFileIdentity{Path: file.Path, ContentHash: file.ContentHash, Language: file.Language}, Stage: stage, RemainingAttempts: 2, SecurityReview: true}
}

func analysisStageAuthorityFor(t *testing.T, request analysisFileStageRequest) analysisFileStageAuthority {
	t.Helper()
	var mu sync.Mutex
	check := func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
		if run != request.Run || file != request.File || stage != request.Stage {
			return project.ErrRevisionConflict
		}
		return nil
	}
	return analysisFileStageAuthority{
		Check: func(ctx context.Context, run AnalysisRunIdentity) error {
			mu.Lock()
			defer mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			if run != request.Run {
				return project.ErrRevisionConflict
			}
			return nil
		},
		BeforeAttempt: func(ctx context.Context, run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage) error {
			mu.Lock()
			defer mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return check(run, file, stage)
		},
		Publish: func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
			mu.Lock()
			defer mu.Unlock()
			if err := check(run, file, stage); err != nil {
				return err
			}
			return write()
		},
	}
}

func analysisReply(stage AnalysisStage) string {
	switch stage {
	case AnalysisStageSemantic:
		return strings.Replace(validSemanticAnalysis, `"risks":[]`, `"risks":[{"category":"bugs","severity":"low","summary":"A conditional correctness concern."},{"category":"security","severity":"high","summary":"A conditional trust-boundary concern."}]`, 1)
	case AnalysisStagePerformance:
		return validPerformanceReview
	default:
		return validSecurityReview
	}
}

func analysisResponseServer(t *testing.T, reply func(AnalysisStage) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	return analysisModelResponseServer(t, "", reply)
}

func analysisModelResponseServer(t *testing.T, model string, reply func(AnalysisStage) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) == 0 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		// The docs refresh at a run's end is answered here but not counted:
		// the tests count the analysis stages' requests.
		if reply, ok := docsRefreshReply(request); ok {
			_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: model, Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: reply}, FinishReason: "stop"}}})
			return
		}
		stage := AnalysisStageSecurityAI
		if strings.HasPrefix(request.Messages[0].Content, "You summarize") {
			stage = AnalysisStageSemantic
		}
		if strings.HasPrefix(request.Messages[0].Content, "Review exactly one supplied") {
			stage = AnalysisStagePerformance
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Model: model, Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Role: "assistant", Content: reply(stage)}, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestAnalysisFileStagesReuseCachesAndKeepProducerEvidenceDistinct(t *testing.T) {
	server, calls := analysisResponseServer(t, analysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	before, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[AnalysisStage]string{}
	for _, stage := range analysisTestStages {
		request := analysisStageRequestFor(t, s, "main.go", stage)
		result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
		if err != nil || result.Progress.Cached || result.Progress.FindingCount == nil || result.Progress.ReportID == "" {
			t.Fatalf("%s result=%+v, %v", stage, result, err)
		}
		if stage == AnalysisStageSecurityRules {
			if result.Progress.Status != AnalysisStageCompletedEmpty || result.Progress.Attempts != 0 || result.Security.Source != project.SecuritySourceDeterministic {
				t.Fatalf("rules=%+v", result)
			}
		} else if result.Progress.Status != AnalysisStageCompleted || result.Progress.Attempts != 1 {
			t.Fatalf("model stage=%+v", result)
		}
		if stage == AnalysisStageSemantic && (result.Semantic.Risks[0].Category != project.FindingCategoryBugs || result.Semantic.Risks[1].Category != project.FindingCategorySecurity) {
			t.Fatal("semantic categories lost")
		}
		if stage == AnalysisStageSecurityAI && (result.Security.Source != project.SecuritySourceAI || result.Security.Findings[0].EvidenceKind != "model_suspicion") {
			t.Fatal("Security AI lost provenance")
		}
		for _, id := range ids {
			if id == result.Progress.ReportID {
				t.Fatal("unrelated producer references merged")
			}
		}
		ids[stage] = result.Progress.ReportID
	}
	if calls.Load() != 3 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	for _, stage := range analysisTestStages {
		request := analysisStageRequestFor(t, s, "main.go", stage)
		request.RemainingAttempts = 0
		request.SecurityReview = false
		authority := analysisStageAuthorityFor(t, request)
		authority.BeforeAttempt = func(context.Context, AnalysisRunIdentity, AnalysisFileIdentity, AnalysisStage) error {
			t.Fatal("cache read reserved a model request")
			return nil
		}
		result, err := s.analyzeFileStage(context.Background(), request, authority)
		if err != nil || !result.Progress.Cached || result.Progress.Attempts != 0 || result.Progress.ReportID != ids[stage] {
			t.Fatalf("cache %s=%+v, %v", stage, result, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("cached evidence called a provider")
	}
	for _, stage := range analysisTestStages {
		request := analysisStageRequestFor(t, s, "main.go", stage)
		request.Refresh = true
		result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
		if err != nil || result.Progress.Cached != (stage == AnalysisStageSecurityRules) || result.Progress.ReportID != ids[stage] {
			t.Fatalf("refresh %s=%+v, %v", stage, result, err)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
	after, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil || string(before) != string(after) {
		t.Fatal("source analysis changed project source")
	}
}

func TestAnalysisFileFailureRetainsEarlierReportsAndAllowsOtherStages(t *testing.T) {
	var failing atomic.Bool
	server, _ := analysisResponseServer(t, func(stage AnalysisStage) string {
		if failing.Load() && stage == AnalysisStageSemantic {
			return `{"private-provider-text":"not a summary"}`
		}
		if stage == AnalysisStagePerformance {
			return strings.Replace(validPerformanceReview, `"findings":[`, `"findings":[{},`, 1)
		}
		return analysisReply(stage)
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSemantic)
	previous, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	request.Refresh = true
	failed, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || failed.Progress.Status != AnalysisStageFailed || failed.Progress.FindingCount != nil || failed.Semantic != nil || strings.Contains(failed.Progress.Reason, "private-provider-text") {
		t.Fatalf("failed=%+v, %v", failed, err)
	}
	stored, err := s.CachedFileAnalysis("main.go")
	if err != nil || stored.GeneratedAt != previous.Semantic.GeneratedAt || len(stored.Risks) != 2 {
		t.Fatal("failure replaced successful semantic evidence")
	}
	for _, stage := range []AnalysisStage{AnalysisStagePerformance, AnalysisStageSecurityRules, AnalysisStageSecurityAI} {
		request := analysisStageRequestFor(t, s, "main.go", stage)
		result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
		if err != nil || result.Progress.FindingCount == nil {
			t.Fatalf("later stage=%+v, %v", result, err)
		}
		if stage == AnalysisStagePerformance && (result.Progress.Status != AnalysisStagePartial || *result.Progress.FindingCount != 1 || result.Performance.Warning == "") {
			t.Fatal("partial performance evidence labeled complete")
		}
	}
}

func TestAnalysisFileUnsupportedRulesDoNotSuppressAIAndPartialRulesStayPartial(t *testing.T) {
	server, calls := analysisResponseServer(t, func(AnalysisStage) string { return `{"findings":[]}` })
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "sample.lua"), []byte("function run() return 1 end\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := "package main\nimport \"os/exec\"\nfunc Calls(input string) {\n" + strings.Repeat("_ = exec.Command(\"sh\", \"-c\", input)\n", 6) + "}\n"
	if err := os.WriteFile(filepath.Join(root, "rules.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	request := analysisStageRequestFor(t, s, "sample.lua", AnalysisStageSecurityRules)
	result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageUnavailable || result.Progress.FindingCount != nil || result.Progress.Reason != noRulePackReason {
		t.Fatalf("unsupported rules=%+v, %v", result, err)
	}
	request.Stage = AnalysisStageSecurityAI
	result, err = s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageCompletedEmpty || result.Progress.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("Lua advisory=%+v, %v", result, err)
	}
	request = analysisStageRequestFor(t, s, "rules.go", AnalysisStageSecurityRules)
	for run := 0; run < 2; run++ {
		result, err = s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
		if err != nil || result.Progress.Status != AnalysisStagePartial || *result.Progress.FindingCount != 5 || result.Progress.Cached != (run == 1) || result.Progress.Attempts != 0 {
			t.Fatalf("partial rules=%+v, %v", result, err)
		}
	}
}

func TestAnalysisFileRequiresStageConsentAndDurableReservationsBeforeEveryAttempt(t *testing.T) {
	for _, stage := range []AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityAI} {
		t.Run(string(stage), func(t *testing.T) {
			server, calls := analysisResponseServer(t, analysisReply)
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			s.conf().runtimes.bug.effective.RemoteProvider = true
			s.conf().runtimes.analyze.effective.RemoteProvider = true
			request := analysisStageRequestFor(t, s, "main.go", stage)
			reserved := 0
			authority := analysisStageAuthorityFor(t, request)
			authority.BeforeAttempt = func(context.Context, AnalysisRunIdentity, AnalysisFileIdentity, AnalysisStage) error {
				reserved++
				return errors.New("durable reservation failed")
			}
			result, err := s.analyzeFileStage(context.Background(), request, authority)
			if err == nil || reserved != 0 || calls.Load() != 0 || result.Progress.FindingCount != nil {
				t.Fatal("missing remote consent reached reservation/transport")
			}
			request.ConfirmRemoteProvider = true
			result, err = s.analyzeFileStage(context.Background(), request, authority)
			if err == nil || reserved != 1 || calls.Load() != 0 || result.Progress.Attempts != 0 {
				t.Fatal("failed reservation reached transport")
			}
		})
	}
	server, calls := analysisResponseServer(t, analysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityAI)
	request.SecurityReview = false
	if _, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); err == nil || calls.Load() != 0 {
		t.Fatal("local security AI bypassed explicit intent")
	}
}

func TestAnalysisFileAttemptsAreBoundedAndRecheckOwnershipBetweenRetries(t *testing.T) {
	for _, test := range []struct {
		name      string
		remaining int
		revoke    bool
		wantCalls int32
		wantErr   bool
	}{
		{"zero allowance", 0, false, 0, true}, {"one allowance", 1, false, 1, false}, {"two allowances", 2, false, 2, false}, {"revoked retry", 2, true, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				http.Error(w, "temporary provider failure", 500)
			}))
			defer server.Close()
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			s.conf().retryBase = time.Millisecond
			s.conf().retryMax = time.Millisecond
			s.conf().runtimes.bug.effective.MaxRetries = 8
			request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSemantic)
			request.RemainingAttempts = test.remaining
			authority := analysisStageAuthorityFor(t, request)
			check := authority.Check
			authority.Check = func(ctx context.Context, run AnalysisRunIdentity) error {
				if test.revoke && calls.Load() > 0 {
					return project.ErrRevisionConflict
				}
				return check(ctx, run)
			}
			result, err := s.analyzeFileStage(context.Background(), request, authority)
			if (err != nil) != test.wantErr || calls.Load() != test.wantCalls || result.Progress.Attempts != int(test.wantCalls) || result.Progress.FindingCount != nil {
				t.Fatalf("retry result=%+v, %v calls=%d", result, err, calls.Load())
			}
			if s.conf().runtimes.bug.effective.MaxRetries != 8 {
				t.Fatal("stage budget mutated the shared runtime")
			}
		})
	}
}

func TestAnalysisFilePublicationRejectsStaleCanceledOrReplacedWork(t *testing.T) {
	for _, stage := range analysisTestStages {
		for _, change := range []string{"source", "policy", "cancel", "generation", "provider", "storage", "skipped callback"} {
			t.Run(string(stage)+"/"+change, func(t *testing.T) {
				server, _ := analysisResponseServer(t, analysisReply)
				s, root := newSemanticAnalysisService(t, server.URL, 0)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request := analysisStageRequestFor(t, s, "main.go", stage)
				authority := analysisStageAuthorityFor(t, request)
				publish := authority.Publish
				authority.Publish = func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
					switch change {
					case "source":
						if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0600); err != nil {
							return err
						}
					case "policy":
						if err := os.WriteFile(filepath.Join(root, ".mini-orcaignore"), []byte("main.go\n"), 0600); err != nil {
							return err
						}
					case "cancel":
						cancel()
					case "generation":
						run.Generation = "replacement"
					case "provider":
						s.conf().runtimes.analyze.effective.Model = "replacement"
					case "storage":
						return errors.New("durable result failed")
					case "skipped callback":
						return nil
					}
					return publish(run, file, stage, write)
				}
				result, err := s.analyzeFileStage(ctx, request, authority)
				if err == nil || result.Progress.FindingCount != nil || result.Semantic != nil || result.Performance != nil || result.Security != nil {
					t.Fatalf("rejected publication=%+v, %v", result, err)
				}
				for _, directory := range []string{"file-analysis", "performance/files", "security/files"} {
					entries, err := os.ReadDir(filepath.Join(root, ".mini-orca", directory))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if len(entries) != 0 {
						t.Fatalf("rejected publication stored %s: %v", directory, entries)
					}
				}
			})
		}
	}
}

func TestAnalysisFileCachedEvidenceRechecksCancellationAndRunIdentity(t *testing.T) {
	server, _ := analysisResponseServer(t, analysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSecurityRules)
	if _, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); err != nil {
		t.Fatal(err)
	}
	load := s.loadSecurityFileReport
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.loadSecurityFileReport = func(root string, input project.SecurityReportInput) (*project.SecurityFileReport, error) {
		report, err := load(root, input)
		cancel()
		return report, err
	}
	result, err := s.analyzeFileStage(ctx, request, analysisStageAuthorityFor(t, request))
	if !errors.Is(err, context.Canceled) || result.Security != nil || result.Progress.FindingCount != nil {
		t.Fatalf("canceled cache=%+v, %v", result, err)
	}
	request.Run.Generation = "replacement"
	if _, err := s.analyzeFileStage(context.Background(), request, analysisFileStageAuthority{}); err == nil {
		t.Fatal("missing run authority accepted")
	}
}

func TestAnalysisFileModelIdentityChangesInvalidateCacheAndSourceEchoIsFailed(t *testing.T) {
	var echo atomic.Bool
	server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
		if echo.Load() && stage == AnalysisStageSecurityAI {
			return strings.Replace(validSecurityReview, "The operation uses input without an observed validation boundary.", "package main", 1)
		}
		return analysisReply(stage)
	})
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStagePerformance)
	if _, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request)); err != nil {
		t.Fatal(err)
	}
	s.conf().runtimes.analyze.effective.ReasoningEffort = "high"
	result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Cached || calls.Load() != 2 {
		t.Fatalf("changed provider cache=%+v, %v", result, err)
	}
	echo.Store(true)
	request.Stage = AnalysisStageSecurityAI
	result, err = s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageFailed || result.Security != nil || result.Progress.FindingCount != nil {
		t.Fatalf("source echo=%+v, %v", result, err)
	}
}

func TestAnalysisFileCancellationInterruptsActiveModelRequests(t *testing.T) {
	for _, stage := range []AnalysisStage{AnalysisStageSemantic, AnalysisStagePerformance, AnalysisStageSecurityAI} {
		t.Run(string(stage), func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				started <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			t.Cleanup(server.Close)
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { cancel(); close(release) })
			request := analysisStageRequestFor(t, s, "main.go", stage)
			authority := analysisStageAuthorityFor(t, request)
			done := make(chan error, 1)
			var result analysisFileStageResult
			go func() { var err error; result, err = s.analyzeFileStage(ctx, request, authority); done <- err }()
			waitForTestSignal(t, started, "coordinated model request")
			cancel()
			if err := waitForTestError(t, done, "coordinated cancellation"); !errors.Is(err, context.Canceled) || result.Progress.Status != AnalysisStageCanceled || result.Progress.Attempts != 1 || result.Progress.FindingCount != nil {
				t.Fatalf("cancellation=%+v, %v", result, err)
			}
		})
	}
}

func TestAnalysisFileRejectsCapturedFileMismatchAndReportsUnavailableModel(t *testing.T) {
	server, calls := analysisResponseServer(t, analysisReply)
	s, _ := newSemanticAnalysisService(t, server.URL, 0)
	request := analysisStageRequestFor(t, s, "main.go", AnalysisStageSemantic)
	request.File.ContentHash = "different"
	result, err := s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if !errors.Is(err, project.ErrRevisionConflict) || result.Progress.Status != AnalysisStageStale || calls.Load() != 0 {
		t.Fatalf("captured mismatch=%+v, %v", result, err)
	}
	request = analysisStageRequestFor(t, s, "main.go", AnalysisStageSemantic)
	s.conf().runtimes.bug.client = nil
	result, err = s.analyzeFileStage(context.Background(), request, analysisStageAuthorityFor(t, request))
	if err != nil || result.Progress.Status != AnalysisStageUnavailable || result.Progress.FindingCount != nil || result.Progress.Attempts != 0 || calls.Load() != 0 {
		t.Fatalf("unavailable model=%+v, %v", result, err)
	}
}

func TestAnalysisFileSemanticPreparationFailureDoesNotStoreModelFailure(t *testing.T) {
	server, calls := analysisResponseServer(t, analysisReply)
	s, root := newSemanticAnalysisService(t, server.URL, 0)
	source := "package main\nfunc Run() {}\n//" + strings.Repeat("x", maxSemanticAnalysisBytes)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	result, err := semanticStageFor(t, s, "main.go", true)
	if err != nil || result.Progress.Status != AnalysisStageSkipped || result.Semantic != nil || calls.Load() != 0 {
		t.Fatalf("oversized input=%+v, %v calls=%d", result, err, calls.Load())
	}
	entries, err := os.ReadDir(filepath.Join(root, ".mini-orca", "file-analysis"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("input preparation wrote a model-failure report: %v", entries)
	}
}

func TestAnalysisEmptyReviewSectionsCompleteAndRemainCached(t *testing.T) {
	for _, output := range []string{"", " \n", "[]", `{"findings":[]}`} {
		t.Run(output, func(t *testing.T) {
			server, calls := analysisResponseServer(t, func(stage AnalysisStage) string {
				if stage == AnalysisStageSemantic {
					return emptyAnalysisReply(stage)
				}
				return output
			})
			s, _ := newSemanticAnalysisService(t, server.URL, 0)
			for pass := 0; pass < 2; pass++ {
				preview := analysisRunPreviewFor(t, s, AnalysisRunLimits{100, 30, 1}, nil)
				if _, err := s.StartAnalysisRun(context.Background(), analysisStartFor(preview)); err != nil {
					t.Fatal(err)
				}
				run := completedAnalysisRun(t, s)
				if run.Status != AnalysisRunCompletedEmpty || calls.Load() != 3 {
					t.Fatalf("run=%+v calls=%d", run, calls.Load())
				}
				for _, section := range run.Sections {
					if section.Status != AnalysisRunCompletedEmpty || section.FindingCount == nil || *section.FindingCount != 0 {
						t.Fatalf("empty section=%+v", section)
					}
				}
				assertSelectionStages(t, readSelectionFor(t, s), "main.go", "fresh", "up to date")
			}
		})
	}
}
