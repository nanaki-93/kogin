package insighteval

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/app"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

const (
	developmentPartition   = "development"
	qualificationPartition = "qualification"
	substantiveIntent      = "substantive"
	controlIntent          = "control"
)

func TestEngineeringInsightEvaluationMalformedOutputIsSeparateFromControls(t *testing.T) {
	target := project.IndexFile{Path: "fixture.go", Language: "Go"}
	validParent := `{"purpose":"Summarizes the fixture.","responsibilities":[],"dependencies":[],"side_effects":[],"risks":[],"suggestions":[]`
	for name, output := range map[string]string{
		"unknown parent field": validParent + `,"unexpected":true}`,
		"trailing JSON":        validParent + `}{}`,
		"missing category":     strings.Replace(validParent, `"risks":[]`, `"risks":[{"severity":"low","summary":"Historical risk."}]`, 1) + `}`,
		"unknown category":     strings.Replace(validParent, `"risks":[]`, `"risks":[{"category":"reliability","severity":"low","summary":"Unknown category."}]`, 1) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			if app.AssessFileAnalysisEvaluation(output, target, "package fixture\n").UsableSummary {
				t.Fatal("strict parent validation accepted malformed output")
			}
		})
	}

	for name, insight := range map[string]string{
		"malformed optional insight": `{"mechanism":false}`,
		"oversized optional insight": fmt.Sprintf(`{"mechanism":%q,"why_it_matters_here":"local"}`, strings.Repeat("x", 1001)),
	} {
		t.Run(name, func(t *testing.T) {
			output := validParent + `,"engineering_insight":` + insight + `}`
			assessment := app.AssessFileAnalysisEvaluation(output, target, "package fixture\n")
			if !assessment.UsableSummary {
				t.Fatal("valid parent with optional malformed insight was rejected")
			}
			if assessment.Diagnostics.OptionalInsight() != "rejected" || !assessment.Diagnostics.Degraded() {
				t.Fatal("invalid optional insight was retained")
			}
		})
	}
}

func TestEngineeringInsightQualificationThresholdBoundaries(t *testing.T) {
	expected := qualificationExpectation()
	for name, mutate := range map[string]struct {
		want  EngineeringInsightEvaluationOutcome
		apply func(*EngineeringInsightEvaluationReceipt)
	}{
		"thresholds pass": {want: EngineeringInsightPassedOutcome},
		"23 usable passes": {want: EngineeringInsightPassedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			degradeAttempt(&receipt.Attempts[0], false)
		}},
		"22 complete passes": {want: EngineeringInsightPassedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			degradeAttempt(&receipt.Attempts[0], true)
			degradeAttempt(&receipt.Attempts[1], true)
		}},
		"13 useful passes": {want: EngineeringInsightPassedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			for index := 0; index < 3; index++ {
				receipt.Attempts[index].Score.Correctness = 0
				receipt.Attempts[index].Score.LocalRelevance = 1
			}
		}},
		"optional degradation retains useful and omitted metrics": {want: EngineeringInsightPassedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].CompleteSummary = false
			receipt.Attempts[0].OptionalSectionDegraded = true
			for index := range receipt.Attempts {
				if receipt.Attempts[index].Intent == controlIntent {
					receipt.Attempts[index].CompleteSummary = false
					receipt.Attempts[index].OptionalSectionDegraded = true
					return
				}
			}
		}},
		"22 usable fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			degradeAttempt(&receipt.Attempts[0], false)
			degradeAttempt(&receipt.Attempts[1], false)
		}},
		"21 complete fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			degradeAttempt(&receipt.Attempts[0], true)
			degradeAttempt(&receipt.Attempts[1], true)
			degradeAttempt(&receipt.Attempts[2], true)
		}},
		"12 useful fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			for index := 0; index < 4; index++ {
				receipt.Attempts[index].Score.Correctness = 0
				receipt.Attempts[index].Score.LocalRelevance = 1
			}
		}},
		"one bad control fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			for index := range receipt.Attempts {
				if receipt.Attempts[index].Intent == controlIntent {
					receipt.Attempts[index].OptionalInsight = "present"
					return
				}
			}
		}},
		"critical claim always fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].Score.CriticalFalseClaim = true
		}},
		"invalid provider metadata always fails": {want: EngineeringInsightFailedOutcome, apply: func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].InvalidProviderMetadata = true
			receipt.Attempts[0].OutputTokens = qualificationOutputTokenCap + 1
			receipt.Consumption.OutputTokens += qualificationOutputTokenCap + 1 - 100
		}},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := qualificationReceipt()
			if mutate.apply != nil {
				mutate.apply(&receipt)
			}
			report, err := ValidateEngineeringInsightEvaluationReceipt(receipt, expected)
			if err != nil {
				t.Fatalf("validate receipt: %v", err)
			}
			if report.Outcome != mutate.want {
				t.Fatalf("outcome = %s, want %s", report.Outcome, mutate.want)
			}
			if name == "thresholds pass" && (report.Latency.MedianMilliseconds != 12.5 || report.Latency.P95Milliseconds != 23) {
				t.Fatalf("latency = %+v, want median 12.5 and p95 23", report.Latency)
			}
		})
	}
}

func TestEngineeringInsightEvaluationPreservesCensoredDeadlineOverhead(t *testing.T) {
	expected := qualificationExpectation()
	timeout := qualificationReceipt()
	timeout.Attempts[0].Outcome = "timeout"
	timeout.Attempts[0].FinishReason = "timeout"
	timeout.Attempts[0].EmittedResponse = false
	timeout.Attempts[0].ResponseDigest = ""
	timeout.Attempts[0].OutputTokens = 0
	timeout.Consumption.OutputTokens -= 100
	timeout.Attempts[0].Score = nil
	timeout.Attempts[0].OptionalInsight = "not_evaluated"
	timeout.Attempts[0].UsableSummary = false
	timeout.Attempts[0].CompleteSummary = false
	timeout.Attempts[0].ElapsedMilliseconds = float64(qualificationAttemptTimeoutSeconds*1000 + 1)
	if _, err := ValidateEngineeringInsightEvaluationReceipt(timeout, expected); err != nil {
		t.Fatalf("censored timeout overhead was rejected: %v", err)
	}
	completed := qualificationReceipt()
	completed.Attempts[0].ElapsedMilliseconds = float64(qualificationAttemptTimeoutSeconds*1000 + 1)
	if _, err := ValidateEngineeringInsightEvaluationReceipt(completed, expected); err == nil {
		t.Fatal("overlong completed attempt was accepted")
	}
}

func TestEngineeringInsightEvaluationRejectsCaseIntentChangedBetweenRepetitions(t *testing.T) {
	expected := qualificationExpectation()
	expected.Schedule[12].Intent = controlIntent
	if _, err := ValidateEngineeringInsightEvaluationReceipt(qualificationReceipt(), expected); err == nil {
		t.Fatal("schedule with a changed case intent was accepted")
	}
}

func TestEngineeringInsightEvaluationSeparatesCollectionIncompleteAndInvalidEvidence(t *testing.T) {
	expected := qualificationExpectation()
	collection := qualificationReceipt()
	collection.Mode = EngineeringInsightCollectionMode
	collection.Attempts = collection.Attempts[:1]
	collection.Consumption.Requests = 1
	collection.Consumption.OutputTokens = collection.Attempts[0].OutputTokens
	collection.Attempts[0].Score = nil
	report, err := ValidateEngineeringInsightEvaluationReceipt(collection, expected)
	if err != nil || report.Outcome != EngineeringInsightCollectionOutcome {
		t.Fatalf("collection = %+v, %v", report, err)
	}

	incomplete := qualificationReceipt()
	incomplete.Attempts = incomplete.Attempts[:23]
	incomplete.Consumption.Requests = len(incomplete.Attempts)
	incomplete.Consumption.OutputTokens -= 100
	report, err = ValidateEngineeringInsightEvaluationReceipt(incomplete, expected)
	if err != nil || report.Outcome != EngineeringInsightIncompleteOutcome {
		t.Fatalf("incomplete = %+v, %v", report, err)
	}

	timeout := qualificationReceipt()
	makeNonCompletedAttempt(&timeout.Attempts[0], "timeout", "timeout")
	report, err = ValidateEngineeringInsightEvaluationReceipt(timeout, expected)
	if err != nil || report.Outcome != EngineeringInsightPassedOutcome || report.Latency.SuccessfulCount != 23 || report.Latency.TimeoutOrCensoredAttempts != 1 {
		t.Fatalf("timeout evidence = %+v, %v", report, err)
	}

	for name, mutate := range map[string]func(*EngineeringInsightEvaluationReceipt){
		"unscored emitted qualification response": func(receipt *EngineeringInsightEvaluationReceipt) { receipt.Attempts[0].Score = nil },
		"mixed candidate":                         func(receipt *EngineeringInsightEvaluationReceipt) { receipt.Attempts[0].CandidateID = "other" },
		"duplicate attempt":                       func(receipt *EngineeringInsightEvaluationReceipt) { receipt.Attempts[1] = receipt.Attempts[0] },
		"out of order attempt": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0], receipt.Attempts[1] = receipt.Attempts[1], receipt.Attempts[0]
		},
		"selected cap mismatch": func(receipt *EngineeringInsightEvaluationReceipt) { receipt.MaxOutputTokens++ },
		"over cap consumption":  func(receipt *EngineeringInsightEvaluationReceipt) { receipt.Consumption.Requests++ },
		"elapsed time exceeds cap": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].ElapsedMilliseconds = 300001
		},
		"nonfinite latency": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].ElapsedMilliseconds = math.Inf(1)
		},
		"completed length finish": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].FinishReason = "length"
		},
		"truncated stop finish": func(receipt *EngineeringInsightEvaluationReceipt) {
			makeNonCompletedAttempt(&receipt.Attempts[0], "truncated", "stop")
		},
		"timeout error finish": func(receipt *EngineeringInsightEvaluationReceipt) {
			makeNonCompletedAttempt(&receipt.Attempts[0], "timeout", "error")
		},
		"canceled completed finish": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].FinishReason = "canceled"
		},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := qualificationReceipt()
			mutate(&receipt)
			if _, err := ValidateEngineeringInsightEvaluationReceipt(receipt, expected); err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}

func TestEngineeringInsightEvaluationV2ReceiptSchemaAndDevelopmentGate(t *testing.T) {
	expected := v2DevelopmentExpectation()
	valid := v2Receipt(expected, EngineeringInsightCollectionMode)
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEngineeringInsightEvaluationReceipt(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateEngineeringInsightEvaluationReceipt(decoded, expected); err != nil || !decoded.DevelopmentGatePassed() {
		t.Fatalf("valid v2 development receipt rejected: %v", err)
	}

	for name, mutate := range map[string]func(*EngineeringInsightEvaluationReceipt){
		"one score of five": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].Score.Correctness = 1
			receipt.Attempts[0].Score.LocalRelevance = 0
		},
		"one unscored control": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[11].Score = nil
		},
		"one emitted control": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[8].OptionalInsight = "present"
		},
		"critical whole-final claim": func(receipt *EngineeringInsightEvaluationReceipt) {
			receipt.Attempts[0].Score.CriticalFalseClaim = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := v2Receipt(expected, EngineeringInsightCollectionMode)
			mutate(&receipt)
			if receipt.DevelopmentGatePassed() {
				t.Fatal("invalid v2 development evidence passed")
			}
		})
	}
	seven := v2Receipt(expected, EngineeringInsightCollectionMode)
	seven.Attempts[0].Score.Correctness = 1
	if !seven.DevelopmentGatePassed() {
		t.Fatal("substantive score of seven did not satisfy the >=6 gate")
	}
}

func TestEngineeringInsightEvaluationV2QualificationUsesDistinctThresholds(t *testing.T) {
	expected := v2QualificationExpectation()
	receipt := v2Receipt(expected, EngineeringInsightQualificationMode)
	report, err := ValidateEngineeringInsightEvaluationReceipt(receipt, expected)
	if err != nil || report.Outcome != EngineeringInsightPassedOutcome {
		t.Fatalf("valid v2 qualification receipt = %+v, %v", report, err)
	}
	receipt.Attempts[0].Score.Correctness = 0
	receipt.Attempts[0].Score.LocalRelevance = 0
	receipt.Attempts[0].Score.TradeoffClarity = 0
	for index := 1; index < 4; index++ {
		receipt.Attempts[index].Score.Correctness = 0
		receipt.Attempts[index].Score.LocalRelevance = 0
		receipt.Attempts[index].Score.TradeoffClarity = 0
	}
	report, err = ValidateEngineeringInsightEvaluationReceipt(receipt, expected)
	if err != nil || report.Outcome != EngineeringInsightFailedOutcome || report.UsefulSubstantiveAttempts != 12 {
		t.Fatalf("v2 qualification useful threshold = %+v, %v", report, err)
	}
}

func v2DevelopmentExpectation() EngineeringInsightEvaluationExpectation {
	return v2Expectation("development", recoveryV2DevelopmentRequests, 8, recoveryV2DevelopmentDigest)
}

func v2QualificationExpectation() EngineeringInsightEvaluationExpectation {
	return v2Expectation("qualification", recoveryV2QualificationRequests, 16, recoveryV2QualificationDigest)
}

func v2Expectation(partition string, count, substantive int, digest string) EngineeringInsightEvaluationExpectation {
	schedule := make([]EngineeringInsightExpectedAttempt, 0, count)
	for index := 0; index < count; index++ {
		intent := controlIntent
		if index < substantive {
			intent = substantiveIntent
		}
		schedule = append(schedule, EngineeringInsightExpectedAttempt{CaseName: fmt.Sprintf("v2-%s-%02d", partition, index), Partition: partition, Intent: intent, Repetition: 1, Attempt: 1})
	}
	return EngineeringInsightEvaluationExpectation{ProtocolVersion: "v2", CandidateID: recoveryV2CandidateID, Provider: recoveryV2Provider, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion, CorpusID: recoveryV2CorpusID, CorpusDigest: digest, BaseRevision: strings.Repeat("a", 40), MaxRequests: count, MaxOutputTokens: qualificationOutputTokenCap, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, Schedule: schedule}
}

func v2Receipt(expected EngineeringInsightEvaluationExpectation, mode EngineeringInsightEvaluationMode) EngineeringInsightEvaluationReceipt {
	receipt := EngineeringInsightEvaluationReceipt{ProtocolVersion: "v2", Mode: mode, RunID: "v2-run", CandidateID: expected.CandidateID, Provider: expected.Provider, Model: expected.Model, PromptVersion: expected.PromptVersion, CorpusID: expected.CorpusID, CorpusDigest: expected.CorpusDigest, BaseRevision: expected.BaseRevision, MaxRequests: expected.MaxRequests, MaxOutputTokens: expected.MaxOutputTokens, AttemptTimeoutSeconds: expected.AttemptTimeoutSeconds}
	for index, scheduled := range expected.Schedule {
		digest := fmt.Sprintf("%064x", index+1)
		optional := "present"
		if scheduled.Intent == controlIntent {
			optional = "omitted"
		}
		score := &EngineeringInsightAttemptScore{ResponseDigest: digest, Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2}
		receipt.Attempts = append(receipt.Attempts, EngineeringInsightEvaluationAttempt{CaseName: scheduled.CaseName, Partition: scheduled.Partition, Intent: scheduled.Intent, Repetition: 1, Attempt: 1, CandidateID: expected.CandidateID, Outcome: "completed", UsableSummary: true, CompleteSummary: true, OptionalInsight: optional, EmittedResponse: true, ResponseDigest: digest, OutputTokens: 100, FinishReason: "stop", ElapsedMilliseconds: float64(index + 1), Score: score})
	}
	receipt.Consumption = EngineeringInsightEvaluationConsumption{Requests: len(receipt.Attempts), OutputTokens: len(receipt.Attempts) * 100}
	return receipt
}

func qualificationExpectation() EngineeringInsightEvaluationExpectation {
	schedule := make([]EngineeringInsightExpectedAttempt, 0, 24)
	for repetition := 1; repetition <= 2; repetition++ {
		for index := 0; index < 12; index++ {
			intent := substantiveIntent
			if index >= 8 {
				intent = controlIntent
			}
			schedule = append(schedule, EngineeringInsightExpectedAttempt{CaseName: fmt.Sprintf("qualification-%02d", index), Partition: qualificationPartition, Intent: intent, Repetition: repetition, Attempt: 1})
		}
	}
	return EngineeringInsightEvaluationExpectation{CandidateID: "candidate-v1", Provider: "provider", Model: "model", PromptVersion: app.EngineeringInsightPromptVersion(), CorpusID: "engineering-insight-v1", CorpusDigest: strings.Repeat("a", 64), BaseRevision: "base6a14c01", MaxRequests: 24, MaxOutputTokens: 4096, AttemptTimeoutSeconds: 300, Schedule: schedule}
}

func qualificationReceipt() EngineeringInsightEvaluationReceipt {
	expected := qualificationExpectation()
	receipt := EngineeringInsightEvaluationReceipt{Mode: EngineeringInsightQualificationMode, RunID: "run-1", CandidateID: expected.CandidateID, Provider: expected.Provider, Model: expected.Model, PromptVersion: expected.PromptVersion, CorpusID: expected.CorpusID, CorpusDigest: expected.CorpusDigest, BaseRevision: expected.BaseRevision, MaxRequests: expected.MaxRequests, MaxOutputTokens: expected.MaxOutputTokens, AttemptTimeoutSeconds: expected.AttemptTimeoutSeconds}
	for index, scheduled := range expected.Schedule {
		digest := fmt.Sprintf("%064x", index+1)
		optional := "present"
		if scheduled.Intent == controlIntent {
			optional = "omitted"
		}
		receipt.Attempts = append(receipt.Attempts, EngineeringInsightEvaluationAttempt{CaseName: scheduled.CaseName, Partition: scheduled.Partition, Intent: scheduled.Intent, Repetition: scheduled.Repetition, Attempt: scheduled.Attempt, CandidateID: expected.CandidateID, Outcome: "completed", UsableSummary: true, CompleteSummary: true, OptionalInsight: optional, EmittedResponse: true, ResponseDigest: digest, OutputTokens: 100, FinishReason: "stop", ElapsedMilliseconds: float64(index + 1), Score: &EngineeringInsightAttemptScore{ResponseDigest: digest, Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2}})
	}
	receipt.Consumption = EngineeringInsightEvaluationConsumption{Requests: len(receipt.Attempts), OutputTokens: len(receipt.Attempts) * 100}
	return receipt
}

func degradeAttempt(attempt *EngineeringInsightEvaluationAttempt, keepUsable bool) {
	attempt.CompleteSummary = false
	attempt.UsableSummary = keepUsable
	if keepUsable {
		attempt.OptionalInsight = "rejected"
	} else {
		attempt.OptionalInsight = "not_evaluated"
	}
}

func makeNonCompletedAttempt(attempt *EngineeringInsightEvaluationAttempt, outcome, finishReason string) {
	attempt.Outcome = outcome
	attempt.FinishReason = finishReason
	attempt.UsableSummary = false
	attempt.CompleteSummary = false
	attempt.OptionalInsight = "not_evaluated"
	attempt.OptionalSectionDegraded = false
}
