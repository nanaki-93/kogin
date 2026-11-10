package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestChangeFileReviewHTTPGuards(t *testing.T) {
	plan := app.ChangePlanInput{Problem: "Document behavior", Approach: "Add a concise guide"}
	h, session := openHandlerWorktreeTask(t, "http://127.0.0.1:1", func(index *project.ProjectIndex) app.ChangeCreateRequest {
		return app.ChangeCreateRequest{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Kind: "feature", Title: "Guide", Paths: []string{"README.md"}, AcceptanceCriteria: []string{"Explain Run"}, Plan: &plan}
	})
	if session.Plan == nil {
		t.Fatalf("open plan: %+v", session)
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

func TestIntentOnlyTaskHTTPAdmission(t *testing.T) {
	h := gitChangeHandlerFixture(t)
	index, _ := h.manager.Index()
	request := app.ChangeCreateRequest{
		ProjectID:          index.ProjectID,
		ProjectRevision:    index.ProjectRevision,
		Kind:               "feature",
		Title:              "Add clipboard flag",
		Paths:              []string{},
		AcceptanceCriteria: []string{"Copies selected secret when --copy is passed"},
	}
	response := workflowResponse(t, h.Open, "POST", "/changes", "", app.ChangeOpenRequest{ChangeCreateRequest: request, ConfirmExecution: true})
	var opened openedTask
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &opened) != nil || len(opened.Targets) != 0 || opened.WorktreePath == "" {
		t.Fatalf("intent-only open: %d %s", response.Code, response.Body)
	}

	instructions := request
	instructions.Kind = "instructions"
	if rejected := workflowResponse(t, h.Open, "POST", "/changes", "", app.ChangeOpenRequest{ChangeCreateRequest: instructions, ConfirmExecution: true}); rejected.Code != http.StatusBadRequest {
		t.Fatalf("instructions task without paths: %d %s", rejected.Code, rejected.Body)
	}
}
