package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
)

func TestChangeFileReviewHTTPGuards(t *testing.T) {
	h := newChangeHandlerFixture(t, "http://127.0.0.1:1")
	index, _ := h.manager.Index()
	plan := app.ChangePlanInput{Problem: "Document behavior", Approach: "Add a concise guide"}
	// A task in this checkout, as tasks were before each got its own worktree:
	// the guards are the same.
	session, err := h.service.OpenChangeSession(context.Background(), app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Guide", Paths: []string{"README.md"}, AcceptanceCriteria: []string{"Explain Run"}, Plan: &plan})
	if err != nil || session.Plan == nil {
		t.Fatalf("open plan: %+v %v", session, err)
	}
	identity := app.ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision, Hash: session.Hash}
	proposal := workflowResponse(t, h.Message, "POST", "/changes/id/messages", session.ID, app.ChangeMessageRequest{ChangeIdentity: identity, Changes: []app.ChangeEditInput{{Path: "README.md", Content: "# Run\nA bounded operation.\n", Criteria: []int{1}}}})
	if proposal.Code != http.StatusOK || json.Unmarshal(proposal.Body.Bytes(), session) != nil {
		t.Fatalf("proposal: %d %s", proposal.Code, proposal.Body)
	}
	identity.Revision, identity.Hash = session.Revision, session.Hash
	for _, path := range []string{"main.go", "README.md"} {
		result := workflowResponse(t, h.ReviewFile, "POST", "/changes/id/review-file", session.ID, app.ChangeFileReviewRequest{ChangeIdentity: identity, Path: path})
		if path == "main.go" {
			if result.Code != http.StatusBadRequest {
				t.Fatalf("uncaptured file: %d", result.Code)
			}
			continue
		}
		if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), session) != nil || len(session.ReviewedFiles) != 1 || session.FileReviewHash != identity.Hash {
			t.Fatalf("file review: %d %s", result.Code, result.Body)
		}
	}
	for _, handler := range []http.HandlerFunc{h.ReviewFile} {
		request := httptest.NewRequest("POST", "/changes/id", strings.NewReader(`{"unexpected":"value"}`))
		request.SetPathValue("sessionID", session.ID)
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unknown field accepted: %d", response.Code)
		}
	}
}
