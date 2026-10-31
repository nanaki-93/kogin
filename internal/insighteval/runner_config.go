package insighteval

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

func validateRunnerDestination(cfg EngineeringInsightRunnerConfig) error {
	if app.FileAnalysisEvaluationProvider(cfg.Profile).RemoteProvider && !cfg.ConfirmRemoteProvider {
		return ErrEngineeringInsightRemoteDenied
	}
	return nil
}

func validateRunnerConfig(cfg EngineeringInsightRunnerConfig) error {
	if hasRecoveryV2IdentityMarkers(cfg) && !isRecoveryV2RunnerConfig(cfg) {
		return fmt.Errorf("recovery evaluation identity is invalid")
	}
	if !isValidRunnerIdentity(cfg) {
		return fmt.Errorf("evaluation runner configuration is incomplete")
	}
	if !isValidRunnerMode(cfg.Mode) {
		return fmt.Errorf("evaluation runner mode is invalid")
	}
	if cfg.PromptVersion != app.EngineeringInsightPromptVersion() {
		return fmt.Errorf("evaluation runner prompt identity is invalid")
	}
	if _, err := app.FileAnalysisResponseSchema().Identity(); err != nil {
		return fmt.Errorf("evaluation runner response schema is invalid: %w", err)
	}
	if isRecoveryV2RunnerConfig(cfg) {
		return validateRecoveryV2RunnerConfig(cfg)
	}
	return validateHistoricalRunnerConfig(cfg)
}

func validateHistoricalRunnerConfig(cfg EngineeringInsightRunnerConfig) error {
	if cfg.Mode == EngineeringInsightQualificationRunMode && len(cfg.Cases) != qualificationRequestCap {
		return fmt.Errorf("qualification runner requires 24 attempts")
	}
	if cfg.Mode != EngineeringInsightQualificationRunMode && len(cfg.Cases) > 6 {
		return fmt.Errorf("development runner exceeds its request budget")
	}
	if !isValidRunnerCases(cfg.Cases) {
		return fmt.Errorf("evaluation runner schedule is invalid")
	}
	if cfg.Mode == EngineeringInsightQualificationRunMode {
		schedule := make([]EngineeringInsightExpectedAttempt, len(cfg.Cases))
		for index, item := range cfg.Cases {
			schedule[index] = item.Expected
		}
		if err := validateQualificationSchedule(schedule); err != nil {
			return fmt.Errorf("qualification runner schedule is invalid")
		}
	} else if !isValidDevelopmentRunnerSchedule(cfg.Cases, cfg.Mode) {
		return fmt.Errorf("development runner schedule is invalid")
	}
	return nil
}

func validateRecoveryV2RunnerConfig(cfg EngineeringInsightRunnerConfig) error {
	if cfg.Mode == EngineeringInsightDevelopmentRunMode && !isValidRecoveryV2DevelopmentSchedule(cfg.Cases) {
		return fmt.Errorf("recovery development runner schedule is invalid")
	}
	if cfg.Mode == EngineeringInsightQualificationRunMode && !isValidRecoveryV2QualificationSchedule(cfg.Cases) {
		return fmt.Errorf("recovery qualification runner schedule is invalid")
	}
	return nil
}

func isValidRunnerIdentity(cfg EngineeringInsightRunnerConfig) bool {
	return strings.TrimSpace(cfg.Root) != "" && isValidRunnerID(cfg.RunID) && cfg.Client != nil && cfg.Profile.Scope == config.BugModelScope && len(cfg.Cases) != 0
}

// ValidEngineeringInsightEvaluationRunID reports whether a run identifier can be
// used as a single private-state file name.
func ValidEngineeringInsightEvaluationRunID(runID string) bool {
	return isValidRunnerID(runID)
}

func isValidRunnerID(runID string) bool {
	if len(runID) == 0 || len(runID) > 96 || !isASCIILetterOrDigit(runID[0]) {
		return false
	}
	for index := 1; index < len(runID); index++ {
		if !isASCIILetterOrDigit(runID[index]) && runID[index] != '-' && runID[index] != '_' {
			return false
		}
	}
	return true
}

func isASCIILetterOrDigit(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func isValidRunnerMode(mode string) bool {
	return mode == EngineeringInsightCollectRunMode || mode == EngineeringInsightDevelopmentRunMode || mode == EngineeringInsightQualificationRunMode
}

func isValidRunnerCases(cases []EngineeringInsightRunnerCase) bool {
	seen := make(map[string]bool, len(cases))
	for _, item := range cases {
		id := buildExpectedAttemptID(item.Expected)
		if seen[id] || !isValidExpectedAttempt(item.Expected) || strings.TrimSpace(item.Source) == "" {
			return false
		}
		seen[id] = true
	}
	return true
}

func isValidDevelopmentRunnerSchedule(cases []EngineeringInsightRunnerCase, mode string) bool {
	wantRepetitions := 1
	if len(cases) != 3*wantRepetitions {
		return false
	}
	seen := make(map[string]map[int]bool, 3)
	for _, item := range cases {
		attempt := item.Expected
		if attempt.Partition != "development" || (attempt.Intent != "substantive" && attempt.Intent != "control") || attempt.Attempt != 1 || attempt.Repetition != 1 {
			return false
		}
		if seen[attempt.CaseName] == nil {
			seen[attempt.CaseName] = make(map[int]bool, wantRepetitions)
		}
		if seen[attempt.CaseName][attempt.Repetition] {
			return false
		}
		seen[attempt.CaseName][attempt.Repetition] = true
	}
	if len(seen) != 3 {
		return false
	}
	for _, repetitions := range seen {
		if len(repetitions) != wantRepetitions {
			return false
		}
	}
	return true
}
