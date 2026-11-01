package insighteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

func loadOrCreateRunnerManifest(path string, cfg EngineeringInsightRunnerConfig) (engineeringInsightRunManifest, error) {
	fingerprint := buildRunnerFingerprint(cfg)
	manifest, exists, err := loadRunManifest(path)
	if err != nil {
		return engineeringInsightRunManifest{}, err
	}
	if !exists {
		manifest = engineeringInsightRunManifest{Fingerprint: fingerprint, Receipt: buildRunnerReceipt(cfg)}
		if err := storeRunnerJSON(path, manifest); err != nil {
			return engineeringInsightRunManifest{}, err
		}
		return manifest, nil
	}
	if manifest.Fingerprint != fingerprint || validateRunnerManifest(manifest, cfg) != nil {
		return engineeringInsightRunManifest{}, fmt.Errorf("evaluation run configuration changed")
	}
	if manifest.Finished {
		return manifest, buildCompletedRunnerManifestError(manifest)
	}
	return manifest, nil
}

func buildCompletedRunnerManifestError(manifest engineeringInsightRunManifest) error {
	if manifest.TerminalReason != "" {
		return ErrEngineeringInsightRunTerminated
	}
	return ErrEngineeringInsightRunFinished
}

func buildRunnerReceipt(cfg EngineeringInsightRunnerConfig) EngineeringInsightEvaluationReceipt {
	mode := EngineeringInsightCollectionMode
	maxRequests := defaultDevelopmentRequestCap
	if isRecoveryV2RunnerConfig(cfg) {
		if cfg.Mode == EngineeringInsightQualificationRunMode {
			mode, maxRequests = EngineeringInsightQualificationMode, recoveryV2QualificationRequests
		} else {
			maxRequests = recoveryV2DevelopmentRequests
		}
		return EngineeringInsightEvaluationReceipt{ProtocolVersion: "v2", Mode: mode, RunID: cfg.RunID, CandidateID: cfg.CandidateID, Provider: cfg.Provider, Model: cfg.Model, PromptVersion: cfg.PromptVersion, CorpusID: cfg.CorpusID, CorpusDigest: cfg.CorpusDigest, BaseRevision: cfg.BaseRevision, MaxRequests: maxRequests, MaxOutputTokens: qualificationOutputTokenCap, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds}
	}
	if cfg.Mode == EngineeringInsightQualificationRunMode {
		mode, maxRequests = EngineeringInsightQualificationMode, qualificationRequestCap
	}
	return EngineeringInsightEvaluationReceipt{Mode: mode, RunID: cfg.RunID, CandidateID: cfg.CandidateID, Provider: cfg.Provider, Model: cfg.Model, PromptVersion: cfg.PromptVersion, CorpusID: cfg.CorpusID, CorpusDigest: cfg.CorpusDigest, BaseRevision: cfg.BaseRevision, MaxRequests: maxRequests, MaxOutputTokens: qualificationOutputTokenCap, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds}
}

func buildRunnerFingerprint(cfg EngineeringInsightRunnerConfig) string {
	// This private digest binds endpoint, credentials, context limit and the
	// immutable public identity without ever writing those values to a receipt.
	// validateRunnerConfig verifies the embedded schema before this digest is
	// used to create or resume a manifest.
	schemaIdentity, _ := app.FileAnalysisResponseSchema().Identity()
	return buildRunnerFingerprintWithSchemaIdentity(cfg, schemaIdentity)
}

func buildRunnerFingerprintWithSchemaIdentity(cfg EngineeringInsightRunnerConfig, schemaIdentity string) string {
	values := []string{cfg.Mode, cfg.CandidateID, cfg.Provider, cfg.Model, cfg.PromptVersion, schemaIdentity, cfg.CorpusID, cfg.CorpusDigest, cfg.BaseRevision, string(cfg.Profile.Scope), cfg.Profile.APIBaseURL, cfg.Profile.APIKey, cfg.Profile.Model, cfg.Profile.ReasoningEffort, fmt.Sprint(cfg.Profile.Temperature), buildFingerprintOptionalFloat(cfg.Profile.TopP), buildFingerprintOptionalInt(cfg.Profile.TopK), buildFingerprintOptionalFloat(cfg.Profile.MinP), buildFingerprintOptionalFloat(cfg.Profile.PresencePenalty), buildFingerprintOptionalFloat(cfg.Profile.RepeatPenalty), fmt.Sprint(cfg.Profile.ContextMaxTokens)}
	for _, item := range cfg.Cases {
		source := sha256.Sum256([]byte(item.Source))
		values = append(values, buildExpectedAttemptID(item.Expected), hex.EncodeToString(source[:]))
	}
	value := strings.Join(values, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func buildFingerprintOptionalFloat(value *float32) string {
	if value == nil {
		return "unset"
	}
	return fmt.Sprintf("%g", *value)
}

func buildFingerprintOptionalInt(value *int) string {
	if value == nil {
		return "unset"
	}
	return fmt.Sprint(*value)
}

func loadRunManifest(path string) (engineeringInsightRunManifest, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return engineeringInsightRunManifest{}, false, nil
	}
	if err != nil || json.Unmarshal(data, &engineeringInsightRunManifest{}) != nil {
		return engineeringInsightRunManifest{}, false, fmt.Errorf("read evaluation run")
	}
	var manifest engineeringInsightRunManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return engineeringInsightRunManifest{}, false, fmt.Errorf("read evaluation run")
	}
	return manifest, true, nil
}

func validateRunnerManifest(manifest engineeringInsightRunManifest, cfg EngineeringInsightRunnerConfig) error {
	want := buildRunnerReceipt(cfg)
	if !hasSameRunnerReceiptIdentity(manifest.Receipt, want) || len(manifest.Receipt.Attempts) > len(cfg.Cases) || manifest.Receipt.Consumption.Requests != len(manifest.Receipt.Attempts) {
		return fmt.Errorf("invalid evaluation manifest")
	}
	if manifest.TerminalReason != "" && (!manifest.Finished || manifest.TerminalReason != permanentRequestRejectionReason || len(manifest.Receipt.Attempts) == 0) {
		return fmt.Errorf("invalid evaluation manifest")
	}
	if !hasValidManifestAttempts(manifest.Receipt, cfg.Cases) {
		return fmt.Errorf("invalid evaluation manifest")
	}
	return nil
}

func hasSameRunnerReceiptIdentity(actual, expected EngineeringInsightEvaluationReceipt) bool {
	return actual.ProtocolVersion == expected.ProtocolVersion && actual.Mode == expected.Mode && actual.RunID == expected.RunID && actual.CandidateID == expected.CandidateID && actual.Provider == expected.Provider && actual.Model == expected.Model && actual.PromptVersion == expected.PromptVersion && actual.CorpusID == expected.CorpusID && actual.CorpusDigest == expected.CorpusDigest && actual.BaseRevision == expected.BaseRevision && actual.MaxRequests == expected.MaxRequests && actual.MaxOutputTokens == expected.MaxOutputTokens && actual.AttemptTimeoutSeconds == expected.AttemptTimeoutSeconds
}

func hasValidManifestAttempts(receipt EngineeringInsightEvaluationReceipt, cases []EngineeringInsightRunnerCase) bool {
	tokens := 0
	for index, attempt := range receipt.Attempts {
		actual := EngineeringInsightExpectedAttempt{CaseName: attempt.CaseName, Partition: attempt.Partition, Intent: attempt.Intent, Repetition: attempt.Repetition, Attempt: attempt.Attempt}
		if buildExpectedAttemptID(cases[index].Expected) != buildExpectedAttemptID(actual) || validateAttempt(attempt, EngineeringInsightCollectionMode) != nil {
			return false
		}
		tokens += attempt.OutputTokens
	}
	return tokens == receipt.Consumption.OutputTokens
}

// Keep private paths and serialization details out of CLI errors while retaining
// the underlying operation for internal error inspection.
type evaluationMetadataWriteError struct {
	cause error
}

func (e *evaluationMetadataWriteError) Error() string { return "write evaluation run" }

func (e *evaluationMetadataWriteError) Unwrap() error { return e.cause }

func storeRunnerJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return &evaluationMetadataWriteError{cause: fmt.Errorf("marshal evaluation metadata: %w", err)}
	}
	// Evaluation callers establish storage before acquiring their locks. Do not
	// let the shared writer initialize a missing evaluation directory implicitly.
	directory, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return &evaluationMetadataWriteError{cause: fmt.Errorf("inspect evaluation directory: %w", err)}
	}
	if !directory.IsDir() {
		return &evaluationMetadataWriteError{cause: errors.New("evaluation storage is not a directory")}
	}
	if err := storage.WriteFile(path, data, 0600); err != nil {
		return &evaluationMetadataWriteError{cause: err}
	}
	return nil
}
