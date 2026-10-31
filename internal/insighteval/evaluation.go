package insighteval

const (
	minimumRetainedEngineeringInsightScore = 6
	minimumUsableQualificationAttempts     = 23
	minimumCompleteQualificationAttempts   = 22
	minimumUsefulSubstantiveAttempts       = 13
	qualificationControlAttemptCount       = 8
	qualificationRequestCap                = 24
	qualificationOutputTokenCap            = 4096
	qualificationAttemptTimeoutSeconds     = 300
)

type EngineeringInsightEvaluationMode string

const (
	EngineeringInsightCollectionMode    EngineeringInsightEvaluationMode = "collection"
	EngineeringInsightQualificationMode EngineeringInsightEvaluationMode = "qualification"
)

type EngineeringInsightEvaluationOutcome string

const (
	EngineeringInsightCollectionOutcome EngineeringInsightEvaluationOutcome = "collection"
	EngineeringInsightIncompleteOutcome EngineeringInsightEvaluationOutcome = "incomplete"
	EngineeringInsightFailedOutcome     EngineeringInsightEvaluationOutcome = "failed"
	EngineeringInsightPassedOutcome     EngineeringInsightEvaluationOutcome = "passed"
)

// EngineeringInsightEvaluationReceipt stores source-free, reviewer-scored evidence.
type EngineeringInsightEvaluationReceipt struct {
	ProtocolVersion       string                                  `json:"protocol_version,omitempty"`
	Mode                  EngineeringInsightEvaluationMode        `json:"mode"`
	RunID                 string                                  `json:"run_id"`
	CandidateID           string                                  `json:"candidate_id"`
	Provider              string                                  `json:"provider"`
	Model                 string                                  `json:"model"`
	PromptVersion         string                                  `json:"prompt_version"`
	CorpusID              string                                  `json:"corpus_id"`
	CorpusDigest          string                                  `json:"corpus_digest"`
	BaseRevision          string                                  `json:"base_revision"`
	MaxRequests           int                                     `json:"max_requests"`
	MaxOutputTokens       int                                     `json:"max_output_tokens"`
	AttemptTimeoutSeconds int                                     `json:"attempt_timeout_seconds"`
	Consumption           EngineeringInsightEvaluationConsumption `json:"consumption"`
	Attempts              []EngineeringInsightEvaluationAttempt   `json:"attempts"`
}

type EngineeringInsightEvaluationConsumption struct {
	Requests     int `json:"requests"`
	OutputTokens int `json:"output_tokens"`
}

type EngineeringInsightEvaluationAttempt struct {
	CaseName                string                          `json:"case_name"`
	Partition               string                          `json:"partition"`
	Intent                  string                          `json:"intent"`
	Repetition              int                             `json:"repetition"`
	Attempt                 int                             `json:"attempt"`
	CandidateID             string                          `json:"candidate_id"`
	Outcome                 string                          `json:"outcome"`
	UsableSummary           bool                            `json:"usable_summary"`
	CompleteSummary         bool                            `json:"complete_summary"`
	OptionalInsight         string                          `json:"optional_insight"`
	OptionalSectionDegraded bool                            `json:"optional_section_degraded"`
	EmittedResponse         bool                            `json:"emitted_response"`
	ResponseDigest          string                          `json:"response_digest"`
	OutputTokens            int                             `json:"output_tokens"`
	InvalidProviderMetadata bool                            `json:"invalid_provider_metadata"`
	FinishReason            string                          `json:"finish_reason"`
	ElapsedMilliseconds     float64                         `json:"elapsed_milliseconds"`
	Score                   *EngineeringInsightAttemptScore `json:"score"`
}

// EngineeringInsightAttemptScore is joined to a response by digest only.
type EngineeringInsightAttemptScore struct {
	ResponseDigest     string `json:"response_digest"`
	Correctness        int    `json:"correctness"`
	LocalRelevance     int    `json:"local_relevance"`
	TradeoffClarity    int    `json:"tradeoff_clarity"`
	UsefulVerification int    `json:"useful_verification"`
	CriticalFalseClaim bool   `json:"critical_false_claim"`
	RetainExample      bool   `json:"retain_example"`
}

func (score EngineeringInsightAttemptScore) Total() int {
	return score.Correctness + score.LocalRelevance + score.TradeoffClarity + score.UsefulVerification
}

// DevelopmentGatePassed is deliberately stricter than historical collection
// validation. The v2 promotion gate needs an independent whole-final verdict
// for every emitted response, including intentional omission controls.
func (receipt EngineeringInsightEvaluationReceipt) DevelopmentGatePassed() bool {
	if receipt.ProtocolVersion != "v2" || receipt.Mode != EngineeringInsightCollectionMode || len(receipt.Attempts) != 12 || receipt.Consumption.Requests != 12 {
		return false
	}
	seen := make(map[string]bool, 12)
	counts := developmentGateCounts{}
	for _, attempt := range receipt.Attempts {
		if !counts.recordAttempt(attempt, seen) {
			return false
		}
	}
	return counts.isValid(len(seen))
}

type developmentGateCounts struct{ usable, complete, useful, omitted, substantive, controls int }

func (counts *developmentGateCounts) recordAttempt(attempt EngineeringInsightEvaluationAttempt, seen map[string]bool) bool {
	id := buildAttemptScoreID(attempt)
	if seen[id] || !isValidDevelopmentGateAttempt(attempt) {
		return false
	}
	seen[id] = true
	counts.usable++
	counts.complete++
	if attempt.Intent == "substantive" {
		counts.substantive++
		if attempt.OptionalInsight == "present" && attempt.Score.Total() >= minimumRetainedEngineeringInsightScore {
			counts.useful++
		}
		return true
	}
	if attempt.Intent == "control" {
		counts.controls++
		if attempt.OptionalInsight == "omitted" {
			counts.omitted++
		}
		return true
	}
	return false
}

func (counts developmentGateCounts) isValid(unique int) bool {
	return unique == 12 && counts.usable == 12 && counts.complete == 12 && counts.substantive == 8 && counts.controls == 4 && counts.useful == 8 && counts.omitted == 4
}

func isValidDevelopmentGateAttempt(attempt EngineeringInsightEvaluationAttempt) bool {
	return attempt.Partition == "development" && attempt.Repetition == 1 && attempt.Attempt == 1 && attempt.EmittedResponse && attempt.UsableSummary && attempt.CompleteSummary && attempt.Score != nil && !attempt.Score.CriticalFalseClaim
}

// EngineeringInsightExpectedAttempt is selected outside the receipt.
type EngineeringInsightExpectedAttempt struct {
	CaseName   string
	Partition  string
	Intent     string
	Repetition int
	Attempt    int
}

type EngineeringInsightEvaluationExpectation struct {
	ProtocolVersion       string
	CandidateID           string
	Provider              string
	Model                 string
	PromptVersion         string
	CorpusID              string
	CorpusDigest          string
	BaseRevision          string
	MaxRequests           int
	MaxOutputTokens       int
	AttemptTimeoutSeconds int
	Schedule              []EngineeringInsightExpectedAttempt
}

type EngineeringInsightLatencyStatistics struct {
	SuccessfulCount           int
	MedianMilliseconds        float64
	P95Milliseconds           float64
	TimeoutOrCensoredAttempts int
}

type EngineeringInsightEvaluationReport struct {
	Outcome                   EngineeringInsightEvaluationOutcome
	ScheduledAttempts         int
	RecordedAttempts          int
	UsableAttempts            int
	CompleteAttempts          int
	UsefulSubstantiveAttempts int
	OmittedControls           int
	CriticalFalseClaims       int
	InvalidProviderMetadata   int
	OutcomeCounts             map[string]int
	Latency                   EngineeringInsightLatencyStatistics
}
