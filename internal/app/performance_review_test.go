package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

func TestReviewPerformanceFileRejectsChangedSourceOrPolicyBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, root string)
	}{
		{name: "source", change: func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() { println(\"changed\") }\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy", change: func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, ".kasuriignore"), []byte("main.go\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				started <- struct{}{}
				<-release
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validPerformanceReview}}}})
			}))
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			done := make(chan error, 1)
			authorizationCalls := 0
			go func() {
				_, err := reviewPerformanceStageForTest(t, service, context.Background(), func(_ func() error) error {
					authorizationCalls++
					return nil
				})
				done <- err
			}()
			waitForTestSignal(t, started, "performance review request")
			test.change(t, root)
			close(release)
			if err := waitForTestError(t, done, "performance review result"); !errors.Is(err, project.ErrRevisionConflict) {
				t.Fatalf("review error = %v", err)
			}
			if authorizationCalls != 0 {
				t.Fatalf("publication authorization was called %d times", authorizationCalls)
			}
			assertNoPerformanceReport(t, root)
		})
	}
}

func TestReviewPerformanceFileRechecksSnapshotDuringPublicationAuthorization(t *testing.T) {
	for _, test := range []struct {
		name      string
		authorize func(t *testing.T, root string, publish func() error) error
		want      error
	}{
		{name: "source change", authorize: func(t *testing.T, root string, publish func() error) error {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() { println(\"changed\") }\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return publish()
		}, want: project.ErrRevisionConflict},
		{name: "policy change", authorize: func(t *testing.T, root string, publish func() error) error {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, ".kasuriignore"), []byte("main.go\n"), 0644); err != nil {
				t.Fatal(err)
			}
			return publish()
		}, want: project.ErrRevisionConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := performanceReviewResponseServer(t)
			defer server.Close()
			service, root := newSemanticAnalysisService(t, server.URL, 0)
			_, err := reviewPerformanceStageForTest(t, service, context.Background(), func(publish func() error) error {
				return test.authorize(t, root, publish)
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("review error = %v", err)
			}
			assertNoPerformanceReport(t, root)
		})
	}
}

func TestReviewPerformanceFileRechecksCancellationDuringPublicationAuthorization(t *testing.T) {
	server := performanceReviewResponseServer(t)
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := reviewPerformanceStageForTest(t, service, ctx, func(publish func() error) error {
		cancel()
		return publish()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("review error = %v", err)
	}
	assertNoPerformanceReport(t, root)
}

func TestReviewPerformanceFileRequiresAuthorizedPublication(t *testing.T) {
	server := performanceReviewResponseServer(t)
	defer server.Close()
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	_, err := reviewPerformanceStageForTest(t, service, context.Background(), func(_ func() error) error {
		return nil
	})
	if err == nil {
		t.Fatal("review returned a report without publication")
	}
	assertNoPerformanceReport(t, root)
}

func TestReviewPerformanceFileCancellationPreventsPublication(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validPerformanceReview}}}})
	}))
	defer server.Close()
	defer close(release)
	service, root := newSemanticAnalysisService(t, server.URL, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := reviewPerformanceStageForTest(t, service, ctx, nil)
		done <- err
	}()
	waitForTestSignal(t, started, "performance review request")
	cancel()
	if err := waitForTestError(t, done, "canceled performance review"); !errors.Is(err, context.Canceled) {
		t.Fatalf("review error = %v", err)
	}
	assertNoPerformanceReport(t, root)
}

func assertNoPerformanceReport(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".kasuri", "performance", "files"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected performance reports: %+v", entries)
	}
}

func performanceReviewResponseServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: validPerformanceReview}}}})
	}))
}

// Retain publication-boundary regressions on the coordinator's production stage path.
func reviewPerformanceStageForTest(t *testing.T, service *Service, ctx context.Context, authorize func(func() error) error) (analysisFileStageResult, error) {
	t.Helper()
	request := analysisStageRequestFor(t, service, "main.go", AnalysisStagePerformance)
	request.ConfirmRemoteProvider = true
	authority := analysisStageAuthorityFor(t, request)
	publish := authority.Publish
	authority.Publish = func(run AnalysisRunIdentity, file AnalysisFileIdentity, stage AnalysisStage, write func() error) error {
		return publish(run, file, stage, func() error {
			if authorize != nil {
				return authorize(write)
			}
			return write()
		})
	}
	return service.analyzeFileStage(ctx, request, authority)
}

const validPerformanceReview = `{"findings":[{"category":"cpu","potential_impact":"low","confidence":"low","title":"Avoid repeated formatting","observed_pattern":"The selected function formats output on each call.","workload_conditions":"This matters only when the function is called frequently.","recommendation":"Measure before changing the formatting path.","tradeoff":"Caching may retain stale output.","verification_plan":"Benchmark representative repeated calls.","start_line":5,"end_line":5}]}`

func storeCachedPerformanceReport(t *testing.T, service *Service, root string) (*project.Analysis, *project.IndexFile, *project.ContextPolicy) {
	t.Helper()
	analysis, err := service.manager.Analysis()
	if err != nil {
		t.Fatal(err)
	}
	file, err := service.manager.IndexedFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	cachedReport := project.PerformanceFileReport{
		SchemaVersion: "1", ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision,
		Path: file.Path, ContentHash: file.ContentHash, Status: "completed",
		Findings: []project.PerformanceFinding{{ID: "performance:cached", Category: "cpu"}},
		Model:    "model", Profile: "analyze", Scope: "analyze", PromptVersion: project.PerformancePromptVersion,
		ContextPolicyVersion: policy.Version(), GeneratedAt: time.Now().UTC(),
	}
	if err := project.StorePerformanceFileReport(root, cachedReport); err != nil {
		t.Fatal(err)
	}
	return analysis, file, policy
}
