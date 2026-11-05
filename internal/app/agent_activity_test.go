package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestActivityTargetNamesWhatACallIsAbout(t *testing.T) {
	for _, tc := range []struct{ arguments, want string }{
		{`{"path":"internal/api/webui.go","start":1}`, "internal/api/webui.go"},
		{`{"pattern":"ReadFile("}`, "ReadFile("},
		{`{"command":["go","test","./..."]}`, "go test ./..."},
		{`{"reason":"done"}`, ""},
		{`not json`, ""},
	} {
		if got := activityTarget(json.RawMessage(tc.arguments)); got != tc.want {
			t.Errorf("activityTarget(%s) = %q, want %q", tc.arguments, got, tc.want)
		}
	}
	if got := activityTarget(json.RawMessage(`{"path":"` + strings.Repeat("a", 400) + `"}`)); len(got) > maxActivityTarget+4 {
		t.Fatalf("target not bounded: %d bytes", len(got))
	}
}

func TestChangeActivityKeepsTheLatestWorkflowsCalls(t *testing.T) {
	s := &Service{events: newEventBroker()}
	events, stop := s.SubscribeEvents()
	defer stop()
	id := "change-" + strings.Repeat("a", 32)
	session := &ChangeSession{ID: id, Workflow: &ChangeWorkflow{ID: "wf-1"}}

	read := s.startActivity(session, toolCoder, "read_file", json.RawMessage(`{"path":"main.go"}`))
	s.endActivity(session, read, nil)
	edit := s.startActivity(session, toolCoder, "edit_file", json.RawMessage(`{"path":"main.go"}`))
	s.endActivity(session, edit, errors.New("old text not found\nmore"))
	if first := <-events; first.Kind != "activity" || first.SessionID != id || first.Activity.Status != "running" || first.Activity.Target != "main.go" {
		t.Fatalf("first event = %+v", first)
	}

	log, err := s.ChangeActivity(id)
	if err != nil || log.WorkflowID != "wf-1" || len(log.Entries) != 2 {
		t.Fatalf("ChangeActivity = %+v, %v", log, err)
	}
	if log.Entries[0].Status != "done" || log.Entries[1].Status != "failed" || log.Entries[1].Detail != "old text not found" {
		t.Fatalf("entries = %+v", log.Entries)
	}

	// A new workflow starts a new log; a reader's questions are not logged.
	session.Workflow = &ChangeWorkflow{ID: "wf-2"}
	s.startActivity(session, toolTester, "list_files", json.RawMessage(`{}`))
	s.startActivity(session, toolReader, "read_file", json.RawMessage(`{"path":"x.go"}`))
	log, _ = s.ChangeActivity(id)
	if log.WorkflowID != "wf-2" || len(log.Entries) != 1 || log.Entries[0].Role != "tester" {
		t.Fatalf("after a new workflow = %+v", log)
	}
	if _, err := s.ChangeActivity("../x"); err == nil {
		t.Fatal("an invalid task id was accepted")
	}
}
