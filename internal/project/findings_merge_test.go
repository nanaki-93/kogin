package project

import (
	"testing"
)

func suggestion(analysis, severity, path string, line int, title, message string) UnifiedFinding {
	return UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: severity, Title: title, Message: message, OriginatingAnalysis: analysis, Location: FindingLocation{Path: path, StartLine: line, EndLine: line}}
}

func TestReportsOfOneIssueByTwoPassesBecomeOneFinding(t *testing.T) {
	store, err := NewFindingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := FindingInput{ProjectID: "p", ProjectRevision: "r"}
	reported := []UnifiedFinding{
		suggestion("security", "medium", "cmd/master.go", 256, "Recorded KDF iterations ignored", "prepareKey reads the iteration count recorded in the salt file but deriveKey always uses the constant, so a salt recorded with another count derives a different key."),
		suggestion("file", "high", "cmd/master.go", 461, "Recorded PBKDF2 iterations ignored", "deriveKey always passes the constant keyDerivationIterations; the iteration count readKeySalt returns from the salt file header is ignored, so the derived key differs."),
		suggestion("file", "medium", "cmd/master.go", 201, "Legacy key deleted before migration", "generateKey removes the legacy key file before the vault is saved under the new key."),
		// The same pass twice is two issues; another file is another issue.
		suggestion("file", "low", "cmd/master.go", 300, "Recorded iterations ignored in tests", "the salt file iteration count recorded is ignored by deriveKey when tests derive the constant key."),
		suggestion("security", "medium", "cmd/get.go", 12, "Recorded KDF iterations ignored", "prepareKey reads the iteration count recorded in the salt file but deriveKey always uses the constant."),
	}
	stored, err := store.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 {
		t.Fatalf("findings = %d: %+v", len(stored), stored)
	}
	var merged *UnifiedFinding
	for i := range stored {
		if stored[i].Title == "Recorded PBKDF2 iterations ignored" {
			merged = &stored[i]
		}
		if stored[i].Title == "Recorded KDF iterations ignored" && stored[i].Location.Path == "cmd/master.go" {
			t.Fatal("the security pass's report was kept beside the file pass's")
		}
	}
	if merged == nil || len(merged.AlsoReported) != 1 || merged.AlsoReported[0] != (FindingReport{Analysis: "security", Title: "Recorded KDF iterations ignored", Severity: "medium", Line: 256}) {
		t.Fatalf("merged finding = %+v", merged)
	}

	// A report with a triage of its own is not merged away.
	security := reported[0]
	security = normalizeFinding(security, input)
	if err := store.SetStatus(security.ID, FindingStatusInProgress); err == nil {
		t.Fatal("set the status of a finding that was merged away")
	}
	single, err := store.ReconcileSource(input, FindingSourceAI, reported[:1])
	if err != nil || len(single) != 5 {
		t.Fatalf("single report = %+v, %v", single, err)
	}
	if err := store.SetStatus(security.ID, FindingStatusInProgress); err != nil {
		t.Fatal(err)
	}
	again, err := store.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range again {
		if finding.ID == security.ID && finding.Status != FindingStatusInProgress {
			t.Fatalf("the finding a task is fixing = %+v", finding)
		}
		if finding.Title == "Recorded PBKDF2 iterations ignored" && len(finding.AlsoReported) != 0 {
			t.Fatalf("a finding in progress was merged into another: %+v", finding)
		}
	}
}

func TestAnIssueDismissedBeforeIsNotOpenedAgainInNewWords(t *testing.T) {
	store, err := NewFindingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := FindingInput{ProjectID: "p", ProjectRevision: "r"}
	old := suggestion("file", "medium", "cmd/master.go", 139, "Derived key zeroed after handoff", "generateKey hands the derived key slice to SetMasterKey and defers clear of that slice; if the setter keeps the slice instead of copying it, the active master key becomes zero bytes.")
	stored, err := store.ReconcileSource(input, FindingSourceAI, []UnifiedFinding{old})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTargetStatus(FindingTarget{ProjectID: "p", Kind: "semantic", ID: stored[0].ID, Path: "cmd/master.go"}, FindingStatusDismissed, ""); err != nil {
		t.Fatal(err)
	}
	// The next run words it afresh and also finds something else.
	reworded := suggestion("file", "medium", "cmd/master.go", 0, "Active key wiped by deferred clear", "rotateMasterKey and generateKey call SetMasterKey with the key after defer wipe, which clears the key slice; if SetMasterKey keeps the slice instead of copying it, the master key becomes zero bytes.")
	other := suggestion("file", "medium", "cmd/master.go", 201, "Legacy key deleted before migration", "generateKey removes the legacy key file before the vault is saved under the new key.")
	next, err := store.ReconcileSource(input, FindingSourceAI, []UnifiedFinding{reworded, other})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]UnifiedFinding{}
	for _, finding := range next {
		byTitle[finding.Title] = finding
	}
	if repeat := byTitle[reworded.Title]; repeat.Status != FindingStatusDismissed || repeat.SeenBefore == nil || repeat.SeenBefore.ID != stored[0].ID || repeat.SeenBefore.Title != old.Title {
		t.Fatalf("the repeat = %+v", repeat)
	}
	if fresh := byTitle[other.Title]; fresh.Status != FindingStatusOpen || fresh.SeenBefore != nil {
		t.Fatalf("a different issue = %+v", fresh)
	}
	if earlier := byTitle[old.Title]; earlier.Status != FindingStatusDismissed {
		t.Fatalf("the dismissed finding = %+v", earlier)
	}
	// Reopened by the user, the repeat stays open on the next run.
	if err := store.SetStatus(byTitle[reworded.Title].ID, FindingStatusOpen); err != nil {
		t.Fatal(err)
	}
	last, err := store.ReconcileSource(input, FindingSourceAI, []UnifiedFinding{reworded, other})
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range last {
		if finding.Title == reworded.Title && finding.Status != FindingStatusOpen {
			t.Fatalf("the reopened repeat = %+v", finding)
		}
	}
}
