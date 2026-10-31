package insighteval

import (
	"fmt"
	"strings"
)

func validateEvaluationExpectation(expected EngineeringInsightEvaluationExpectation) error {
	if !hasCompleteExpectationIdentity(expected) {
		return fmt.Errorf("evaluation expectation is incomplete")
	}
	if expected.ProtocolVersion == "v2" {
		return validateV2EvaluationExpectation(expected)
	}
	if expected.ProtocolVersion != "" {
		return fmt.Errorf("evaluation expectation has an invalid protocol version")
	}
	if hasQualificationLimits(expected) {
		return validateQualificationSchedule(expected.Schedule)
	}
	if expected.MaxRequests <= 0 || expected.MaxRequests > 6 || expected.MaxOutputTokens != qualificationOutputTokenCap || expected.AttemptTimeoutSeconds != qualificationAttemptTimeoutSeconds || len(expected.Schedule) == 0 || len(expected.Schedule) > expected.MaxRequests {
		return fmt.Errorf("evaluation expectation has invalid limits")
	}
	for _, attempt := range expected.Schedule {
		if !isValidExpectedAttempt(attempt) {
			return fmt.Errorf("evaluation expectation has an invalid schedule")
		}
	}
	return nil
}

func validateV2EvaluationExpectation(expected EngineeringInsightEvaluationExpectation) error {
	if expected.CorpusID != recoveryV2CorpusID || expected.CandidateID != recoveryV2CandidateID || expected.Provider != recoveryV2Provider || expected.Model != recoveryV2Model || expected.PromptVersion != recoveryV2PromptVersion || !isValidRecoveryV2BaseRevision(expected.BaseRevision) || expected.MaxOutputTokens != qualificationOutputTokenCap || expected.AttemptTimeoutSeconds != qualificationAttemptTimeoutSeconds {
		return fmt.Errorf("evaluation expectation has an invalid v2 identity")
	}
	if expected.MaxRequests == recoveryV2DevelopmentRequests && expected.CorpusDigest == recoveryV2DevelopmentDigest {
		return validateV2Schedule(expected.Schedule, "development", 12, 8, 4)
	}
	if expected.MaxRequests == recoveryV2QualificationRequests && expected.CorpusDigest == recoveryV2QualificationDigest {
		return validateV2Schedule(expected.Schedule, "qualification", 24, 16, 8)
	}
	return fmt.Errorf("evaluation expectation has an invalid v2 schedule")
}

func validateV2Schedule(schedule []EngineeringInsightExpectedAttempt, partition string, count, substantive, controls int) error {
	if len(schedule) != count {
		return fmt.Errorf("evaluation expectation has an invalid schedule")
	}
	seen := make(map[string]bool, count)
	actualSubstantive, actualControls := 0, 0
	for _, attempt := range schedule {
		if strings.TrimSpace(attempt.CaseName) == "" || seen[attempt.CaseName] || attempt.Partition != partition || attempt.Repetition != 1 || attempt.Attempt != 1 {
			return fmt.Errorf("evaluation expectation has an invalid schedule")
		}
		seen[attempt.CaseName] = true
		if attempt.Intent == "substantive" {
			actualSubstantive++
		} else if attempt.Intent == "control" {
			actualControls++
		} else {
			return fmt.Errorf("evaluation expectation has an invalid schedule")
		}
	}
	if actualSubstantive != substantive || actualControls != controls {
		return fmt.Errorf("evaluation expectation has an invalid schedule")
	}
	return nil
}

func hasCompleteExpectationIdentity(expected EngineeringInsightEvaluationExpectation) bool {
	for _, value := range []string{expected.CandidateID, expected.Provider, expected.Model, expected.PromptVersion, expected.CorpusID, expected.CorpusDigest, expected.BaseRevision} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func hasQualificationLimits(expected EngineeringInsightEvaluationExpectation) bool {
	return expected.MaxRequests == qualificationRequestCap && expected.MaxOutputTokens == qualificationOutputTokenCap && expected.AttemptTimeoutSeconds == qualificationAttemptTimeoutSeconds && len(expected.Schedule) == qualificationRequestCap
}

type expectedCaseSchedule struct {
	intent      string
	repetitions map[int]bool
}

func validateQualificationSchedule(schedule []EngineeringInsightExpectedAttempt) error {
	cases := make(map[string]expectedCaseSchedule, 12)
	for _, scheduled := range schedule {
		if err := validateAndRecordExpectedAttempt(cases, scheduled); err != nil {
			return err
		}
	}
	if len(cases) != 12 || buildScheduleIntentCount(cases, "substantive") != 8 || buildScheduleIntentCount(cases, "control") != 4 {
		return fmt.Errorf("evaluation expectation has an invalid schedule")
	}
	for _, scheduled := range cases {
		if len(scheduled.repetitions) != 2 || !scheduled.repetitions[1] || !scheduled.repetitions[2] {
			return fmt.Errorf("evaluation expectation has an invalid schedule")
		}
	}
	return nil
}

func validateAndRecordExpectedAttempt(cases map[string]expectedCaseSchedule, attempt EngineeringInsightExpectedAttempt) error {
	if !isValidQualificationExpectedAttempt(attempt) {
		return fmt.Errorf("evaluation expectation has an invalid schedule")
	}
	scheduled, exists := cases[attempt.CaseName]
	if !exists {
		scheduled = expectedCaseSchedule{intent: attempt.Intent, repetitions: make(map[int]bool, 2)}
	}
	if scheduled.intent != attempt.Intent || scheduled.repetitions[attempt.Repetition] {
		return fmt.Errorf("evaluation expectation has an invalid schedule")
	}
	scheduled.repetitions[attempt.Repetition] = true
	cases[attempt.CaseName] = scheduled
	return nil
}

func isValidQualificationExpectedAttempt(attempt EngineeringInsightExpectedAttempt) bool {
	return strings.TrimSpace(attempt.CaseName) != "" && attempt.Partition == "qualification" && (attempt.Intent == "substantive" || attempt.Intent == "control") && (attempt.Repetition == 1 || attempt.Repetition == 2) && attempt.Attempt == 1
}

func buildScheduleIntentCount(cases map[string]expectedCaseSchedule, intent string) int {
	count := 0
	for _, scheduled := range cases {
		if scheduled.intent == intent {
			count++
		}
	}
	return count
}
