package app

import (
	"fmt"
	"slices"
	"strings"
)

// The testing agent's tests are the coding agent's target: in a repair the
// coder may not change the test files the tester wrote. A coder sure that
// one is wrong disputes it once (finish outcome test_disputed); the next
// testing run reads its argument and decides, and that decision stands. A
// gap the change cannot close (behavior in code the task does not change)
// is a suggested follow-up, not a failing test.

const (
	maxFollowUps        = 5
	maxFollowUpTitle    = 120
	maxFollowUpDetail   = 1000
	maxDisputeReason    = 1000
	outcomeTestDisputed = "test_disputed"
)

// TestDispute is a coder's objection to tests the testing agent wrote,
// waiting for the testing agent's decision.
type TestDispute struct {
	Paths  []string `json:"paths"`
	Reason string   `json:"reason"`
}

// FollowUp is work the testing agent found the change cannot cover: a
// suggested task of its own.
type FollowUp struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Path   string `json:"path,omitempty"`
}

// testerWrote reports whether the testing agent wrote file.
func testerWrote(session *ChangeSession, file string) bool {
	return slices.Contains(session.TesterTests, file)
}

// errTesterTest is the coder's answer when it writes a tester's test.
func errTesterTest(file string) error {
	return fmt.Errorf("%s was written by the testing agent: change the code so it passes. If you are sure the test is wrong, leave it and finish with outcome test_disputed, naming it in disputed_tests and saying why; the testing agent then decides once", file)
}

// validateDispute checks a coder's dispute: tests the tester wrote that it
// has not already decided on.
func validateDispute(session *ChangeSession, finish *toolFinish) error {
	if finish.Outcome != outcomeTestDisputed {
		if len(finish.DisputedTests) > 0 {
			return fmt.Errorf("disputed_tests go with outcome test_disputed")
		}
		return nil
	}
	if len(finish.DisputedTests) == 0 || len(finish.DisputedTests) > 8 {
		return fmt.Errorf("name one to eight disputed tests")
	}
	for _, file := range finish.DisputedTests {
		switch {
		case !testerWrote(session, file):
			return fmt.Errorf("%s is not a test the testing agent wrote; change it as the fix needs", file)
		case slices.Contains(session.DecidedTests, file):
			return fmt.Errorf("the testing agent already decided on %s after a dispute: change the code so it passes", file)
		}
	}
	return nil
}

// validateFollowUps checks the testing agent's suggested follow-ups.
func validateFollowUps(followUps []FollowUp) error {
	if len(followUps) > maxFollowUps {
		return fmt.Errorf("suggest at most %d follow-ups", maxFollowUps)
	}
	for _, followUp := range followUps {
		if !boundedIssueText(followUp.Title, maxFollowUpTitle, true) || !boundedIssueText(followUp.Detail, maxFollowUpDetail, true) {
			return fmt.Errorf("a follow-up has a title of at most %d bytes and a detail of at most %d", maxFollowUpTitle, maxFollowUpDetail)
		}
		if followUp.Path != "" {
			if clean, err := cleanToolPath(followUp.Path); err != nil || clean != followUp.Path {
				return fmt.Errorf("a follow-up's path is a project file")
			}
		}
	}
	return nil
}

// recordToolRunTests keeps what a run means for the tests: the files the
// testing agent wrote, its decision on a dispute and its follow-ups, or the
// coder's dispute.
func recordToolRunTests(current *ChangeSession, role toolRole, changed []string, finish *toolFinish, isTest func(string) bool) {
	if role == toolTester {
		for _, file := range changed {
			if isTest(file) && !slices.Contains(current.TesterTests, file) {
				current.TesterTests = append(current.TesterTests, file)
			}
		}
		if current.TestDispute != nil {
			for _, file := range current.TestDispute.Paths {
				if !slices.Contains(current.DecidedTests, file) {
					current.DecidedTests = append(current.DecidedTests, file)
				}
			}
			current.TestDispute = nil
		}
		current.FollowUps = nil
		if finish != nil {
			current.FollowUps = slices.Clone(finish.FollowUps)
		}
		return
	}
	if finish != nil && finish.Outcome == outcomeTestDisputed {
		current.TestDispute = &TestDispute{Paths: slices.Clone(finish.DisputedTests), Reason: clip(strings.TrimSpace(finish.Summary), maxDisputeReason)}
	}
}

// withTestDispute gives the testing agent the coder's dispute to decide.
func withTestDispute(session *ChangeSession, message string) string {
	dispute := session.TestDispute
	if dispute == nil {
		return message
	}
	return fmt.Sprintf("%s\n\nThe coding agent disputes tests you wrote and it may not change: %s. It says: %s\nDecide once; your decision stands. Keep a test only when it shows a defect of this change or an unmet acceptance criterion (the coding agent must then fix the code). Otherwise correct it or remove it; a gap the change cannot close becomes a follow-up in finish. Say in finish what you decided for each.",
		message, strings.Join(dispute.Paths, ", "), dispute.Reason)
}

// validateTestRecords checks the test bookkeeping of a stored task.
func validateTestRecords(session *ChangeSession) error {
	for _, files := range [][]string{session.TesterTests, session.DecidedTests} {
		if len(files) > maxStoredTaskPaths {
			return fmt.Errorf("a task records at most %d tests", maxStoredTaskPaths)
		}
		for _, file := range files {
			if clean, err := cleanToolPath(file); err != nil || clean != file {
				return fmt.Errorf("a recorded test is a project file")
			}
		}
	}
	if dispute := session.TestDispute; dispute != nil && (len(dispute.Paths) == 0 || len(dispute.Paths) > 8 || !boundedIssueText(dispute.Reason, maxDisputeReason, false)) {
		return fmt.Errorf("a test dispute names one to eight tests and why")
	}
	return validateFollowUps(session.FollowUps)
}
