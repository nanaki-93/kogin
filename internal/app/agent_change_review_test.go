package app

import (
	"encoding/json"
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
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func TestWorkflowSpecialistRoutingAndEvidenceLabels(t *testing.T) {
	for _, test := range []struct {
		kind         string
		paths, roles []string
	}{
		{"feature", []string{"new.go", "new_test.go"}, []string{"correctness", "architecture", "test_quality"}},
		{"fix", []string{"new.ts", "new.test.ts"}, []string{"correctness", "test_quality"}},
		{"feature", []string{"a.ts", "b.ts", "new.test.ts"}, []string{"correctness", "architecture", "test_quality"}},
		{"security", []string{"new.go", "new_test.go"}, []string{"correctness", "architecture", "test_quality", "security"}},
		{"performance", []string{"new.go", "new_test.go"}, []string{"correctness", "architecture", "test_quality", "performance"}},
	} {
		session := &ChangeSession{Kind: test.kind}
		for _, path := range test.paths {
			session.Targets = append(session.Targets, ChangeTarget{Path: path})
		}
		if roles := workflowSpecialistSelection(session); !reflect.DeepEqual(roles, test.roles) {
			t.Fatalf("%s routing: %v", test.kind, roles)
		}
	}
	s, _, session := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval }})
	session.Kind = "performance"
	w := &ChangeWorkflow{Models: workflowRequest(session).Models, Specialists: s.prepareWorkflowSpecialists(session, "code")}
	if err := validateWorkflowSpecialists(w); err != nil || w.Specialists[3].Evidence != "unmeasured" {
		t.Fatalf("performance advice was promoted: %+v %v", w.Specialists, err)
	}
	w.Specialists[3].Evidence = "measured"
	if err := validateWorkflowSpecialists(w); err == nil {
		t.Fatal("forged measurement accepted")
	}
	w.Specialists[3].Evidence = "unmeasured"
	for i := range w.Specialists {
		w.Specialists[i].Status = "completed"
		w.Specialists[i].Review = &ChangeWorkflowReview{ProposalHash: "proposal", Verdict: "approve", Summary: "Reviewed", Findings: []ReviewItem{}}
	}
	w.Specialists[0].Review.Verdict = "changes_requested"
	w.Specialists[0].Review.Findings = []ReviewItem{{Title: "Handle zero input."}}
	w.Review = &ChangeWorkflowReview{ProposalHash: "proposal", Verdict: "approve", Summary: "Forged approval", Findings: []ReviewItem{}}
	if err := validateWorkflowSpecialists(w); err == nil {
		t.Fatal("aggregate approval contradicted a rejecting specialist")
	}
	w.Review = aggregateSpecialistReviews(w.Specialists, "proposal")
	if err := validateWorkflowSpecialists(w); err != nil {
		t.Fatal(err)
	}
	w.Specialists = nil
	if err := validateWorkflowSpecialists(w); err != nil || !specialistsComplete(w, "legacy") {
		t.Fatal("legacy workflow became unreadable")
	}
}

// Replacing only the fixture's review transport leaves creation and test
// generation intact and exercises normal admission, budgets and source guards.
func workflowWithReviewer(t *testing.T, handler http.HandlerFunc) (*Service, string, *ChangeSession) {
	t.Helper()
	s, root, session := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval }})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	profile := s.conf().runtimes.function.profile
	profile.APIBaseURL = server.URL
	s.conf().runtimes.function = newModelRuntime(profile, time.Minute, 0)
	return s, root, session
}

func TestWorkflowSpecialistsShareProposalAndJoinActionableFindings(t *testing.T) {
	entered := make(chan llm.ChatRequest, 3)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s, _, session := workflowWithReviewer(t, func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		entered <- request
		<-release
		output := workflowApproval
		if strings.Contains(request.Messages[0].Content, "architecture specialist") {
			output = `{"verdict":"changes_requested","summary":"Keep dependency direction.","findings":["Move the dependency into its owning package."]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
	})
	defer unblock()
	if _, err := s.StartChangeWorkflow(t.Context(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	var shared []llm.ChatMessage
	for range 3 {
		select {
		case request := <-entered:
			if shared == nil {
				shared = request.Messages[1:]
			} else if !reflect.DeepEqual(shared, request.Messages[1:]) {
				t.Fatal("specialists did not read the same captured proposal")
			}
			if strings.Contains(request.Messages[0].Content, "security specialist") {
				t.Fatal("security specialist dispatched without intent")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("reviewers did not enter concurrently")
		}
	}
	active, err := s.ChangeSession(t.Context(), session.ID)
	if err != nil || active.Workflow.Review != nil || len(active.Workflow.Specialists) != 3 {
		t.Fatalf("partial review became approval: %+v %v", active, err)
	}
	for _, report := range active.Workflow.Specialists {
		if report.Status != "running" || report.Model.Profile != "code" {
			t.Fatalf("missing admitted reviewer progress: %+v", report)
		}
	}
	unblock()
	result := finishedWorkflow(t, s, session.ID)
	if result.Workflow.Status != "changes_requested" || !specialistsComplete(result.Workflow, result.Hash) || len(result.Workflow.Review.Findings) != 1 {
		t.Fatalf("specialist verdict was lost: %+v", result.Workflow)
	}
	if _, err := s.ReviewChange(t.Context(), result.ID, ChangeReviewRequest{ChangeIdentity: changeIdentity(result)}); err == nil {
		t.Fatal("one approving specialist bypassed a rejecting reviewer")
	}
}

func TestWorkflowSpecialistResultsCannotOutliveSourceOrCancellation(t *testing.T) {
	for _, action := range []string{"source", "cancel", "trust"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{}, 3)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			s, root, session := workflowWithReviewer(t, func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
					_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: workflowApproval}}}})
				case <-r.Context().Done():
				}
			})
			defer unblock()
			request := workflowRequest(session)
			request.AutoRepair = true
			started, err := s.StartChangeWorkflow(t.Context(), session.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("reviewers did not start")
				}
			}
			if action == "source" {
				if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\nfunc External() {}\n"), 0644); err != nil {
					t.Fatal(err)
				}
			} else if action == "trust" {
				s.clearExecutionTrust()
			} else {
				if _, err := s.CancelChangeWorkflow(t.Context(), started.ID, ChangeWorkflowControl{ProjectID: started.ProjectID, ProjectRevision: started.ProjectRevision, WorkflowID: started.Workflow.ID}); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			result := finishedWorkflow(t, s, session.ID)
			if result.Workflow.Review != nil || result.Workflow.AutoRepairAttempts != 0 || result.ReviewedHash != "" {
				t.Fatalf("obsolete review published or repaired: %+v", result.Workflow)
			}
			if action == "cancel" && result.Workflow.Status != "canceled" {
				t.Fatalf("user cancellation mislabeled: %+v", result.Workflow)
			}
		})
	}
}

func TestWorkflowSpecialistsShareCallBudgetAndRepairCap(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			var calls atomic.Int32
			var reviewers atomic.Int32
			entered := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(entered) }) }
			defer unblock()
			s, _, session := workflowService(t, [3]func() string{
				func() string { calls.Add(1); return workflowImplementation },
				func() string { calls.Add(1); return workflowTests },
				func() string {
					calls.Add(1)
					if limited {
						if reviewers.Add(1) == 2 {
							unblock()
						}
						<-entered
					}
					return `{"verdict":"changes_requested","summary":"Missing boundary case.","findings":["Add a zero-input test."]}`
				},
			})
			if limited {
				s.conf().agentRuntime.Limits.ModelCalls = 4
				s.conf().agentRuntime.Limits.ConcurrentCalls = 2
			}
			request := workflowRequest(session)
			request.AutoRepair = true
			if _, err := s.StartChangeWorkflow(t.Context(), session.ID, request); err != nil {
				t.Fatal(err)
			}
			result := finishedWorkflow(t, s, session.ID)
			if limited {
				if calls.Load() != 4 || result.Workflow.Status != "failed" || result.Workflow.AutoRepairAttempts != 0 || result.Workflow.Review != nil {
					t.Fatalf("shared call budget failed: calls=%d %+v", calls.Load(), result.Workflow)
				}
			} else if calls.Load() != 15 || result.Workflow.Status != "changes_requested" || result.Workflow.AutoRepairAttempts != 2 {
				t.Fatalf("repair cap failed: calls=%d %+v", calls.Load(), result.Workflow)
			}
		})
	}
}
