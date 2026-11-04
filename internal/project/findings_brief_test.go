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

func TestABriefFallsBackToTheFindingsOwnInsightAndTaskSpec(t *testing.T) {
	own := &EngineeringInsight{Mechanism: "The test never checks the partial state.", WhyItMattersHere: "A no-op restore passes.", TransferableLesson: "Assert the value inside the subtest."}
	finding := UnifiedFinding{Message: "The test never checks the partial state.", EngineeringInsight: own,
		TaskSpec: &BugTaskSpec{AcceptanceCriteria: []string{"The subtest asserts `nameValue`"}, TestCandidate: &TestCandidateSpec{Path: "cmd/x_test.go", Name: "TestX"}}}
	brief := BriefOf(finding)
	if brief.Impact != "A no-op restore passes." || brief.Fix != "Assert the value inside the subtest." || brief.Verify != "`TestX` in cmd/x_test.go fails before the fix and passes after it." {
		t.Fatalf("brief = %+v", brief)
	}
	// The file's general insight lent to a risk explains something else.
	finding.EngineeringInsight = &EngineeringInsight{Mechanism: "Something about another function.", WhyItMattersHere: "Unrelated."}
	if brief := BriefOf(finding); brief.Impact != "" || brief.Fix != "The subtest asserts `nameValue`" {
		t.Fatalf("borrowed insight was used: %+v", brief)
	}
}

func TestToolOutputIsNotReadAsABrief(t *testing.T) {
	finding := UnifiedFinding{Message: "TestSave failed.", Evidence: "--- FAIL: TestSave\nexpected 1, got 2"}
	if brief := BriefOf(finding); brief != (FindingBrief{Problem: "TestSave failed."}) || EvidenceIsLabeled(finding.Evidence) {
		t.Fatalf("brief = %+v", brief)
	}
}
