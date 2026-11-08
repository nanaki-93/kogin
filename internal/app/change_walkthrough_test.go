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
	  {"quote":"func readKeySalt() ([]byte, error) { return nil, nil }","goal":0,"problem":"` + "`generateKey`" + ` needs one place that reads the salt file.","how":"Adds ` + "`readKeySalt`" + `."},
	  {"quote":"\tsalt, err := readKeySalt()","goal":1,"problem":"Each run derived a new key, so the vault could not be opened again.","how":"` + "`generateKey`" + ` reads the saved salt."},
	  {"quote":"not in the file","goal":7,"problem":"Something unplaced","how":""}]},
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
	// In line order, each with the goal it serves; a goal the task does not
	// have is no goal.
	if len(notes) != 3 || notes[0].Line != 4 || notes[0].Goal != 1 || notes[0].How != "`generateKey` reads the saved salt." || notes[1].Line != 8 || notes[1].Goal != 0 || notes[2].Line != 0 || notes[2].Goal != 0 {
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
			output = `{"files":[{"path":"new.go","summary":"Adds ` + "`Helper`" + `.","notes":[{"quote":"func Helper() int { return 2 }","goal":1,"problem":"Nothing returned two.","how":"Adds ` + "`Helper`" + `."}]},{"path":"new_test.go","summary":"Pins ` + "`Helper`" + `.","notes":[]}]}`
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

func TestAWalkthroughAsksWhatEachHunkFixesAgainstTheTasksIssues(t *testing.T) {
	session := walkthroughSession()
	session.Issues = []ChangeIssue{
		{Title: "Salt discarded", Detail: "(line 4, in generateKey)\nProblem: `generateKey` drops its salt, so the key cannot be derived again.\nFix: Save it.", Status: IssueFixed},
		{Title: "Exit status 0 on mismatch", Detail: "(line 9)\nProblem: A wrong password exits 0.", Status: IssueDoesNotHold},
	}
	prompt := walkthroughMessages(session)
	for _, want := range []string{"1. Salt discarded: `generateKey` drops its salt, so the key cannot be derived again.", "2. Exit status 0 on mismatch: A wrong password exits 0. (does not hold; not part of the change)", "what am I fixing here"} {
		if !strings.Contains(prompt[0].Content+prompt[1].Content, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt[1].Content)
		}
	}
	// A task without issues names its criteria and the request as asked.
	session.Issues = nil
	session.Messages = []ChangeMessage{{Role: "user", Content: "Keep the salt across runs."}}
	if prompt := walkthroughMessages(session)[1].Content; !strings.Contains(prompt, "As asked:\nKeep the salt across runs.") || !strings.Contains(prompt, "1. The salt survives a restart") {
		t.Fatalf("prompt = %s", prompt)
	}
}

func TestWalkthroughsWrittenBeforeGoalsStillRead(t *testing.T) {
	var note WalkthroughNote
	if err := json.Unmarshal([]byte(`{"line":3,"what":"Adds `+"`Helper`"+`.","why":"The task asks for it."}`), &note); err != nil || note.Problem != "The task asks for it." || note.How != "Adds `Helper`." || note.Line != 3 {
		t.Fatalf("note = %+v, %v", note, err)
	}
}

func TestWalkthroughStepsBecomeOneGuidedSequenceAcrossFiles(t *testing.T) {
	session := walkthroughSession()
	session.Changes = append(session.Changes, ChangeEdit{Path: "cmd/master_test.go", Content: "package cmd\n\nfunc TestGenerateKey(t *T) {}\n"})
	// The model numbered steps 2, 5, 5, 9 across two files: they become
	// 1..3 in that order, the repeated 5 losing its place.
	reply := `{"files":[
	 {"path":"cmd/master.go","summary":"Loads the saved salt.","notes":[
	  {"quote":"\tsalt, err := readKeySalt()","goal":1,"problem":"Each run derived a new key.","how":"Reads the saved salt.","kind":"fix","step":2},
	  {"quote":"func readKeySalt() ([]byte, error) { return nil, nil }","goal":0,"problem":"The fix needs one reader.","how":"Adds the helper.","kind":"support","step":5}]},
	 {"path":"cmd/master_test.go","summary":"Pins the salt.","notes":[
	  {"quote":"func TestGenerateKey(t *T) {}","goal":1,"problem":"Nothing pinned the salt surviving a restart.","how":"TestGenerateKey.","kind":"test","step":9},
	  {"quote":"package cmd","goal":0,"problem":"A second note with a taken step.","how":"","kind":"wrong","step":5}]}]}`
	files, err := parseWalkthrough(reply, session)
	if err != nil {
		t.Fatal(err)
	}
	first, second := files[0].Notes, files[1].Notes
	if first[0].Step != 1 || first[0].Kind != "fix" || first[1].Step != 2 || first[1].Kind != "support" {
		t.Fatalf("master notes = %+v", first)
	}
	// Notes are in line order within the file; the test's step follows the fix's.
	var test, repeated WalkthroughNote
	for _, note := range second {
		if note.Kind == "test" {
			test = note
		} else {
			repeated = note
		}
	}
	if test.Step != 3 {
		t.Fatalf("test note = %+v", second)
	}
	// The repeated step fell out of the sequence, and an unknown kind is dropped.
	if repeated.Step != 0 || repeated.Kind != "" {
		t.Fatalf("repeated-step note = %+v", repeated)
	}
}
