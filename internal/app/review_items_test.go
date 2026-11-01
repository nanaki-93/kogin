package app

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReviewsStoredWithTextFindingsStillRead(t *testing.T) {
	stored := `{"proposal_hash":"p","verdict":"changes_requested","summary":"No.","findings":["The salt is dropped before key derivation. It must be stored with the vault."],"notes":["Optional."]}`
	var review ChangeWorkflowReview
	if err := json.Unmarshal([]byte(stored), &review); err != nil {
		t.Fatal(err)
	}
	finding := review.Findings[0]
	if finding.Title != "The salt is dropped before key derivation." || !strings.HasSuffix(finding.Detail, "stored with the vault.") || review.Notes[0] != (ReviewItem{Title: "Optional."}) {
		t.Fatalf("stored text findings = %+v %+v", review.Findings, review.Notes)
	}
	if err := validateWorkflowReview(&review); err != nil {
		t.Fatalf("a stored review no longer validates: %v", err)
	}
}

func TestReviewFindingsAreShortAndPlacedInTheProposal(t *testing.T) {
	session := &ChangeSession{
		Targets: []ChangeTarget{{Path: "cmd/master.go", Content: "package cmd\n\nfunc old() {}\n", Exists: true}, {Path: "cmd/add.go", Content: "package cmd\n\nfunc add() { save(v) }\n", Exists: true}},
		Changes: []ChangeEdit{{Path: "cmd/master.go", Content: "package cmd\n\nfunc rotate() {\n\tpendingDurableDir = dir\n\tsave(v)\n}\n", Status: "modified"}},
	}
	output := `{"verdict":"changes_requested","summary":"Durability is lost across processes.","findings":[
		{"title":"Pending sync is lost on exit","path":"./cmd/master.go","quote":"  pendingDurableDir = dir","detail":"The state lives in process memory."},
		{"title":"Unsynced save not covered","path":"cmd/wrong.go","quote":"func add() {"},
		{"title":"Save without a check","quote":"save(v)"},
		{"title":"` + strings.Repeat("word ", 40) + `","path":"/etc/passwd","detail":"` + strings.Repeat("d", 3000) + `"}
	]}`
	review, err := parseWorkflowReview(output, session)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewItem{
		{Title: "Pending sync is lost on exit", Path: "cmd/master.go", Line: 4, Detail: "The state lives in process memory."},
		{Title: "Unsynced save not covered", Path: "cmd/add.go", Line: 3},
		{Title: "Save without a check"},
	}
	for i, item := range want {
		if review.Findings[i] != item {
			t.Fatalf("finding %d = %+v, want %+v", i, review.Findings[i], item)
		}
	}
	long := review.Findings[3]
	if utf8.RuneCountInString(long.Title) > maxReviewTitle || long.Path != "" || len(long.Detail) > maxReviewDetail {
		t.Fatalf("long finding not trimmed: %d chars, path %q, %d bytes", utf8.RuneCountInString(long.Title), long.Path, len(long.Detail))
	}
	feedback := criticReviewRetryFeedback("Fix it", &ChangeWorkflow{Review: review})
	if !strings.Contains(feedback, "Pending sync is lost on exit (cmd/master.go:4): The state lives in process memory.") {
		t.Fatalf("the repair brief lost a finding's place or detail: %s", feedback)
	}
}
