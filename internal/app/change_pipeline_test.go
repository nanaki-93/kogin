package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const workflowImplementation = `{"explanation":"Add Helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 2 }\n"}]}`
const workflowTests = `{"explanation":"Exercise Helper.","changes":[{"path":"new_test.go","content":"package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 2 { t.Fatal(\"wrong result\") } }\n"}]}`
const workflowApproval = `{"verdict":"approve","summary":"The implementation and test meet the captured criteria.","findings":[]}`

func workflowService(t *testing.T, outputs [3]func() string) (*Service, string, *ChangeSession) {
	t.Helper()
	create, test, review := changeProvider(t, outputs[0]), changeProvider(t, outputs[1]), changeProvider(t, outputs[2])
	base, root := newSemanticAnalysisService(t, create.URL, 0)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/workflow\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Reindex(); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(create.URL)
	// Deliberately choose profiles independently of their usual prompt scopes.
	cfg.ModelScopes.Bug.APIBaseURL, cfg.ModelScopes.Bug.Model = create.URL, "creator"
	cfg.ModelScopes.Analyze.APIBaseURL, cfg.ModelScopes.Analyze.Model = test.URL, "tester"
	cfg.ModelScopes.Code.APIBaseURL, cfg.ModelScopes.Code.Model = review.URL, "reviewer"
	s, err := New(cfg, base.manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CancelChangeWorkflows)
	session := openChangeFixture(t, s, "new.go", "new_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	return s, root, session
}

func workflowRequest(session *ChangeSession) ChangeWorkflowRequest {
	return ChangeWorkflowRequest{ChangeIdentity: changeIdentity(session), Message: "Add Helper returning two, with a regression test.", Models: ChangeWorkflowModels{Create: "bug", Test: "analyze", Review: "code"}}
}

func finishedWorkflow(t *testing.T, s *Service, id string) *ChangeSession {
	t.Helper()
	s.changeWorkflow.mu.Lock()
	job := s.changeWorkflow.getJob(id)
	if job == nil {
		job = s.changeWorkflow.job
	}
	done := job.done
	s.changeWorkflow.mu.Unlock()
	// done is the synchronization; the deadline only guards against a hang and
	// allows for the race detector's slowdown of real check processes.
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("workflow did not finish")
	}
	session, err := s.ChangeSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestChangeWorkflowSelectedModelsTestsReviewAndHumanApply(t *testing.T) {
	var calls [3]atomic.Int32
	outputs := [3]func() string{}
	for i, output := range []string{workflowImplementation, workflowTests, workflowApproval} {
		i, output := i, output
		outputs[i] = func() string { calls[i].Add(1); return output }
	}
	s, root, initial := workflowService(t, outputs)
	started, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial))
	if err != nil || started.Workflow.Status != "running" || started.Revision != 1 {
		t.Fatalf("admission: %+v, %v", started, err)
	}
	session := finishedWorkflow(t, s, initial.ID)
	if session.Workflow.Status != "awaiting_human_review" || session.Workflow.Review.ProposalHash != session.Hash || session.ReviewedHash != "" || len(session.Changes) != 2 {
		t.Fatalf("handoff: %+v", session)
	}
	for i, name := range []string{"creator", "tester", "reviewer"} {
		if calls[i].Load() != []int32{1, 1, 3}[i] || session.Workflow.Stages[i].Model.Model != name || session.Workflow.Stages[i].Status != "completed" {
			t.Fatalf("stage %d: %+v, calls=%d", i, session.Workflow.Stages[i], calls[i].Load())
		}
	}
	if !requiredChecksPassed(session.Checks) || len(session.PinnedTests) != 1 {
		t.Fatalf("real baseline/candidate tests were not retained: %+v", session.Checks)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Fatal("workflow wrote project source")
	}
	if _, err := s.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}); err == nil {
		t.Fatal("model review authorized Apply without human review")
	}
	if _, err := s.ReviewChange(context.Background(), session.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(session)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyChange(context.Background(), session.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(session), Confirm: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "new.go"))
	if err != nil || !strings.Contains(string(data), "return 2") {
		t.Fatalf("human Apply: %s, %v", data, err)
	}
}

func TestChangeWorkflowAdmissionGuardsDispatchNothing(t *testing.T) {
	var calls atomic.Int32
	reply := func() string { calls.Add(1); return workflowImplementation }
	s, _, session := workflowService(t, [3]func() string{reply, reply, reply})
	for _, name := range []string{"untrusted", "profile", "remote", "security", "stale", "canceled", "test_scope", "streaming"} {
		t.Run(name, func(t *testing.T) {
			request := workflowRequest(session)
			ctx := context.Background()
			switch name {
			case "streaming":
				request.StreamOutput = true
				previous := s.conf().runtimes.function.profile.Provider
				s.conf().runtimes.function.profile.Provider = config.PiProvider
				defer func() { s.conf().runtimes.function.profile.Provider = previous }()
			case "untrusted":
				s.clearExecutionTrust()
				defer s.TrustProjectExecution(context.Background(), session.ProjectRevision, true)
			case "profile":
				request.Models.Test = "arbitrary-model"
			case "remote":
				s.conf().runtimes.function.effective.RemoteProvider = true
				request.ConfirmedProfiles = []string{"bug", "analyze"}
				defer func() { s.conf().runtimes.function.effective.RemoteProvider = false }()
			case "security":
				session.Kind = "security"
				if err := storeChangeSession(s.manager.Root(), session); err != nil {
					t.Fatal(err)
				}
				defer func() {
					session.Kind = "feature"
					if err := storeChangeSession(s.manager.Root(), session); err != nil {
						t.Error(err)
					}
				}()
			case "stale":
				request.Hash = "old-proposal"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "test_scope":
				other := openChangeFixture(t, s, "new.go")
				request = workflowRequest(other)
				if _, err := s.StartChangeWorkflow(ctx, other.ID, request); err == nil {
					t.Fatal("accepted missing tests")
				}
				return
			}
			if _, err := s.StartChangeWorkflow(ctx, session.ID, request); err == nil {
				t.Fatal("invalid admission succeeded")
			}
		})
	}
	stored, err := s.ChangeSession(context.Background(), session.ID)
	if err != nil || stored.Workflow != nil || stored.Revision != 0 || calls.Load() != 0 {
		t.Fatalf("rejected run mutated or dispatched: %+v %d, %v", stored, calls.Load(), err)
	}
}

func TestChangeWorkflowDefaultRolesRequireConsentAndShareBudget(t *testing.T) {
	var calls atomic.Int32
	reply := func() string { calls.Add(1); return workflowImplementation }
	s, _, initial := workflowService(t, [3]func() string{reply, reply, reply})
	s.conf().agentRuntime.Limits.ModelCalls = 1
	s.conf().runtimes.bug.effective.RemoteProvider = true
	req := workflowRequest(initial)
	req.Models = ChangeWorkflowModels{}
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, req); err == nil || calls.Load() != 0 {
		t.Fatalf("default role bypassed consent: %v", err)
	}
	req.ConfirmedProfiles = []string{"bug"}
	started, err := s.StartChangeWorkflow(context.Background(), initial.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if started.Workflow.Models != (ChangeWorkflowModels{"bug", "code", "analyze"}) {
		t.Fatalf("resolved models not saved: %+v", started.Workflow.Models)
	}
	finished := finishedWorkflow(t, s, initial.ID)
	if finished.Workflow.Status != "failed" || calls.Load() != 1 || !strings.Contains(finished.Workflow.Reason, "model calls") {
		t.Fatalf("stage budget refreshed: %+v calls=%d", finished.Workflow, calls.Load())
	}
}

func TestChangeWorkflowSupportsCatalogAndCLIModels(t *testing.T) {
	var calls [3]atomic.Int32
	outputs := [3]func() string{}
	for i, output := range []string{workflowImplementation, workflowTests, workflowApproval} {
		i, output := i, output
		outputs[i] = func() string { calls[i].Add(1); return output }
	}
	s, _, initial := workflowService(t, outputs)
	s.discoverPiModels = func(context.Context, string) ([]llm.PiModel, error) {
		return []llm.PiModel{{Provider: "openai", ID: "gpt-5"}}, nil
	}
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) {
		return []llm.AgyModel{{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"}}, nil
	}
	catalog, err := s.AvailableModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := workflowRequest(initial)
	request.Models = ChangeWorkflowModels{
		Create: catalog.Defaults["bug"],
		Test:   catalog.Defaults["analyze"],
		Review: "agy:unlisted-model",
	}
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, request); !errors.Is(err, project.ErrRevisionConflict) {
		t.Fatalf("unlisted CLI model admitted: %v", err)
	}
	s.conf().runtimes.bug.effective.Timeout = "2m0s"
	s.conf().runtimes.function.effective.Timeout = "5m0s"
	request.Models.Review = catalog.Defaults["code"]
	started, err := s.StartChangeWorkflow(context.Background(), initial.ID, request)
	if err != nil || started.Workflow.Status != "running" {
		t.Fatalf("catalog workflow admission: %+v, %v", started, err)
	}
	session := finishedWorkflow(t, s, initial.ID)
	if session.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("catalog workflow status: %+v", session.Workflow)
	}
	expectedProfiles := []string{catalog.Defaults["bug"], catalog.Defaults["analyze"], catalog.Defaults["code"]}
	expectedScopes := []string{"bug", "code", "analyze"}
	for i, name := range []string{"creator", "tester", "reviewer"} {
		stage := session.Workflow.Stages[i]
		if calls[i].Load() != []int32{1, 1, 3}[i] || stage.Model.Model != name || stage.Model.Profile != expectedProfiles[i] || stage.Model.Scope != expectedScopes[i] || stage.Model.Timeout != "15m0s" {
			t.Fatalf("stage %d provenance: %+v", i, stage)
		}
	}
}

func TestChangeWorkflowSupportsPolyglotProjects(t *testing.T) {
	tsImpl := `{"explanation":"Add compute helper.","changes":[{"path":"app.ts","content":"export function compute(): number {\n  return 42;\n}\n"}]}`
	tsTests := `{"explanation":"Add TypeScript test.","changes":[{"path":"app.test.ts","content":"import { compute } from './app';\nif (compute() !== 42) throw new Error('bad');\n"}]}`
	fnServer := changeProvider(t, func() string { return workflowApproval })
	create, testSrv := changeProvider(t, func() string { return tsImpl }), changeProvider(t, func() string { return tsTests })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"ts-fixture"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("test:\n\t@true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.ts"), []byte("import { helper } from './helper';\nexport function compute(): number {\n  return 1;\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := project.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &project.Analysis{Name: "ts-fixture", Path: root, Type: "typescript"}); err != nil {
		t.Fatal(err)
	}
	cfg := scopedTestConfig(create.URL)
	cfg.ModelScopes.Bug.APIBaseURL, cfg.ModelScopes.Bug.Model = create.URL, "creator"
	cfg.ModelScopes.Analyze.APIBaseURL, cfg.ModelScopes.Analyze.Model = testSrv.URL, "tester"
	cfg.ModelScopes.Code.APIBaseURL, cfg.ModelScopes.Code.Model = fnServer.URL, "reviewer"
	s, err := New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CancelChangeWorkflows)

	session := openChangeFixture(t, s, "app.ts", "app.test.ts")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatalf("TypeScript workflow start failed: %v", err)
	}
	finished := finishedWorkflow(t, s, session.ID)
	if finished.Workflow.Status != "awaiting_human_review" || len(finished.Changes) != 2 {
		t.Fatalf("TypeScript workflow did not reach human review: %+v", finished)
	}
}

func TestConcurrentChangeWorkflowsDoNotConflict(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var createCalls atomic.Int32
	s, _, first := workflowService(t, [3]func() string{
		func() string {
			if createCalls.Add(1) == 1 {
				close(firstEntered)
				<-releaseFirst
			}
			return workflowImplementation
		},
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	second := openChangeFixture(t, s, "new.go", "new_test.go")

	if _, err := s.StartChangeWorkflow(context.Background(), first.ID, workflowRequest(first)); err != nil {
		t.Fatalf("start first workflow: %v", err)
	}
	<-firstEntered

	if _, err := s.StartChangeWorkflow(context.Background(), second.ID, workflowRequest(second)); err != nil {
		close(releaseFirst)
		t.Fatalf("expected second session workflow to start concurrently without conflict, got %v", err)
	}
	close(releaseFirst)

	res1 := finishedWorkflow(t, s, first.ID)
	res2 := finishedWorkflow(t, s, second.ID)
	if res1.Workflow.Status != "awaiting_human_review" || res2.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected both concurrent workflows to succeed, got first=%+v second=%+v", res1.Workflow, res2.Workflow)
	}
}
