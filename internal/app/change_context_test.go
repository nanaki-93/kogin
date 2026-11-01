package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func TestGuidedChangeRequestsSolutionWithoutRepeatingCause(t *testing.T) {
	for _, kind := range []string{"fix", "performance", "security", "feature"} {
		t.Run(kind, func(t *testing.T) {
			requests := make(chan llm.ChatRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request llm.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode model request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- request
				_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{
					Content: `{"explanation":"Document the return value. Runtime behavior still needs verification.","changes":[{"path":"README.md","content":"# Behavior\nReturns the result.\n"}]}`,
				}}}})
			}))
			t.Cleanup(server.Close)
			service, _ := newSemanticAnalysisService(t, server.URL, 0)
			index, err := service.manager.Index()
			if err != nil {
				t.Fatal(err)
			}
			session, err := service.OpenChangeSession(context.Background(), ChangeCreateRequest{
				ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision,
				Kind: kind, Title: "Document the return value", Paths: []string{"README.md"},
			})
			if err != nil {
				t.Fatal(err)
			}
			proposal, err := service.SendChangeMessage(context.Background(), session.ID, ChangeMessageRequest{
				ChangeIdentity: changeIdentity(session), Message: "Cause: the return value is undocumented. Document the behavior.",
			})
			if err != nil {
				t.Fatal(err)
			}
			request := <-requests
			input := request.Messages[len(request.Messages)-1].Content
			if !strings.Contains(input, "Cause: the return value is undocumented.") {
				t.Fatal("original finding evidence was removed from model context")
			}
			for _, guidance := range []string{"Describe only the correction", "Do not repeat the cause", "Include unresolved failures, limitations and uncertainty"} {
				if strings.Contains(request.Messages[0].Content, guidance) != (kind != "feature") {
					t.Fatalf("incorrect explanation guidance for %s: %s", kind, guidance)
				}
			}
			if got := proposal.Messages[len(proposal.Messages)-1].Content; got != "Document the return value. Runtime behavior still needs verification." {
				t.Fatalf("solution or limitation changed: %q", got)
			}
		})
	}
}

func TestChangeProposalRejectionNamesThePath(t *testing.T) {
	session := &ChangeSession{
		Targets: []ChangeTarget{{Path: "cmd/master_test.go", Exists: true, Content: "package cmd\n"}},
		Plan:    &ChangePlan{ReadTargets: []ChangeTarget{{Path: "cmd/master.go", Exists: true, Content: "package cmd\n"}}},
	}
	for _, test := range []struct {
		name, path, content, want string
	}{
		{"new file outside scope", "cmd/util_test.go", "package cmd\n", `proposal wrote "cmd/util_test.go", outside the task's files (a new file must be in the task's scope)`},
		{"read-only context", "./cmd/master.go", "package cmd\n", `proposal wrote "./cmd/master.go", which the task only reads; only the task's files can change`},
		{"escaping path", "../cmd/master_test.go", "package cmd\n", `proposal wrote "../cmd/master_test.go", outside the task's files`},
		{"deletion", "cmd/master_test.go", "", "proposal emptied cmd/master_test.go; deleting files is not supported"},
		{"binary content", "cmd/master_test.go", "package cmd\x00\n", "proposal for cmd/master_test.go is not UTF-8 text"},
		{"control characters", "cmd/\nevil.go", "package cmd\n", `proposal wrote "cmd/\nevil.go"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildChangeEdits(session, []ChangeEditInput{{Path: test.path, Content: test.content}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	long := strings.Repeat("é", 200) + ".go"
	_, err := buildChangeEdits(session, []ChangeEditInput{{Path: long, Content: "package cmd\n"}})
	if err == nil || len(err.Error()) > 220 || !strings.Contains(err.Error(), "…") {
		t.Fatalf("long path was not bounded: %v", err)
	}
}

func TestChangeProposalAcceptsCleanedCapturedPaths(t *testing.T) {
	session := &ChangeSession{Targets: []ChangeTarget{{Path: "cmd/util_test.go"}}}
	edits, err := buildChangeEdits(session, []ChangeEditInput{{Path: "./cmd/util_test.go", Content: "package cmd\n"}})
	if err != nil || len(edits) != 1 || edits[0].Path != "cmd/util_test.go" {
		t.Fatalf("cleaned captured path was not accepted as the target: %+v, %v", edits, err)
	}
	_, err = buildChangeEdits(session, []ChangeEditInput{{Path: "cmd/util_test.go", Content: "package cmd\n"}, {Path: "cmd//util_test.go", Content: "package cmd\n\n"}})
	if err == nil || err.Error() != "proposal repeated cmd/util_test.go" {
		t.Fatalf("two spellings of one target were accepted: %v", err)
	}
}

func TestChangePromptListsTheWritablePaths(t *testing.T) {
	session := &ChangeSession{Kind: "fix", Targets: []ChangeTarget{
		{Path: "cmd/master_test.go", Exists: true, Content: "package cmd\n"},
		{Path: "cmd/util_test.go"},
	}}
	messages, err := changeStageMessages(session, "Move the helpers into util_test.go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := "Write only these exact paths: cmd/master_test.go, cmd/util_test.go (new file). Any other path, including a new helper file, rejects the whole proposal"
	if !strings.Contains(messages[0].Content, want) {
		t.Fatalf("system prompt does not list the writable paths:\n%s", messages[0].Content)
	}
}

// source returns Go-like text of about n bytes, with the quotes and newlines
// that JSON escaping grows.
func source(marker string, n int) string {
	line := "\tfmt.Println(\"" + marker + "\")\n"
	return strings.Repeat(line, max(1, n/len(line)))
}

func TestAChangedFileIsInTheStagePromptOnce(t *testing.T) {
	session := &ChangeSession{
		Kind: "fix",
		Targets: []ChangeTarget{
			{Path: "a.go", Content: source("ORIGINAL-A", 400), Exists: true},
			{Path: "b.go", Content: source("ORIGINAL-B", 400), Exists: true},
			{Path: "c.go", Content: source("ORIGINAL-C", 400), Exists: true},
		},
		Changes: []ChangeEdit{
			{Path: "a.go", Content: source("PROPOSED-A", 400), Status: "modified"},
			{Path: "c.go", Status: "deleted"},
		},
	}
	session.Hash = changeProposalHash(session.Changes)
	text, _, err := composeChangePromptContext(t.TempDir(), session)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := changeMessages(session, "Review the proposal", text)
	if err != nil {
		t.Fatal(err)
	}
	prompt := messages[1].Content
	if strings.Contains(prompt, "ORIGINAL-A") || !strings.Contains(prompt, "PROPOSED-A") || !strings.Contains(prompt, "a.go: the current proposal changes it") {
		t.Fatal("a changed file is not carried once, as its proposal")
	}
	if !strings.Contains(prompt, "ORIGINAL-B") || !strings.Contains(prompt, "ORIGINAL-C") {
		t.Fatal("an unchanged or deleted file lost its captured content")
	}
}

func TestTheConversationAStageCarriesIsBounded(t *testing.T) {
	short := []ChangeMessage{{Role: "user", Content: "Fix it"}, {Role: "assistant", Content: "Fixed"}}
	if got := promptConversation(short); len(got) != 2 || got[1].Content != "Fixed" {
		t.Fatalf("a short conversation changed: %+v", got)
	}
	if got := promptConversation(nil); got == nil || len(got) != 0 {
		t.Fatalf("no conversation = %#v", got)
	}
	long := []ChangeMessage{{Role: "user", Content: strings.Repeat("t", 20*1024)}}
	for i := range 30 {
		long = append(long, ChangeMessage{Role: []string{"user", "assistant"}[i%2], Content: fmt.Sprintf("%02d", i) + strings.Repeat("a", 5*1024)})
	}
	long = append(long, ChangeMessage{Role: "assistant", Content: "last" + strings.Repeat("b", 10*1024)})
	got := promptConversation(long)
	total := 0
	for _, message := range got {
		total += len(message.Content)
	}
	if total > maxPromptConversationBytes+64 {
		t.Fatalf("conversation of %d bytes", total)
	}
	if !strings.HasPrefix(got[0].Content, "ttt") || !strings.HasSuffix(got[0].Content, "[clipped]") || len(got[0].Content) > maxPromptTaskBytes+16 {
		t.Fatal("the task as first asked is not kept, clipped")
	}
	last := got[len(got)-1]
	if !strings.HasPrefix(last.Content, "last") || len(last.Content) > maxPromptMessageBytes+16 {
		t.Fatalf("the newest message is not kept, clipped: %d bytes", len(last.Content))
	}
	if got[1].Role != "note" || !strings.Contains(got[1].Content, "earlier messages are left out") {
		t.Fatalf("no note for the messages left out: %+v", got[1])
	}
	if !strings.HasPrefix(got[2].Content, "2") {
		t.Fatalf("the kept messages are not the newest, in order: %.4s", got[2].Content)
	}
}

// The sizes of a task that fixed 8 issues of one file and failed its review
// with "conversation context exceeds 256 KiB": its files, its proposal, its
// conversation and the review's diff.
func TestAGroupedFixTaskFitsItsReview(t *testing.T) {
	session := &ChangeSession{Kind: "fix"}
	for i, size := range []int{6685, 28765, 15803, 13096, 18916} {
		session.Targets = append(session.Targets, ChangeTarget{Path: fmt.Sprintf("f%d.go", i), Content: source("before", size), Exists: true})
	}
	session.Targets = append(session.Targets, ChangeTarget{Path: "f5_test.go"}, ChangeTarget{Path: "f6_test.go"})
	for i, size := range []int{15812, 4906, 23385, 20796, 18916, 11871, 9005} {
		session.Changes = append(session.Changes, ChangeEdit{Path: fmt.Sprintf("f%d.go", i), Content: source("after", size), Status: "modified"})
	}
	session.Changes[5].Path, session.Changes[6].Path = "f5_test.go", "f6_test.go"
	for i, size := range []int{6520, 7056, 8191, 5460, 4282, 5078} {
		session.Messages = append(session.Messages, ChangeMessage{Role: []string{"user", "assistant"}[i%2], Content: source("message", size)})
	}
	session.Hash = changeProposalHash(session.Changes)
	text, _, err := composeChangePromptContext(t.TempDir(), session)
	if err != nil {
		t.Fatal(err)
	}
	review := "Review the complete checked proposal:\n" + strings.Repeat("c", 4096) + "\n" + source("diff", maxReviewDiffBytes)
	if _, err := changeMessages(session, review, text); err != nil {
		t.Fatalf("the review of a grouped fix does not fit: %v", err)
	}
}
