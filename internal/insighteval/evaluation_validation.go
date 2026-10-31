package insighteval

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// ValidateEngineeringInsightEvaluationReceipt validates a receipt against a
// caller-selected candidate, corpus, limits and schedule. Collection evidence may
// be unscored; qualification evidence with an emitted response may not.
func ValidateEngineeringInsightEvaluationReceipt(receipt EngineeringInsightEvaluationReceipt, expected EngineeringInsightEvaluationExpectation) (EngineeringInsightEvaluationReport, error) {
	if err := validateEvaluationExpectation(expected); err != nil {
		return EngineeringInsightEvaluationReport{}, err
	}
	if err := validateReceiptIdentity(receipt, expected); err != nil {
		return EngineeringInsightEvaluationReport{}, err
	}
	if err := validateReceiptConsumption(receipt); err != nil {
		return EngineeringInsightEvaluationReport{}, err
	}
	recorded, err := validateAttempts(receipt, expected)
	if err != nil {
		return EngineeringInsightEvaluationReport{}, err
	}
	report := buildEvaluationReport(expected, recorded)
	if receipt.Mode == EngineeringInsightCollectionMode {
		report.Outcome = EngineeringInsightCollectionOutcome
		return report, nil
	}
	if len(recorded) != len(expected.Schedule) {
		report.Outcome = EngineeringInsightIncompleteOutcome
		return report, nil
	}
	if report.UsableAttempts < minimumUsableQualificationAttempts || report.CompleteAttempts < minimumCompleteQualificationAttempts || report.UsefulSubstantiveAttempts < minimumUsefulSubstantiveAttempts || report.OmittedControls != qualificationControlAttemptCount || report.CriticalFalseClaims != 0 || report.InvalidProviderMetadata != 0 {
		report.Outcome = EngineeringInsightFailedOutcome
		return report, nil
	}
	report.Outcome = EngineeringInsightPassedOutcome
	return report, nil
}

func validateReceiptIdentity(receipt EngineeringInsightEvaluationReceipt, expected EngineeringInsightEvaluationExpectation) error {
	if receipt.Mode != EngineeringInsightCollectionMode && receipt.Mode != EngineeringInsightQualificationMode {
		return fmt.Errorf("evaluation receipt has invalid mode")
	}
	if receipt.ProtocolVersion != expected.ProtocolVersion || strings.TrimSpace(receipt.RunID) == "" || receipt.CandidateID != expected.CandidateID || receipt.Provider != expected.Provider || receipt.Model != expected.Model || receipt.PromptVersion != expected.PromptVersion || receipt.CorpusID != expected.CorpusID || receipt.CorpusDigest != expected.CorpusDigest || receipt.BaseRevision != expected.BaseRevision {
		return fmt.Errorf("evaluation receipt identity does not match the selected candidate")
	}
	if receipt.MaxRequests != expected.MaxRequests || receipt.MaxOutputTokens != expected.MaxOutputTokens || receipt.AttemptTimeoutSeconds != expected.AttemptTimeoutSeconds {
		return fmt.Errorf("evaluation receipt limits do not match the selected limits")
	}
	return nil
}

func validateReceiptConsumption(receipt EngineeringInsightEvaluationReceipt) error {
	if receipt.Consumption.Requests != len(receipt.Attempts) || receipt.Consumption.Requests < 0 || receipt.Consumption.Requests > receipt.MaxRequests {
		return fmt.Errorf("evaluation receipt consumption is invalid")
	}
	totalTokens := 0
	for _, attempt := range receipt.Attempts {
		if !attempt.InvalidProviderMetadata && (attempt.OutputTokens < 0 || attempt.OutputTokens > receipt.MaxOutputTokens) || attempt.ElapsedMilliseconds > float64(receipt.AttemptTimeoutSeconds)*1000 && !isCensoredAttempt(attempt) {
			return fmt.Errorf("evaluation attempt consumption is invalid")
		}
		totalTokens += attempt.OutputTokens
	}
	if totalTokens != receipt.Consumption.OutputTokens {
		return fmt.Errorf("evaluation receipt consumption is invalid")
	}
	return nil
}

func isCensoredAttempt(attempt EngineeringInsightEvaluationAttempt) bool {
	return attempt.Outcome == "timeout" || attempt.Outcome == "failed" && attempt.FinishReason == "canceled" || attempt.Outcome == "unknown"
}

func validateAttempts(receipt EngineeringInsightEvaluationReceipt, expected EngineeringInsightEvaluationExpectation) (map[string]EngineeringInsightEvaluationAttempt, error) {
	expectedByID := make(map[string]EngineeringInsightExpectedAttempt, len(expected.Schedule))
	for _, scheduled := range expected.Schedule {
		id := buildExpectedAttemptID(scheduled)
		if _, duplicate := expectedByID[id]; duplicate || !isValidExpectedAttempt(scheduled) {
			return nil, fmt.Errorf("evaluation expectation has an invalid schedule")
		}
		expectedByID[id] = scheduled
	}
	recorded := make(map[string]EngineeringInsightEvaluationAttempt, len(receipt.Attempts))
	if len(receipt.Attempts) > len(expected.Schedule) {
		return nil, fmt.Errorf("evaluation receipt contains evidence outside the selected schedule")
	}
	for index, attempt := range receipt.Attempts {
		id := buildExpectedAttemptID(EngineeringInsightExpectedAttempt{CaseName: attempt.CaseName, Partition: attempt.Partition, Intent: attempt.Intent, Repetition: attempt.Repetition, Attempt: attempt.Attempt})
		if _, known := expectedByID[id]; !known || id != buildExpectedAttemptID(expected.Schedule[index]) || attempt.CandidateID != expected.CandidateID {
			return nil, fmt.Errorf("evaluation receipt contains evidence outside the selected schedule")
		}
		if _, duplicate := recorded[id]; duplicate {
			return nil, fmt.Errorf("evaluation receipt contains duplicate attempt evidence")
		}
		if err := validateAttempt(attempt, receipt.Mode); err != nil {
			return nil, err
		}
		recorded[id] = attempt
	}
	return recorded, nil
}

func isValidExpectedAttempt(attempt EngineeringInsightExpectedAttempt) bool {
	return strings.TrimSpace(attempt.CaseName) != "" && (attempt.Partition == "development" || attempt.Partition == "qualification") && (attempt.Intent == "substantive" || attempt.Intent == "control") && attempt.Repetition > 0 && attempt.Attempt > 0
}

func buildExpectedAttemptID(attempt EngineeringInsightExpectedAttempt) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", attempt.CaseName, attempt.Partition, attempt.Intent, attempt.Repetition, attempt.Attempt)
}

func validateAttempt(attempt EngineeringInsightEvaluationAttempt, mode EngineeringInsightEvaluationMode) error {
	if !isValidAttemptValues(attempt) {
		return fmt.Errorf("evaluation attempt has invalid evidence")
	}
	if !isValidOutcomeFinishReason(attempt.Outcome, attempt.FinishReason) {
		return fmt.Errorf("evaluation attempt has inconsistent outcome evidence")
	}
	if err := validateAttemptSummary(attempt); err != nil {
		return err
	}
	return validateAttemptScore(attempt, mode)
}

func isValidAttemptValues(attempt EngineeringInsightEvaluationAttempt) bool {
	return isValidOutcome(attempt.Outcome) && isValidOptionalInsight(attempt.OptionalInsight) && isValidFinishReason(attempt.FinishReason) && isFiniteNonNegative(attempt.ElapsedMilliseconds)
}

func isValidOutcomeFinishReason(outcome, finishReason string) bool {
	switch outcome {
	case "completed", "malformed", "abstained":
		return finishReason == "stop"
	case "truncated":
		return finishReason == "length"
	case "timeout":
		return finishReason == "timeout"
	case "failed":
		return finishReason == "error" || finishReason == "canceled"
	case "budget_exhausted":
		return finishReason == "unknown"
	case "unknown":
		return finishReason == "unknown"
	default:
		return false
	}
}

func validateAttemptSummary(attempt EngineeringInsightEvaluationAttempt) error {
	if attempt.Outcome != "completed" {
		return validateNonCompletedSummary(attempt)
	}
	return validateCompletedSummary(attempt)
}

func validateNonCompletedSummary(attempt EngineeringInsightEvaluationAttempt) error {
	if hasSummaryEvidence(attempt) {
		return fmt.Errorf("evaluation attempt has inconsistent summary evidence")
	}
	return nil
}

func validateCompletedSummary(attempt EngineeringInsightEvaluationAttempt) error {
	if !attempt.EmittedResponse {
		return fmt.Errorf("evaluation attempt has inconsistent summary evidence")
	}
	if attempt.UsableSummary {
		return validateUsableSummary(attempt)
	}
	if hasSummaryEvidence(attempt) {
		return fmt.Errorf("evaluation attempt has inconsistent summary evidence")
	}
	return nil
}

func validateUsableSummary(attempt EngineeringInsightEvaluationAttempt) error {
	if attempt.OptionalInsight == "not_evaluated" {
		return fmt.Errorf("evaluation attempt has inconsistent summary evidence")
	}
	if !attempt.CompleteSummary {
		return nil
	}
	if attempt.OptionalInsight == "rejected" || attempt.OptionalSectionDegraded {
		return fmt.Errorf("evaluation attempt has inconsistent summary evidence")
	}
	return nil
}

func hasSummaryEvidence(attempt EngineeringInsightEvaluationAttempt) bool {
	return attempt.UsableSummary || attempt.CompleteSummary || attempt.OptionalInsight != "not_evaluated" || attempt.OptionalSectionDegraded
}

func validateAttemptScore(attempt EngineeringInsightEvaluationAttempt, mode EngineeringInsightEvaluationMode) error {
	if err := validateResponseDigest(attempt); err != nil {
		return err
	}
	if attempt.Score == nil {
		if mode == EngineeringInsightQualificationMode && attempt.EmittedResponse {
			return fmt.Errorf("qualification receipt has unscored response evidence")
		}
		return nil
	}
	if !attempt.EmittedResponse || attempt.Score.ResponseDigest != attempt.ResponseDigest || !isValidDigest(attempt.Score.ResponseDigest) || !isValidScore(*attempt.Score) {
		return fmt.Errorf("evaluation attempt has invalid score evidence")
	}
	if attempt.Score.RetainExample && (attempt.Score.CriticalFalseClaim || attempt.Score.Total() < minimumRetainedEngineeringInsightScore) {
		return fmt.Errorf("evaluation attempt retains an ineligible example")
	}
	return nil
}

func validateResponseDigest(attempt EngineeringInsightEvaluationAttempt) error {
	if attempt.EmittedResponse == (attempt.ResponseDigest == "") {
		return fmt.Errorf("evaluation attempt has invalid response evidence")
	}
	if attempt.EmittedResponse && !isValidDigest(attempt.ResponseDigest) {
		return fmt.Errorf("evaluation attempt has invalid response evidence")
	}
	return nil
}

func isValidOutcome(value string) bool {
	return value == "completed" || value == "timeout" || value == "malformed" || value == "truncated" || value == "abstained" || value == "failed" || value == "budget_exhausted" || value == "unknown"
}
func isValidOptionalInsight(value string) bool {
	return value == "present" || value == "omitted" || value == "rejected" || value == "not_evaluated"
}
func isValidFinishReason(value string) bool {
	return value == "stop" || value == "length" || value == "error" || value == "timeout" || value == "canceled" || value == "unknown"
}
func isValidScore(score EngineeringInsightAttemptScore) bool {
	for _, value := range []int{score.Correctness, score.LocalRelevance, score.TradeoffClarity, score.UsefulVerification} {
		if value < 0 || value > 2 {
			return false
		}
	}
	return true
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func isValidDigest(value string) bool { return digestPattern.MatchString(value) }
func isFiniteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
