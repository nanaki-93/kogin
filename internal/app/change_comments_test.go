package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

const revisedChange = `{"explanation":"Return three.","changes":[{"path":"new.go","content":"package main\nfunc Helper() int { return 3 }\n"}]}`

// recordingProvider answers with responses in turn and keeps the last user
// message of every request.
func recordingProvider(t *testing.T, responses ...string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		mu.Lock()
		seen = append(seen, request.Messages[len(request.Messages)-1].Content)
		response := responses[min(len(seen), len(responses))-1]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: response}}}})
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func commentedProposal(t *testing.T) (*Service, *ChangeSession, func() []string) {
	t.Helper()
	server, seen := recordingProvider(t, workflowImplementation, revisedChange)
	service, _ := newSemanticAnalysisService(t, server.URL, 0)
	session := prepareChangeFixture(t, service, "new.go")
	return service, session, seen
}

func comment(session *ChangeSession, line int, text string) ChangeCommentRequest {
	return ChangeCommentRequest{ChangeIdentity: changeIdentity(session), Path: "new.go", Side: "new", Line: line, Text: text}
}

func TestLineCommentsAnchorToTheCurrentProposal(t *testing.T) {
	service, session, _ := commentedProposal(t)
	ctx := context.Background()
	for name, request := range map[string]ChangeCommentRequest{
		"empty":          comment(session, 2, "  "),
		"long":           comment(session, 2, strings.Repeat("x", maxCommentText+1)),
		"no line":        comment(session, 9, "Missing line."),
		"old side":       {ChangeIdentity: changeIdentity(session), Path: "new.go", Side: "old", Line: 2, Text: "Not removed."},
		"other file":     {ChangeIdentity: changeIdentity(session), Path: "main.go", Side: "new", Line: 1, Text: "Unchanged file."},
		"wrong side":     {ChangeIdentity: changeIdentity(session), Path: "new.go", Side: "both", Line: 2, Text: "Bad side."},
		"stale proposal": {ChangeIdentity: ChangeIdentity{ProjectID: session.ProjectID, ProjectRevision: session.ProjectRevision, Revision: session.Revision - 1, Hash: session.Hash}, Path: "new.go", Side: "new", Line: 2, Text: "Old."},
	} {
		if _, err := service.CommentChangeLine(ctx, session.ID, request); err == nil {
			t.Fatalf("%s: comment accepted", name)
		}
	}
	commented, err := service.CommentChangeLine(ctx, session.ID, comment(session, 3, "Return 3 here."))
	if err != nil {
		t.Fatal(err)
	}
	if len(commented.Comments) != 1 || commented.Comments[0].Excerpt != "func Helper() int { return 2 }" || commented.Comments[0].Hash != session.Hash || commented.Revision != session.Revision {
		t.Fatalf("comment not anchored or changed the proposal: %+v", commented.Comments)
	}
	deleted, err := service.DeleteChangeComment(ctx, session.ID, ChangeCommentDeleteRequest{ChangeIdentity: changeIdentity(commented), ID: commented.Comments[0].ID})
	if err != nil || len(deleted.Comments) != 0 {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
}

func TestLineCommentsBecomeARepairBrief(t *testing.T) {
	service, session, seen := commentedProposal(t)
	ctx := context.Background()
	if _, err := service.SendChangeMessage(ctx, session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(session), IncludeComments: true}); err == nil {
		t.Fatal("an empty brief was sent")
	}
	commented, err := service.CommentChangeLine(ctx, session.ID, comment(session, 3, "Return 3 here.\nKeep the signature."))
	if err != nil {
		t.Fatal(err)
	}
	revised, err := service.SendChangeMessage(ctx, session.ID, ChangeMessageRequest{ChangeIdentity: changeIdentity(commented), Message: "Apply the review.", IncludeComments: true})
	if err != nil {
		t.Fatal(err)
	}
	if requests := seen(); !strings.Contains(requests[len(requests)-1], "Keep the signature.") {
		t.Fatal("the brief did not reach the provider")
	}
	brief := revised.Messages[len(revised.Messages)-2].Content
	for _, want := range []string{"Apply the review.", repairBriefHeading, "1. new.go:3 `func Helper() int { return 2 }`", "   Return 3 here.\n   Keep the signature."} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	if len(revised.Comments) != 1 || revised.Comments[0].SentRevision != commented.Revision || openComments(revised.Comments) != 0 {
		t.Fatalf("sent comment not recorded: %+v", revised.Comments)
	}
	if _, err := service.DeleteChangeComment(ctx, session.ID, ChangeCommentDeleteRequest{ChangeIdentity: changeIdentity(revised), ID: revised.Comments[0].ID}); err == nil {
		t.Fatal("a sent comment was deleted")
	}
}

func TestWorkflowRunCarriesTheRepairBrief(t *testing.T) {
	s, _, initial := workflowService(t, [3]func() string{func() string { return workflowImplementation }, func() string { return workflowTests }, func() string { return workflowApproval }})
	if _, err := s.StartChangeWorkflow(t.Context(), initial.ID, workflowRequest(initial)); err != nil {
		t.Fatal(err)
	}
	ready := finishedWorkflow(t, s, initial.ID)
	commented, err := s.CommentChangeLine(t.Context(), ready.ID, ChangeCommentRequest{ChangeIdentity: identityForChange(ready), Path: "new.go", Side: "new", Line: 3, Text: "Name the constant."})
	if err != nil {
		t.Fatal(err)
	}
	request := workflowRequest(commented)
	request.Message, request.IncludeComments = "", true
	started, err := s.StartChangeWorkflow(t.Context(), ready.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if openComments(started.Comments) != 0 || started.Comments[0].SentRevision != commented.Revision {
		t.Fatalf("workflow did not send the comments: %+v", started.Comments)
	}
	last := started.Messages[len(started.Messages)-1]
	finished := finishedWorkflow(t, s, ready.ID)
	found := strings.Contains(last.Content, "Name the constant.")
	for _, message := range finished.Messages {
		found = found || message.Role == "user" && strings.Contains(message.Content, "Name the constant.")
	}
	if !found {
		t.Fatal("the brief did not reach the workflow conversation")
	}
}

func TestStoredCommentsMustBeBoundedAndAnchored(t *testing.T) {
	valid := ChangeComment{ID: "c1", Path: "a.go", Side: "new", Line: 1, Text: "Fix.", Hash: "h"}
	for name, change := range map[string]func(*ChangeComment){
		"side":     func(c *ChangeComment) { c.Side = "both" },
		"line":     func(c *ChangeComment) { c.Line = 0 },
		"text":     func(c *ChangeComment) { c.Text = "" },
		"hash":     func(c *ChangeComment) { c.Hash = "" },
		"revision": func(c *ChangeComment) { c.SentRevision = 9 },
	} {
		broken := valid
		change(&broken)
		if err := validateStoredComments(&ChangeSession{Revision: 1, Comments: []ChangeComment{broken}}); err == nil {
			t.Fatalf("%s: corrupt comment accepted", name)
		}
	}
	if err := validateStoredComments(&ChangeSession{Revision: 1, Comments: []ChangeComment{valid, valid}}); err == nil {
		t.Fatal("duplicate comment IDs accepted")
	}
}
