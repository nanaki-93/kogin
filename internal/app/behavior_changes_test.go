package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

// behaviorReview approves and lists one behavior change at Helper's line.
const behaviorReview = `{"verdict":"approve","summary":"Meets the criteria.","findings":[],"behavior_changes":[{"title":"Helper returns two","path":"new.go","quote":"func Helper() int { return 2 }","detail":"Before: no Helper. After: callers get 2. The task asked for it."}]}`

func TestReviewersListBehaviorChangesAndApprovalMustAcceptThem(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	s, _, session := workflowWithReviewer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request llm.ChatRequest
		_ = json.Unmarshal(body, &request)
		output := behaviorReview
		if strings.Contains(string(body), `"change_walkthrough"`) {
			output = fakeWalkthrough
		} else {
			mu.Lock()
			prompts = append(prompts, request.Messages[0].Content)
			mu.Unlock()
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
	})
	if _, err := s.StartChangeWorkflow(t.Context(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	reviewed := finishedWorkflow(t, s, session.ID)
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "behavior_changes") {
		t.Fatalf("reviewers were not asked for behavior changes: %v", prompts)
	}
	// Three reviewers reported the same change: it is listed once, at its line.
	changes := reviewed.Workflow.Review.BehaviorChanges
	if reviewed.Workflow.Status != "awaiting_human_review" || len(changes) != 1 || changes[0].Title != "Helper returns two" || changes[0].Path != "new.go" || changes[0].Line == 0 {
		t.Fatalf("behavior changes = %+v (workflow %s)", changes, reviewed.Workflow.Status)
	}
	request := ChangeReviewRequest{ChangeIdentity: changeIdentity(reviewed)}
	if _, err := s.ReviewChange(t.Context(), reviewed.ID, request); !errors.Is(err, ErrBehaviorChangesNotAccepted) {
		t.Fatalf("approval without accepting the behavior changes: %v", err)
	}
	request.AcceptBehaviorChanges = true
	approved, err := s.ReviewChange(t.Context(), reviewed.ID, request)
	if err != nil || approved.ReviewedHash != approved.Hash || len(approved.AcceptedBehaviorChanges) != 1 {
		t.Fatalf("approved = %+v, %v", approved, err)
	}
}

func TestAReviewWithoutBehaviorChangesStillReads(t *testing.T) {
	review, err := parseWorkflowReview(`{"verdict":"approve","summary":"Fine.","findings":[]}`, nil)
	if err != nil || review.BehaviorChanges != nil {
		t.Fatalf("review = %+v, %v", review, err)
	}
	if _, err := parseWorkflowReview(`{"verdict":"approve","summary":"Fine.","findings":[],"behavior_changes":[`+strings.Repeat(`{"title":"x"},`, 8)+`{"title":"x"}]}`, nil); err == nil {
		t.Fatal("a reviewer's ninth behavior change was accepted")
	}
}
