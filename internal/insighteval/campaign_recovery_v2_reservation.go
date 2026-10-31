package insighteval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
)

func reserveRecoveryV2Attempt(directory string, cfg EngineeringInsightRunnerConfig, expected EngineeringInsightExpectedAttempt) (bool, error) {
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		return false, err
	}
	if err := validateRecoveryV2ReservationBoundary(directory, cfg, period); err != nil {
		return false, err
	}
	id, err := bindRecoveryV2Schedule(&period, cfg, expected)
	if err != nil {
		return false, err
	}
	if period.Reservations[id] {
		return false, nil
	}
	if err := consumeRecoveryV2Request(&period, cfg.Mode); err != nil {
		return false, err
	}
	period.Reservations[id] = true
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		return false, err
	}
	return true, nil
}

func validateRecoveryV2ReservationBoundary(directory string, cfg EngineeringInsightRunnerConfig, period engineeringInsightRecoveryV2Period) error {
	actualBase, err := loadCleanCanonicalRecoveryBase(period.CanonicalRoot)
	if err != nil || actualBase != period.BaseRevision || cfg.BaseRevision != period.BaseRevision {
		return fmt.Errorf("recovery evaluation base changed after authorization")
	}
	if err := validateRecoveryV2Predecessors(period.CanonicalRoot); err != nil {
		return err
	}
	if cfg.Mode == EngineeringInsightQualificationRunMode && !isRecoveryV2DevelopmentAccepted(directory, period) {
		return fmt.Errorf("recovery qualification requires accepted development evidence")
	}
	return nil
}

func bindRecoveryV2Schedule(period *engineeringInsightRecoveryV2Period, cfg EngineeringInsightRunnerConfig, expected EngineeringInsightExpectedAttempt) (string, error) {
	schedule := buildRecoveryV2ScheduleIDs(cfg.Cases)
	boundSchedule := &period.DevelopmentSchedule
	if cfg.Mode == EngineeringInsightQualificationRunMode {
		boundSchedule = &period.QualificationSchedule
	}
	if len(*boundSchedule) == 0 {
		*boundSchedule = schedule
	} else if !isSameStringSchedule(*boundSchedule, schedule) {
		return "", fmt.Errorf("recovery evaluation schedule changed after reservation")
	}
	id := buildExpectedAttemptID(expected)
	if !hasScheduledAttemptID(*boundSchedule, id) {
		return "", fmt.Errorf("recovery evaluation attempt is outside the authorized schedule")
	}
	return id, nil
}

func consumeRecoveryV2Request(period *engineeringInsightRecoveryV2Period, mode string) error {
	if mode == EngineeringInsightQualificationRunMode {
		if period.QualificationRequests >= recoveryV2QualificationRequests {
			return fmt.Errorf("recovery qualification request budget is exhausted")
		}
		period.QualificationRequests++
		period.CumulativeQualificationRequests++
		return nil
	}
	if period.DevelopmentRequests >= recoveryV2DevelopmentRequests {
		return fmt.Errorf("recovery development request budget is exhausted")
	}
	period.DevelopmentRequests++
	period.CumulativeDevelopmentRequests++
	return nil
}

func loadRecoveryV2Period(directory, root string) (engineeringInsightRecoveryV2Period, error) {
	data, err := os.ReadFile(buildRecoveryV2PeriodPath(directory))
	if err != nil {
		return engineeringInsightRecoveryV2Period{}, fmt.Errorf("recovery successor period is not authorized")
	}
	if ValidateStrictJSONDocument(data) != nil {
		return engineeringInsightRecoveryV2Period{}, fmt.Errorf("recovery successor period is invalid")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || !hasValidRecoveryV2PeriodFields(fields) {
		return engineeringInsightRecoveryV2Period{}, fmt.Errorf("recovery successor period is invalid")
	}
	var period engineeringInsightRecoveryV2Period
	if json.Unmarshal(data, &period) != nil || !isValidRecoveryV2Period(period, root) {
		return engineeringInsightRecoveryV2Period{}, fmt.Errorf("recovery successor period is invalid")
	}
	return period, nil
}

func hasValidRecoveryV2PeriodFields(fields map[string]json.RawMessage) bool {
	want := []string{"version", "period_id", "canonical_root", "provider", "endpoint", "candidate_id", "model", "prompt_version", "corpus_id", "development_corpus_digest", "qualification_corpus_digest", "base_revision", "schema_identity", "max_output_tokens", "input_context_tokens", "runtime_context_tokens", "attempt_timeout_seconds", "reasoning_effort", "temperature", "top_p", "top_k", "lanes", "development_requests", "qualification_requests", "cumulative_development_requests", "cumulative_qualification_requests", "development_limit", "qualification_limit", "predecessor_files_sha256", "development_schedule", "qualification_schedule", "reservations"}
	return isValidRequiredJSONObject(fields, want)
}

func isValidRecoveryV2Period(period engineeringInsightRecoveryV2Period, root string) bool {
	canonicalRoot, err := loadCanonicalEvaluationRoot(root)
	schemaIdentity, schemaErr := app.FileAnalysisResponseSchema().Identity()
	if err != nil || schemaErr != nil || !hasSameRecoveryV2PeriodIdentity(period, canonicalRoot) || !hasSameRecoveryV2PeriodProfile(period, schemaIdentity) || !isValidRecoveryV2PeriodAccounting(period) {
		return false
	}
	if !hasSameRecoveryV2Predecessors(period.PredecessorFiles) || !hasValidRecoveryV2PeriodSchedules(period) {
		return false
	}
	developmentReservations, qualificationReservations, valid := buildRecoveryV2ReservationCounts(period)
	return valid && developmentReservations == period.DevelopmentRequests && qualificationReservations == period.QualificationRequests
}

func hasSameRecoveryV2Predecessors(actual map[string]string) bool {
	for name, digest := range recoveryV2PredecessorFiles {
		if actual[name] != digest {
			return false
		}
	}
	return true
}

func hasValidRecoveryV2PeriodSchedules(period engineeringInsightRecoveryV2Period) bool {
	return isValidBoundRecoveryV2Schedule(period.DevelopmentSchedule, "development", recoveryV2DevelopmentRequests, 8, 4, period.DevelopmentRequests) && isValidBoundRecoveryV2Schedule(period.QualificationSchedule, "qualification", recoveryV2QualificationRequests, 16, 8, period.QualificationRequests)
}

func buildRecoveryV2ReservationCounts(period engineeringInsightRecoveryV2Period) (int, int, bool) {
	allowed := make(map[string]string, len(period.DevelopmentSchedule)+len(period.QualificationSchedule))
	for _, id := range period.DevelopmentSchedule {
		allowed[id] = "development"
	}
	for _, id := range period.QualificationSchedule {
		allowed[id] = "qualification"
	}
	developmentReservations, qualificationReservations := 0, 0
	for id, reserved := range period.Reservations {
		partition, authorized := allowed[id]
		if !reserved || !authorized {
			return 0, 0, false
		}
		switch partition {
		case "development":
			developmentReservations++
		case "qualification":
			qualificationReservations++
		default:
			return 0, 0, false
		}
	}
	return developmentReservations, qualificationReservations, true
}

func isValidBoundRecoveryV2Schedule(schedule []string, partition string, count, substantive, controls, consumed int) bool {
	if len(schedule) == 0 {
		return consumed == 0
	}
	if len(schedule) != count {
		return false
	}
	seen := make(map[string]bool, count)
	actualSubstantive, actualControls := 0, 0
	for _, id := range schedule {
		parts := strings.Split(id, "\x00")
		if len(parts) != 5 || parts[0] == "" || parts[1] != partition || parts[3] != "1" || parts[4] != "1" || seen[id] {
			return false
		}
		seen[id] = true
		switch parts[2] {
		case "substantive":
			actualSubstantive++
		case "control":
			actualControls++
		default:
			return false
		}
	}
	return actualSubstantive == substantive && actualControls == controls
}

func buildRecoveryV2ScheduleIDs(cases []EngineeringInsightRunnerCase) []string {
	result := make([]string, len(cases))
	for index, item := range cases {
		result[index] = buildExpectedAttemptID(item.Expected)
	}
	return result
}

func isSameStringSchedule(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hasScheduledAttemptID(schedule []string, id string) bool {
	for _, scheduled := range schedule {
		if scheduled == id {
			return true
		}
	}
	return false
}

func hasSameRecoveryV2PeriodIdentity(period engineeringInsightRecoveryV2Period, root string) bool {
	return period.Version == "v2" && period.PeriodID == recoveryV2PeriodID && period.CanonicalRoot == root && period.Provider == recoveryV2Provider && period.Endpoint == recoveryV2Endpoint && period.CandidateID == recoveryV2CandidateID && period.Model == recoveryV2Model && period.PromptVersion == recoveryV2PromptVersion && period.CorpusID == recoveryV2CorpusID && period.DevelopmentCorpusDigest == recoveryV2DevelopmentDigest && period.QualificationCorpusDigest == recoveryV2QualificationDigest && isValidRecoveryV2BaseRevision(period.BaseRevision)
}

func hasSameRecoveryV2PeriodProfile(period engineeringInsightRecoveryV2Period, schemaIdentity string) bool {
	return period.SchemaIdentity == schemaIdentity && period.MaxOutputTokens == qualificationOutputTokenCap && period.InputContextTokens == 16384 && period.RuntimeContextTokens == recoveryV2RuntimeContextTokens && period.AttemptTimeoutSeconds == qualificationAttemptTimeoutSeconds && period.ReasoningEffort == mediumDevelopmentReasoningEffort && period.Temperature == 1 && period.TopP == .95 && period.TopK == 20 && period.Lanes == 1
}

func isValidRecoveryV2PeriodAccounting(period engineeringInsightRecoveryV2Period) bool {
	return period.DevelopmentLimit == recoveryV2DevelopmentLimit && period.QualificationLimit == recoveryV2QualificationLimit && period.DevelopmentRequests >= 0 && period.DevelopmentRequests <= recoveryV2DevelopmentRequests && period.QualificationRequests >= 0 && period.QualificationRequests <= recoveryV2QualificationRequests && period.CumulativeDevelopmentRequests == 48+period.DevelopmentRequests && period.CumulativeQualificationRequests == 24+period.QualificationRequests && len(period.PredecessorFiles) == len(recoveryV2PredecessorFiles)
}

func isRecoveryV2DevelopmentAccepted(directory string, period engineeringInsightRecoveryV2Period) bool {
	manifest, exists, err := loadRunManifest(filepath.Join(directory, recoveryV2DevelopmentRunID+".json"))
	if err != nil || !exists || !manifest.Finished || len(period.DevelopmentSchedule) != recoveryV2DevelopmentRequests || len(manifest.Receipt.Attempts) != recoveryV2DevelopmentRequests {
		return false
	}
	schedule := make([]EngineeringInsightExpectedAttempt, len(period.DevelopmentSchedule))
	for index, id := range period.DevelopmentSchedule {
		parts := strings.Split(id, "\x00")
		schedule[index] = EngineeringInsightExpectedAttempt{CaseName: parts[0], Partition: parts[1], Intent: parts[2], Repetition: 1, Attempt: 1}
	}
	expected := EngineeringInsightEvaluationExpectation{ProtocolVersion: "v2", CandidateID: period.CandidateID, Provider: period.Provider, Model: period.Model, PromptVersion: period.PromptVersion, CorpusID: period.CorpusID, CorpusDigest: period.DevelopmentCorpusDigest, BaseRevision: period.BaseRevision, MaxRequests: recoveryV2DevelopmentRequests, MaxOutputTokens: period.MaxOutputTokens, AttemptTimeoutSeconds: period.AttemptTimeoutSeconds, Schedule: schedule}
	if _, err := ValidateEngineeringInsightEvaluationReceipt(manifest.Receipt, expected); err != nil || !manifest.Receipt.DevelopmentGatePassed() {
		return false
	}
	return manifest.Receipt.CandidateID == period.CandidateID && manifest.Receipt.CorpusDigest == period.DevelopmentCorpusDigest
}
