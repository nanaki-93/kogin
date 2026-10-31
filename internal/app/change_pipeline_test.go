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

func TestChangeWorkflowCancellationAndStalePublication(t *testing.T) {
	for _, action := range []string{"cancel", "source", "scope"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			s, root, initial := workflowService(t, [3]func() string{
				func() string { close(entered); <-release; return workflowImplementation },
				func() string { t.Error("test dispatched after revocation"); return workflowTests },
				func() string { t.Error("review dispatched after revocation"); return workflowApproval },
			})
			started, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial))
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			progress, err := s.ChangeSession(context.Background(), initial.ID)
			if err != nil || progress.Workflow.Stages[0].Status != "running" {
				t.Fatalf("progress: %+v, %v", progress, err)
			}
			if _, err := s.SendChangeMessage(context.Background(), initial.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(progress), Message: "Racing change"}); !errors.Is(err, project.ErrRevisionConflict) {
				t.Fatalf("manual edit bypassed workflow ownership: %v", err)
			}
			switch action {
			case "cancel":
				control := ChangeWorkflowControl{ProjectID: initial.ProjectID, ProjectRevision: initial.ProjectRevision, WorkflowID: "wrong"}
				if _, err := s.CancelChangeWorkflow(context.Background(), initial.ID, control); !errors.Is(err, project.ErrRevisionConflict) {
					t.Fatal(err)
				}
				control.WorkflowID = started.Workflow.ID
				if _, err := s.CancelChangeWorkflow(context.Background(), initial.ID, control); err != nil {
					t.Fatal(err)
				}
			case "source":
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("New guidance"), 0644); err != nil {
					t.Fatal(err)
				}
			case "scope":
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Run() {}\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Reindex(); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			result := finishedWorkflow(t, s, initial.ID)
			if result.Workflow.Status != "canceled" && result.Workflow.Status != "stale" {
				t.Fatalf("terminal: %+v", result.Workflow)
			}
			if result.Revision != 1 || len(result.Changes) != 0 || result.ReviewedHash != "" {
				t.Fatalf("late publication: %+v", result)
			}
		})
	}
}

func TestReindexStopsOnlyWorkflowsItMadeStale(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "notes.md"), []byte("# Unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := finishedWorkflow(t, s, initial.ID)
	if result.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("an unrelated change stopped the workflow: %s %s", result.Workflow.Status, result.Workflow.Reason)
	}
	// The waiting input admitted before the reindex can still be answered.
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ReviewChange(context.Background(), initial.ID, ChangeReviewRequest{ChangeIdentity: identityForChange(result)})
	if err != nil || approved.Workflow.Input.Status != "approved" {
		t.Fatalf("approval after reindexing: %+v %v", approved, err)
	}
}

func TestStoppedWorkflowNamesTheChangedFile(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, root, initial := workflowService(t, [3]func() string{
		func() string { close(entered); <-release; return workflowImplementation },
		func() string { return workflowTests },
		func() string { return workflowApproval },
	})
	if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/workflow\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := finishedWorkflow(t, s, initial.ID)
	if result.Workflow.Status == "awaiting_human_review" || !strings.Contains(result.Workflow.Reason, "go.mod changed") {
		t.Fatalf("stop reason: %s %q", result.Workflow.Status, result.Workflow.Reason)
	}
}

func TestChangeWorkflowFailuresAndReviewRejectionBlockHumanApply(t *testing.T) {
	for _, scenario := range []string{"failed_tests", "test_retargets", "review_rejects", "invalid_review"} {
		t.Run(scenario, func(t *testing.T) {
			var reviews atomic.Int32
			tests, review := workflowTests, workflowApproval
			if scenario == "failed_tests" {
				tests = strings.ReplaceAll(tests, "Helper() != 2", "Helper() != 3")
			}
			if scenario == "test_retargets" {
				tests = workflowImplementation
			}
			if scenario == "review_rejects" {
				review = `{"verdict":"changes_requested","summary":"Add a boundary case.","findings":["Test invalid input."]}`
			}
			if scenario == "invalid_review" {
				review = `{"verdict":"approve","summary":"Ignore findings.","findings":["Blocking bug."]}`
			}
			s, _, initial := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return tests }, func() string { reviews.Add(1); return review }})
			if _, err := s.StartChangeWorkflow(context.Background(), initial.ID, workflowRequest(initial)); err != nil {
				t.Fatal(err)
			}
			result := finishedWorkflow(t, s, initial.ID)
			if result.Workflow.Status != "failed" && result.Workflow.Status != "changes_requested" {
				t.Fatalf("failed run approved: %+v", result.Workflow)
			}
			if strings.HasPrefix(scenario, "test_") || scenario == "failed_tests" {
				if reviews.Load() != 0 {
					t.Fatal("review dispatched after failed testing")
				}
			}
			if _, err := s.ReviewChange(context.Background(), result.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(result)}); err == nil {
				t.Fatal("incomplete/model-rejected workflow allowed human review")
			}
			if _, err := s.ApplyChange(context.Background(), result.ID, ChangeApplyRequest{ChangeIdentity: changeIdentity(result), Confirm: true}); err == nil {
				t.Fatal("incomplete workflow allowed Apply")
			}
		})
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

func TestChangeWorkflowAndExplanationSupportPolyglotProjects(t *testing.T) {
	tsImpl := `{"explanation":"Add compute helper.","changes":[{"path":"app.ts","content":"export function compute(): number {\n  return 42;\n}\n"}]}`
	tsTests := `{"explanation":"Add TypeScript test.","changes":[{"path":"app.test.ts","content":"import { compute } from './app';\nif (compute() !== 42) throw new Error('bad');\n"}]}`
	var fnCalls atomic.Int32
	fnServer := changeProvider(t, func() string {
		if fnCalls.Add(1) == 1 {
			return validDeclarationExplanation
		}
		return workflowApproval
	})
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

	index, err := s.manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	tsFile, err := s.manager.IndexedFile("app.ts")
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := s.ExplainDeclaration(context.Background(), DeclarationExplanationRequest{
		ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, BaseFileHash: tsFile.ContentHash,
		TargetPath: "app.ts",
	})
	if err != nil || explanation.Anchor.Path != "app.ts" {
		t.Fatalf("TypeScript declaration explanation failed: %+v, %v", explanation, err)
	}

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

func TestChangeWorkflowRerunAfterPinnedTestsAndLargeFunctionScope(t *testing.T) {
	var createCalls, testCalls, reviewCalls atomic.Int32
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			if createCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return `{"explanation":"Add Helper with updated value.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 4 }\n"}]}`
		},
		func() string {
			if testCalls.Add(1) == 1 {
				return workflowTests
			}
			return `{"explanation":"Exercise updated Helper.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 4 }\n"},{"path":"new_test.go","content":"package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 4 { t.Fatal(\"wrong result\") } }\n"}]}`
		},
		func() string {
			if reviewCalls.Add(1) == 1 {
				return `{"verdict":"changes_requested","summary":"Return 4 instead of 2.","findings":["Update Helper to return 4."]}`
			}
			return workflowApproval
		},
	})
	padding := "// " + strings.Repeat("context ", 2400) + "\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"+padding+"func main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, s, "main.go", "new.go", "new_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	req := workflowRequest(session)
	req.Models = ChangeWorkflowModels{Create: "code", Test: "analyze", Review: "code"}
	// Route code profile to creator on stage 0 and reviewer on stage 2 by using catalog IDs.
	s.discoverAgyModels = func(context.Context, string) ([]llm.AgyModel, error) {
		return []llm.AgyModel{{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"}}, nil
	}
	catalog, err := s.AvailableModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req.Models = ChangeWorkflowModels{Create: catalog.Defaults["bug"], Test: catalog.Defaults["analyze"], Review: catalog.Defaults["code"]}
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatalf("first workflow start failed: %v", err)
	}
	first := finishedWorkflow(t, s, session.ID)
	if first.Workflow.Status != "changes_requested" || len(first.PinnedTests) != 1 {
		t.Fatalf("expected changes_requested with pinned test: %+v", first)
	}
	req.ChangeIdentity = changeIdentity(first)
	req.Message = "Add Helper returning four, addressing critic feedback."
	if _, err := s.StartChangeWorkflow(context.Background(), first.ID, req); err != nil {
		t.Fatalf("rerun workflow start failed: %v", err)
	}
	second := finishedWorkflow(t, s, first.ID)
	if second.Workflow.Status != "awaiting_human_review" || len(second.PinnedTests) != 1 {
		t.Fatalf("rerun workflow failed to complete: %+v", second.Workflow)
	}
	for _, msg := range second.Messages {
		if msg.Role == "user" && msg.Content == workflowTestPrompt {
			// Only the latest test stage prompt should remain after stripping prior test prompts.
			continue
		}
	}
	edited, err := s.SendChangeMessage(context.Background(), second.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(second),
		Changes: []ChangeEditInput{
			{Path: "new.go", Content: "package main\nfunc Helper() int { return 8 }\n"},
			{Path: "new_test.go", Content: "package main\nimport \"testing\"\nfunc TestHelper(t *testing.T) { if Helper() != 8 { t.Fatal(\"wrong result\") } }\n"},
		},
	})
	if err != nil || len(edited.PinnedTests) != 0 {
		t.Fatalf("expected manual test update after pinned tests to succeed and clear stale pin, got %+v, err=%v", edited, err)
	}
}

func TestChangeWorkflowPassingNewTestBaselineAndDetailedFailureReason(t *testing.T) {
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			return "```json\n" + `{"explanation":"Optimize Existing.","changes":[{"path":"existing.go","content":"package main\n// optimized\nfunc Existing() int { return 1 }\n"}]}` + "\n```"
		},
		func() string {
			return "```json\n" + `{"explanation":"Add unit test for Existing.","changes":[{"path":"existing_test.go","content":"package main\nimport \"testing\"\nfunc TestExisting(t *testing.T) { if Existing() != 1 { t.Fatal(\"bad\") } }\n"}]}` + "\n```"
		},
		func() string {
			return "```json\n" + workflowApproval + "\n```"
		},
	})
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("package main\nfunc Existing() int { return 1 }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	session := openChangeFixture(t, s, "existing.go", "existing_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	finished := finishedWorkflow(t, s, session.ID)
	if finished.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected new test passing against baseline to succeed, got %+v (checks: %+v)", finished.Workflow, finished.Checks)
	}

	var failCreateCalls atomic.Int32
	failS, _, failSession := workflowService(t, [3]func() string{
		func() string {
			if failCreateCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return workflowTests
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	if _, err := failS.StartChangeWorkflow(context.Background(), failSession.ID, workflowRequest(failSession)); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, failS, failSession.ID)
	if failed.Workflow.Status != "failed" || !strings.Contains(failed.Workflow.Reason, "--- FAIL: TestHelper") {
		t.Fatalf("expected detailed check failure reason, got %+v", failed.Workflow)
	}
	if len(failed.PinnedTests) != 0 {
		t.Fatalf("failed test stage should not pin broken tests: %+v", failed.PinnedTests)
	}
	history, err := failS.ChangeHistory(context.Background())
	if err != nil || len(history) == 0 || history[0].WorkflowReason != failed.Workflow.Reason {
		t.Fatalf("expected history entry to include workflow_reason, got %+v, err=%v", history, err)
	}

	repaired, err := failS.RepairChangeMessage(context.Background(), failed.ID, ChangeMessageRequest{
		ChangeIdentity: changeIdentity(failed),
		Repair:         true,
		Profile:        "bug",
	})
	if err != nil || len(repaired.Changes) != 2 {
		t.Fatalf("repair after workflow test failure failed: %+v, %v", repaired, err)
	}
	checked, err := failS.CheckChange(context.Background(), repaired.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(repaired),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil || !requiredChecksPassed(checked.Checks) || checked.Workflow.Stages[1].Status != "completed" || checked.Workflow.Stages[3].Status != "blocked" || checked.Workflow.Review != nil || len(checked.PinnedTests) != 1 {
		t.Fatalf("repaired workflow checks should complete test stage without fabricating critic review: %+v, err=%v", checked, err)
	}
	if _, err := failS.ReviewChange(context.Background(), checked.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(checked)}); err == nil {
		t.Fatalf("expected human review to remain blocked until critic review approves the proposal")
	}

	var rerunCreateCalls atomic.Int32
	rerunS, _, rerunSession := workflowService(t, [3]func() string{
		func() string {
			if rerunCreateCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	if _, err := rerunS.StartChangeWorkflow(context.Background(), rerunSession.ID, workflowRequest(rerunSession)); err != nil {
		t.Fatal(err)
	}
	failedFirst := finishedWorkflow(t, rerunS, rerunSession.ID)
	if failedFirst.Workflow.Status != "failed" {
		t.Fatalf("expected initial workflow test stage to fail, got %+v", failedFirst.Workflow)
	}
	rerunReq := workflowRequest(failedFirst)
	if _, err := rerunS.StartChangeWorkflow(context.Background(), failedFirst.ID, rerunReq); err != nil {
		t.Fatalf("rerun workflow after failed test stage failed: %v", err)
	}
	rerunDone := finishedWorkflow(t, rerunS, failedFirst.ID)
	if rerunDone.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected rerun workflow to recover after test failure, got %+v", rerunDone.Workflow)
	}
	foundFailureContext := false
	for _, msg := range rerunDone.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Previous check failures to resolve:") && strings.Contains(msg.Content, "wrong result") {
			foundFailureContext = true
		}
	}
	if !foundFailureContext {
		t.Fatalf("expected rerun create message to include failed test error output, got %+v", rerunDone.Messages)
	}
}

func TestChangeWorkflowAutonomousSelfHealingLoop(t *testing.T) {
	var createCalls atomic.Int32
	s, _, session := workflowService(t, [3]func() string{
		func() string {
			if createCalls.Add(1) == 1 {
				return workflowImplementation
			}
			return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	healed := finishedWorkflow(t, s, session.ID)
	if healed.Workflow.Status != "awaiting_human_review" || !healed.Workflow.AutoRepair || healed.Workflow.AutoRepairAttempts != 1 {
		t.Fatalf("expected autonomous self-healing loop to succeed in 1 auto-repair attempt, got %+v", healed.Workflow)
	}
	foundAutoRepairPrompt := false
	for _, msg := range healed.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "Previous check failures to resolve:") && strings.Contains(msg.Content, "Harness² Propose-Probe-Compose:") && strings.Contains(msg.Content, "wrong result") {
			foundAutoRepairPrompt = true
		}
	}
	if !foundAutoRepairPrompt {
		t.Fatalf("expected auto-repair Stage 0 prompt to include Harness² Probe and failed test output, got %+v", healed.Messages)
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

func TestChangeWorkflowAutoRepairStageOneTimeoutPreservesCheckEvidenceAndReconcilesStages(t *testing.T) {
	var createCalls atomic.Int32
	s, _, session := workflowService(t, [3]func() string{
		func() string {
			switch createCalls.Add(1) {
			case 1:
				return workflowImplementation
			case 2:
				return "invalid-json-causing-stage1-failure"
			default:
				return strings.ReplaceAll(workflowImplementation, "return 2", "return 99")
			}
		},
		func() string { return strings.ReplaceAll(workflowTests, "Helper() != 2", "Helper() != 99") },
		func() string { return workflowApproval },
	})
	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	failed := finishedWorkflow(t, s, session.ID)
	if failed.Workflow.Status != "failed" || failed.Workflow.Stages[0].Status != "failed" || failed.Workflow.Stages[1].Status != "skipped" {
		t.Fatalf("expected auto-repair Stage 0 failure to leave only Stage 0 failed, got %+v", failed.Workflow)
	}
	if len(failed.Checks) == 0 || requiredChecksPassed(failed.Checks) {
		t.Fatalf("expected failed checks from Attempt 0 to be preserved when Auto-Repair Stage 0 fails, got %+v", failed.Checks)
	}

	// If manual CheckChange runs after a Stage 0 failure, Stage 0 must not remain failed alongside Stage 1.
	checked, err := s.CheckChange(context.Background(), failed.ID, ChangeCheckRequest{
		ChangeIdentity: changeIdentity(failed),
		CheckOptions:   CheckOptions{RunTests: true},
	})
	if err != nil {
		t.Fatalf("CheckChange after Stage 0 failure: %v", err)
	}
	if checked.Workflow.Stages[0].Status == "failed" || checked.Workflow.Stages[0].Reason != "" || checked.Workflow.Stages[1].Status != "failed" {
		t.Fatalf("expected Stage 0 failure to clear when Stage 1 check failure is recorded, got %+v", checked.Workflow.Stages)
	}

	// Rerunning the workflow via StartChangeWorkflow must include the preserved check failure and recover cleanly.
	rerunReq := workflowRequest(checked)
	rerunReq.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), checked.ID, rerunReq); err != nil {
		t.Fatalf("rerun StartChangeWorkflow failed: %v", err)
	}
	recovered := finishedWorkflow(t, s, checked.ID)
	if recovered.Workflow.Status != "awaiting_human_review" {
		t.Fatalf("expected rerun workflow to reach awaiting_human_review, got %+v", recovered.Workflow)
	}
}

func TestChangeWorkflowNeverWidensScopeForUncapturedFailingTests(t *testing.T) {
	var testCalls atomic.Int32
	s, root, _ := workflowService(t, [3]func() string{
		func() string {
			return `{"explanation":"Validate nil password in SavePassword.","changes":[{"path":"cmd/master.go","content":"package cmd\nimport \"example.test/workflow/internal/utils\"\nfunc SavePassword(p []byte) bool {\n\tif len(p) == 0 {\n\t\treturn false\n\t}\n\treturn utils.NotEmpty(p)\n}\n"}]}`
		},
		func() string {
			if testCalls.Add(1) == 1 {
				return `{"explanation":"Add SavePassword test in master_test.go.","changes":[{"path":"cmd/master_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePassword(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"}]}`
			}
			return `{"explanation":"Update sibling add_test.go expectation alongside master_test.go.","changes":[{"path":"cmd/master_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePassword(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"},{"path":"cmd/add_test.go","content":"package cmd\nimport \"testing\"\nfunc TestSavePasswordWhenEmpty(t *testing.T) {\n\tif SavePassword(nil) {\n\t\tt.Fatal(\"expected false on nil\")\n\t}\n}\n"}]}`
		},
		func() string { return workflowApproval },
	})
	for rel, content := range map[string]string{
		"internal/utils/utils.go":      "package utils\nfunc NotEmpty(b []byte) bool { return len(b) > 0 }\n",
		"internal/utils/utils_test.go": "package utils\nimport \"testing\"\nfunc TestNotEmpty(t *testing.T) { if !NotEmpty([]byte(\"a\")) { t.Fatal(\"bad\") } }\n",
		"cmd/master.go":                "package cmd\nimport \"example.test/workflow/internal/utils\"\nfunc SavePassword(p []byte) bool {\n\tif p != nil && len(p) == 0 {\n\t\treturn false\n\t}\n\t_ = utils.NotEmpty(p)\n\treturn true\n}\n",
		"cmd/master_test.go":           "package cmd\nimport \"testing\"\n// BULKY_TEST_BODY_MARKER\nfunc TestSavePassword(t *testing.T) {\n\tif !SavePassword([]byte(\"secret123\")) {\n\t\tt.Fatal(\"expected true\")\n\t}\n}\n",
		"cmd/add_test.go":              "package cmd\nimport \"testing\"\nfunc TestSavePasswordWhenEmpty(t *testing.T) {\n\tif !SavePassword(nil) {\n\t\tt.Fatal(\"legacy test expected nil to succeed\")\n\t}\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	// Open session capturing ONLY cmd/master.go and cmd/master_test.go (omitting cmd/add_test.go).
	session := openChangeFixture(t, s, "cmd/master.go", "cmd/master_test.go")
	if _, err := s.TrustProjectExecution(context.Background(), session.ProjectRevision, true); err != nil {
		t.Fatal(err)
	}

	// Verify Stage 0 prompt context omits BULKY_TEST_BODY_MARKER and includes imported local Go declarations.
	stageZeroText, _, err := composeChangeStagePromptContext(root, session, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stageZeroText, "BULKY_TEST_BODY_MARKER") {
		t.Fatalf("expected Stage 0 workflow prompt to summarize test target declarations instead of full body, got:\n%s", stageZeroText)
	}
	if !strings.Contains(stageZeroText, "func NotEmpty(b []byte) bool") {
		t.Fatalf("expected Stage 0 workflow prompt to include local imported package declarations from internal/utils, got:\n%s", stageZeroText)
	}

	req := workflowRequest(session)
	req.AutoRepair = true
	if _, err := s.StartChangeWorkflow(context.Background(), session.ID, req); err != nil {
		t.Fatal(err)
	}
	// The repair cannot fix a test outside the task's files: the testing agent's
	// attempt to write it fails the run and names the file, and the task's
	// files never change on their own.
	blocked := finishedWorkflow(t, s, session.ID)
	if blocked.Workflow.Status != "failed" || changeTarget(blocked, "cmd/add_test.go") != nil || !strings.Contains(blocked.Workflow.Reason, "cmd/add_test.go") {
		t.Fatalf("an uncaptured failing test widened the scope or went unnamed: %+v", blocked.Workflow)
	}
	// A new conversation explicitly captures the named file. The user can
	// then pass the retained failure evidence to the newly admitted workflow.
	callsBefore := testCalls.Load()
	expanded := openChangeFixture(t, s, "cmd/master.go", "cmd/master_test.go", "cmd/add_test.go")
	approved := workflowRequest(expanded)
	approved.Message = workflowRetryMessage(req.Message, blocked.Workflow, blocked.Checks)
	if _, err := s.StartChangeWorkflow(context.Background(), expanded.ID, approved); err != nil {
		t.Fatal(err)
	}
	healed := finishedWorkflow(t, s, expanded.ID)
	if healed.Workflow.Status != "awaiting_human_review" || healed.Workflow.AutoRepairAttempts != 0 || testCalls.Load() != callsBefore+1 {
		t.Fatalf("explicitly expanded scope did not recover: %+v", healed.Workflow)
	}
	var testStageSawFailure bool
	for _, msg := range healed.Messages {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "Write meaningful regression and boundary tests") && strings.Contains(msg.Content, "failure to resolve:") && strings.Contains(msg.Content, "add_test.go") {
			testStageSawFailure = true
		}
	}
	if !testStageSawFailure {
		t.Fatalf("expected the Stage 1 testing agent prompt to carry the failure that named add_test.go, got messages=%+v", healed.Messages)
	}
}
