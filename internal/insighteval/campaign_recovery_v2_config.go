package insighteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func hasRecoveryV2IdentityMarkers(cfg EngineeringInsightRunnerConfig) bool {
	return cfg.CandidateID == recoveryV2CandidateID || cfg.Model == recoveryV2Model || cfg.Profile.Model == recoveryV2Model || cfg.CorpusID == recoveryV2CorpusID || cfg.RunID == recoveryV2DevelopmentRunID || cfg.RunID == recoveryV2QualificationRunID
}

func isRecoveryV2RunnerConfig(cfg EngineeringInsightRunnerConfig) bool {
	if !hasSameRecoveryV2RunnerIdentity(cfg) || !hasSameRecoveryV2RunnerProfile(cfg) || !isValidRecoveryV2Corpus(cfg) {
		return false
	}
	if cfg.Mode == EngineeringInsightDevelopmentRunMode {
		return cfg.RunID == recoveryV2DevelopmentRunID && cfg.CorpusDigest == recoveryV2DevelopmentDigest
	}
	return cfg.Mode == EngineeringInsightQualificationRunMode && cfg.RunID == recoveryV2QualificationRunID && cfg.CorpusDigest == recoveryV2QualificationDigest
}

func isValidRecoveryV2Corpus(cfg EngineeringInsightRunnerConfig) bool {
	if !hasMatchingRecoveryV2CorpusDigest(cfg) || ValidateStrictJSONDocument(cfg.CorpusJSON) != nil {
		return false
	}
	corpus, ok := decodeRecoveryV2Corpus(cfg.CorpusJSON)
	return ok && hasMatchingRecoveryV2CorpusCases(corpus, cfg.Cases)
}

func hasMatchingRecoveryV2CorpusDigest(cfg EngineeringInsightRunnerConfig) bool {
	want := recoveryV2DevelopmentDigest
	if cfg.Mode == EngineeringInsightQualificationRunMode {
		want = recoveryV2QualificationDigest
	}
	sum := sha256.Sum256(cfg.CorpusJSON)
	return cfg.CorpusDigest == want && hex.EncodeToString(sum[:]) == want
}

type recoveryV2CorpusCase struct {
	Name      string `json:"name"`
	Partition string `json:"partition"`
	Intent    string `json:"intent"`
	Source    string `json:"source"`
}

func decodeRecoveryV2Corpus(data []byte) ([]recoveryV2CorpusCase, bool) {
	var corpus []recoveryV2CorpusCase
	if json.Unmarshal(data, &corpus) != nil {
		return nil, false
	}
	return corpus, true
}

func hasMatchingRecoveryV2CorpusCases(corpus []recoveryV2CorpusCase, cases []EngineeringInsightRunnerCase) bool {
	selected := make(map[string]struct{ partition, intent, source string })
	for _, item := range corpus {
		if !isValidRecoveryV2CorpusCase(item) {
			return false
		}
		if _, exists := selected[item.Name]; exists {
			return false
		}
		selected[item.Name] = struct{ partition, intent, source string }{item.Partition, item.Intent, item.Source}
	}
	if len(selected) != len(cases) {
		return false
	}
	for _, item := range cases {
		caseSpec, exists := selected[item.Expected.CaseName]
		if !exists || !isMatchingRecoveryV2SelectedCase(caseSpec, item) {
			return false
		}
	}
	return true
}

func isValidRecoveryV2CorpusCase(item recoveryV2CorpusCase) bool {
	return item.Name != "" && !strings.ContainsRune(item.Name, '\x00') && item.Source != "" && (item.Partition == "development" || item.Partition == "qualification") && (item.Intent == "substantive" || item.Intent == "control")
}

func isMatchingRecoveryV2SelectedCase(spec struct{ partition, intent, source string }, item EngineeringInsightRunnerCase) bool {
	return spec.partition == item.Expected.Partition && spec.intent == item.Expected.Intent && spec.source == item.Source && item.Expected.Repetition == 1 && item.Expected.Attempt == 1
}

func hasSameRecoveryV2RunnerIdentity(cfg EngineeringInsightRunnerConfig) bool {
	return cfg.CandidateID == recoveryV2CandidateID && cfg.Provider == recoveryV2Provider && cfg.Model == recoveryV2Model && cfg.Profile.Model == recoveryV2Model && cfg.PromptVersion == recoveryV2PromptVersion && cfg.CorpusID == recoveryV2CorpusID && isValidRecoveryV2BaseRevision(cfg.BaseRevision)
}

func hasSameRecoveryV2RunnerProfile(cfg EngineeringInsightRunnerConfig) bool {
	return cfg.Profile.APIBaseURL == recoveryV2Endpoint && cfg.Profile.ReasoningEffort == mediumDevelopmentReasoningEffort && cfg.Profile.MaxTokens == qualificationOutputTokenCap && cfg.Profile.ContextMaxTokens == 16384 && cfg.Profile.Temperature == 1 && cfg.Profile.TopP != nil && *cfg.Profile.TopP == .95 && cfg.Profile.TopK != nil && *cfg.Profile.TopK == 20 && cfg.Profile.MinP == nil && cfg.Profile.PresencePenalty == nil && cfg.Profile.RepeatPenalty == nil
}

func isValidRecoveryV2DevelopmentSchedule(cases []EngineeringInsightRunnerCase) bool {
	return isValidRecoveryV2Schedule(cases, "development", recoveryV2DevelopmentRequests, 8, 4)
}

func isValidRecoveryV2QualificationSchedule(cases []EngineeringInsightRunnerCase) bool {
	return isValidRecoveryV2Schedule(cases, "qualification", recoveryV2QualificationRequests, 16, 8)
}

func isValidRecoveryV2Schedule(cases []EngineeringInsightRunnerCase, partition string, count, substantive, controls int) bool {
	if len(cases) != count {
		return false
	}
	seen := make(map[string]bool, len(cases))
	actualSubstantive, actualControls := 0, 0
	for _, item := range cases {
		attempt := item.Expected
		if seen[attempt.CaseName] || attempt.Partition != partition || attempt.Repetition != 1 || attempt.Attempt != 1 {
			return false
		}
		seen[attempt.CaseName] = true
		if attempt.Intent == "substantive" {
			actualSubstantive++
		} else if attempt.Intent == "control" {
			actualControls++
		} else {
			return false
		}
	}
	return actualSubstantive == substantive && actualControls == controls
}
