package project

import "testing"

func TestABriefIsReadFromTheFindingsLabeledEvidence(t *testing.T) {
	security := UnifiedFinding{Message: "`parseKeySalt` accepts a count below the minimum. The header is on disk.",
		Evidence: "Preconditions: A local writer lowers the KDF cost.\nRemediation: Reject counts under `minAcceptedKDFIterations`.\nHow to verify: `go test ./internal/utils -run TestParseKeySalt`.\nReference: https://cwe.mitre.org/data/definitions/916.html"}
	brief := BriefOf(security)
	want := FindingBrief{Problem: "`parseKeySalt` accepts a count below the minimum.", Impact: "A local writer lowers the KDF cost.", Fix: "Reject counts under `minAcceptedKDFIterations`.", Verify: "`go test ./internal/utils -run TestParseKeySalt`."}
	if brief != want {
		t.Fatalf("brief = %+v, want %+v", brief, want)
	}
	file := UnifiedFinding{Message: "`randRead` is reassigned without `t.Cleanup`.", Evidence: "Impact: Later tests call a stale closure.\nFix: Restore it in `t.Cleanup`.\nHow to verify: `go test ./cmd -count=2`."}
	if brief := BriefOf(file); brief.Impact != "Later tests call a stale closure." || brief.Fix != "Restore it in `t.Cleanup`." || brief.Verify != "`go test ./cmd -count=2`." {
		t.Fatalf("file brief = %+v", brief)
	}
}

func TestABriefNeverReadsAStoredInsight(t *testing.T) {
	// File analysis lent a risk the insight of a suggestion about another
	// function; its lines must not become this issue's impact or fix.
	lent := &EngineeringInsight{Mechanism: "`savePassword` returns normally on a mismatch.", WhyItMattersHere: "`writeFileAtomic` does not sync the parent directory.", TransferableLesson: "Sync the directory after the rename."}
	finding := UnifiedFinding{Message: "`savePassword` returns normally on a mismatch.", EngineeringInsight: lent,
		TaskSpec: &BugTaskSpec{AcceptanceCriteria: []string{"A mismatch exits non-zero"}, TestCandidate: &TestCandidateSpec{Path: "cmd/x_test.go", Name: "TestX"}}}
	brief := BriefOf(finding)
	if brief.Impact != "" || brief.Fix != "A mismatch exits non-zero" || brief.Verify != "`TestX` in cmd/x_test.go fails before the fix and passes after it." {
		t.Fatalf("brief = %+v", brief)
	}
}

func TestToolOutputIsNotReadAsABrief(t *testing.T) {
	finding := UnifiedFinding{Message: "TestSave failed.", Evidence: "--- FAIL: TestSave\nexpected 1, got 2"}
	if brief := BriefOf(finding); brief != (FindingBrief{Problem: "TestSave failed."}) || EvidenceIsLabeled(finding.Evidence) {
		t.Fatalf("brief = %+v", brief)
	}
}
