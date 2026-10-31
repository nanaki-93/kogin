package insighteval

import (
	"math"
	"sort"
)

func buildEvaluationReport(expected EngineeringInsightEvaluationExpectation, recorded map[string]EngineeringInsightEvaluationAttempt) EngineeringInsightEvaluationReport {
	report := buildEmptyEvaluationReport(expected, recorded)
	var successful []float64
	for _, scheduled := range expected.Schedule {
		attempt, present := recorded[buildExpectedAttemptID(scheduled)]
		if present {
			successful = buildRecordedEvaluationAttempt(&report, scheduled, attempt, successful)
		}
	}
	report.Latency.SuccessfulCount = len(successful)
	report.Latency.MedianMilliseconds = buildMedianLatency(successful)
	report.Latency.P95Milliseconds = buildNearestRankLatency(successful, 0.95)
	return report
}

func buildEmptyEvaluationReport(expected EngineeringInsightEvaluationExpectation, recorded map[string]EngineeringInsightEvaluationAttempt) EngineeringInsightEvaluationReport {
	return EngineeringInsightEvaluationReport{ScheduledAttempts: len(expected.Schedule), RecordedAttempts: len(recorded), OutcomeCounts: map[string]int{"completed": 0, "timeout": 0, "malformed": 0, "truncated": 0, "abstained": 0, "failed": 0, "budget_exhausted": 0, "unknown": 0}}
}

func buildRecordedEvaluationAttempt(report *EngineeringInsightEvaluationReport, scheduled EngineeringInsightExpectedAttempt, attempt EngineeringInsightEvaluationAttempt, successful []float64) []float64 {
	report.OutcomeCounts[attempt.Outcome]++
	if attempt.InvalidProviderMetadata {
		report.InvalidProviderMetadata++
	}
	buildSummaryCounts(report, attempt)
	buildScoreCounts(report, scheduled, attempt)
	if attempt.Outcome == "completed" && attempt.UsableSummary {
		return append(successful, attempt.ElapsedMilliseconds)
	}
	report.Latency.TimeoutOrCensoredAttempts++
	return successful
}

func buildSummaryCounts(report *EngineeringInsightEvaluationReport, attempt EngineeringInsightEvaluationAttempt) {
	if attempt.UsableSummary {
		report.UsableAttempts++
	}
	if attempt.CompleteSummary {
		report.CompleteAttempts++
	}
}

func buildScoreCounts(report *EngineeringInsightEvaluationReport, scheduled EngineeringInsightExpectedAttempt, attempt EngineeringInsightEvaluationAttempt) {
	if attempt.Score != nil && attempt.Score.CriticalFalseClaim {
		report.CriticalFalseClaims++
	}
	if scheduled.Intent == "substantive" && hasUsefulSubstantiveInsight(attempt) {
		report.UsefulSubstantiveAttempts++
	}
	if scheduled.Intent == "control" && attempt.UsableSummary && attempt.OptionalInsight == "omitted" {
		report.OmittedControls++
	}
}

func hasUsefulSubstantiveInsight(attempt EngineeringInsightEvaluationAttempt) bool {
	return attempt.UsableSummary && attempt.OptionalInsight == "present" && attempt.Score != nil && attempt.Score.Total() >= minimumRetainedEngineeringInsightScore
}

func buildNearestRankLatency(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[int(math.Ceil(percentile*float64(len(sorted))))-1]
}

func buildMedianLatency(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}
