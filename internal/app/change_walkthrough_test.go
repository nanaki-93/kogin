package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

func walkthroughSession() *ChangeSession {
	return &ChangeSession{Kind: "fix", Title: "Persist the salt", Hash: "h1", AcceptanceCriteria: []string{"The salt survives a restart"},
		Targets: []ChangeTarget{{Path: "cmd/master.go", Content: "package cmd\n"}, {Path: "cmd/old.go", Content: "package cmd\n"}},
		Changes: []ChangeEdit{
			{Path: "cmd/master.go", Content: "package cmd\n\nfunc generateKey() {\n\tsalt, err := readKeySalt()\n\t_ = err\n}\n\nfunc readKeySalt() ([]byte, error) { return nil, nil }\n"},
			{Path: "cmd/old.go", Status: "deleted"},
		}}
}

func TestAWalkthroughPlacesEachNoteAtItsLineInTheChangedFiles(t *testing.T) {
	session := walkthroughSession()
	reply := `{"files":[
	 {"path":"./cmd/master.go","summary":"Loads the salt from .pwd-salt instead of a fresh one.","notes":[
	  {"quote":"func readKeySalt() ([]byte, error) { return nil, nil }","what":"Adds ` + "`readKeySalt`" + `","why":"One place reads the salt file."},
	  {"quote":"\tsalt, err := readKeySalt()","what":"` + "`generateKey`" + ` reads the saved salt","why":"The key must be the same after a restart."},
	  {"quote":"not in the file","what":"Something unplaced","why":""}]},
	 {"path":"cmd/old.go","summary":"Removed: its helper moved.","notes":[]},
	 {"path":"internal/elsewhere.go","summary":"Not part of the change.","notes":[]}]}`
	files, err := parseWalkthrough(reply, session)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "cmd/master.go" || files[1].Path != "cmd/old.go" {
		t.Fatalf("files = %+v", files)
	}
	notes := files[0].Notes
	if len(notes) != 3 || notes[0].Line != 4 || notes[1].Line != 8 || notes[2].Line != 0 || notes[0].What != "`generateKey` reads the saved salt" {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestAWalkthroughThatFailsSaysWhyAndLeavesTheReviewAlone(t *testing.T) {
	session := walkthroughSession()
	failed := explainProposal(session, func([]llm.ChatMessage, llm.JSONSchema) (modelOutput, error) {
		return modelOutput{}, errors.New("provider timed out. Retry later")
	})
	if failed.Status != WalkthroughFailed || failed.Reason != "provider timed out." || failed.ProposalHash != "h1" {
		t.Fatalf("walkthrough = %+v", failed)
	}
	var prompt []llm.ChatMessage
	ready := explainProposal(session, func(messages []llm.ChatMessage, schema llm.JSONSchema) (modelOutput, error) {
		prompt = messages
		return modelOutput{Content: `{"files":[{"path":"cmd/master.go","summary":"Reads the saved salt.","notes":[]}]}`}, nil
	})
	if ready.Status != WalkthroughReady || len(ready.Files) != 1 || !strings.Contains(prompt[1].Content, "The salt survives a restart") || !strings.Contains(prompt[0].Content, "Do not judge the change") {
		t.Fatalf("walkthrough = %+v", ready)
	}
	if _, err := parseWalkthrough(`{"files":[{"path":"nowhere.go","summary":"x","notes":[]}]}`, session); err == nil {
		t.Fatal("a walkthrough of no changed file was accepted")
	}
}

func TestTheReviewStageExplainsTheProposalAndAReviewerCanAskAgain(t *testing.T) {
	var explained atomic.Int32
	s, _, session := workflowWithReviewer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		output := workflowApproval
		if strings.Contains(string(body), `"change_walkthrough"`) {
			explained.Add(1)
			output = `{"files":[{"path":"new.go","summary":"Adds ` + "`Helper`" + `.","notes":[{"quote":"func Helper() int { return 2 }","what":"Adds ` + "`Helper`" + ` returning 2","why":"The task asks for it."}]},{"path":"new_test.go","summary":"Pins ` + "`Helper`" + `.","notes":[]}]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{Content: output}}}})
	})
	if _, err := s.StartChangeWorkflow(t.Context(), session.ID, workflowRequest(session)); err != nil {
		t.Fatal(err)
	}
	reviewed := finishedWorkflow(t, s, session.ID)
	w := reviewed.Walkthrough
	if reviewed.Workflow.Status != "awaiting_human_review" || w == nil || w.Status != WalkthroughReady || w.ProposalHash != reviewed.Hash || len(w.Files) != 2 || w.Files[0].Path != "new.go" || w.Files[0].Notes[0].Line != 3 { // gofmt adds a blank line after the package clause
		t.Fatalf("walkthrough = %+v (workflow %s)", w, reviewed.Workflow.Status)
	}
	again, err := s.ExplainChange(t.Context(), reviewed.ID, ChangeWalkthroughRequest{ChangeIdentity: changeIdentity(reviewed)})
	if err != nil || again.Walkthrough == nil || again.Walkthrough.Status != WalkthroughReady || explained.Load() != 2 || again.Hash != reviewed.Hash || again.Revision != reviewed.Revision {
		t.Fatalf("explained again = %+v, %v (calls %d)", again, err, explained.Load())
	}
	stale := changeIdentity(reviewed)
	stale.Hash = "other"
	if _, err := s.ExplainChange(t.Context(), reviewed.ID, ChangeWalkthroughRequest{ChangeIdentity: stale}); err == nil || explained.Load() != 2 {
		t.Fatal("a walkthrough of a version the reviewer did not see was written")
	}
}
