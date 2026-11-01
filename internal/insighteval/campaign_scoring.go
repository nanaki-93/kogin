package insighteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// StoreEngineeringInsightScores joins independent scores to the durable receipt
// by response digest. It never accepts or persists reviewer prose.
func StoreEngineeringInsightScores(root, runID string, scores map[string]EngineeringInsightAttemptScore) (EngineeringInsightEvaluationReceipt, error) {
	if !isValidRunnerID(runID) {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("evaluation run ID is invalid")
	}
	if _, err := LoadEngineeringInsightScoringHandoff(root, runID); err != nil {
		return EngineeringInsightEvaluationReceipt{}, err
	}
	directory := filepath.Join(root, runnerRelativeDirectory)
	lock, err := lockRunnerFile(filepath.Join(directory, runID+".lock"))
	if err != nil {
		return EngineeringInsightEvaluationReceipt{}, err
	}
	defer lock.Close()
	path := filepath.Join(directory, runID+".json")
	manifest, exists, err := loadRunManifest(path)
	if err != nil || !exists || !manifest.Finished {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("evaluation run is not ready for scoring")
	}
	if err := validateAndApplyEngineeringInsightScores(&manifest.Receipt, scores); err != nil {
		return EngineeringInsightEvaluationReceipt{}, err
	}
	if err := storeRunnerJSON(path, manifest); err != nil {
		return EngineeringInsightEvaluationReceipt{}, err
	}
	return manifest.Receipt, nil
}

func validateAndApplyEngineeringInsightScores(receipt *EngineeringInsightEvaluationReceipt, scores map[string]EngineeringInsightAttemptScore) error {
	byID := make(map[string]*EngineeringInsightEvaluationAttempt, len(receipt.Attempts))
	for index := range receipt.Attempts {
		attempt := &receipt.Attempts[index]
		byID[buildAttemptScoreID(*attempt)] = attempt
	}
	for id, score := range scores {
		attempt, exists := byID[id]
		if !exists {
			return fmt.Errorf("evaluation score is outside the selected schedule")
		}
		if !attempt.EmittedResponse || score.ResponseDigest != attempt.ResponseDigest || !isValidScore(score) {
			return fmt.Errorf("evaluation score does not match a retained response")
		}
		copy := score
		attempt.Score = &copy
	}
	return nil
}

func buildAttemptScoreID(attempt EngineeringInsightEvaluationAttempt) string {
	return buildExpectedAttemptID(EngineeringInsightExpectedAttempt{CaseName: attempt.CaseName, Partition: attempt.Partition, Intent: attempt.Intent, Repetition: attempt.Repetition, Attempt: attempt.Attempt})
}

// LoadEngineeringInsightEvaluationReceipt exports only the source-free receipt.
func LoadEngineeringInsightEvaluationReceipt(root, runID string) (EngineeringInsightEvaluationReceipt, error) {
	if !isValidRunnerID(runID) {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("evaluation run ID is invalid")
	}
	manifest, exists, err := loadRunManifest(filepath.Join(root, runnerRelativeDirectory, runID+".json"))
	if err != nil || !exists || !manifest.Finished {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("evaluation run is not ready for export")
	}
	return manifest.Receipt, nil
}

func buildPrivateResponseDirectory(directory, runID string) string {
	return filepath.Join(directory, "responses", runID)
}

func buildPrivateOptionalDiagnosticsDirectory(directory, runID string) string {
	return filepath.Join(directory, "optional-diagnostics", runID)
}

func buildPrivateResponsePath(directory, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(directory, hex.EncodeToString(sum[:])+".response")
}

func buildPrivateOptionalDiagnosticsPath(directory, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(directory, hex.EncodeToString(sum[:])+".json")
}

func storePrivateResponse(directory, id, response string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("write private evaluation response")
	}
	if err := storePrivateArtifact(buildPrivateResponsePath(directory, id), []byte(response)); err != nil {
		return fmt.Errorf("write private evaluation response")
	}
	return nil
}

func storePrivateOptionalDiagnostics(directory string, diagnostics engineeringInsightOptionalDiagnostics) error {
	if !isValidRunnerID(diagnostics.RunID) || diagnostics.AttemptID == "" || !isValidDigest(diagnostics.ResponseDigest) || !isValidPrivateOptionalDiagnostics(diagnostics) {
		return fmt.Errorf("write private optional diagnostics")
	}
	privateDirectory := buildPrivateOptionalDiagnosticsDirectory(directory, diagnostics.RunID)
	if err := os.MkdirAll(privateDirectory, 0700); err != nil {
		return fmt.Errorf("write private optional diagnostics")
	}
	data, err := json.Marshal(diagnostics)
	if err != nil {
		return fmt.Errorf("write private optional diagnostics")
	}
	if err := storePrivateArtifact(buildPrivateOptionalDiagnosticsPath(privateDirectory, diagnostics.AttemptID), data); err != nil {
		return fmt.Errorf("write private optional diagnostics")
	}
	return nil
}

func isValidPrivateOptionalDiagnostics(record engineeringInsightOptionalDiagnostics) bool {
	previousIndex := -1
	for _, index := range record.TaskSpecDegradedRiskIndices {
		if index < 0 || index <= previousIndex {
			return false
		}
		previousIndex = index
	}
	for _, diagnostic := range record.Insights {
		if !isValidPrivateOptionalDiagnosticReason(diagnostic.Reason) || !isValidPrivateOptionalDiagnosticPresence(diagnostic.Presence) || !isValidPrivateOptionalDiagnosticLocation(diagnostic.Location, diagnostic.Index) {
			return false
		}
		for _, field := range []project.OptionalEngineeringInsightFieldDiagnostic{diagnostic.Mechanism, diagnostic.WhyItMattersHere, diagnostic.TradeoffOrFailureMode, diagnostic.TransferableLesson} {
			if field.Runes < 0 {
				return false
			}
		}
	}
	return true
}

func isValidPrivateOptionalDiagnosticReason(reason project.OptionalEngineeringInsightReason) bool {
	switch reason {
	case project.OptionalEngineeringInsightAbsent, project.OptionalEngineeringInsightNull, project.OptionalEngineeringInsightInvalidShape, project.OptionalEngineeringInsightEmptyRequiredField, project.OptionalEngineeringInsightOverLimit, project.OptionalEngineeringInsightAccepted:
		return true
	default:
		return false
	}
}

func isValidPrivateOptionalDiagnosticPresence(presence project.OptionalEngineeringInsightPresence) bool {
	return presence == project.OptionalEngineeringInsightAbsentPresence || presence == project.OptionalEngineeringInsightNullPresence || presence == project.OptionalEngineeringInsightValuePresence
}

func isValidPrivateOptionalDiagnosticLocation(location string, index *int) bool {
	switch location {
	case "top_level":
		return index == nil
	case "risk", "suggestion":
		return index != nil && *index >= 0
	default:
		return false
	}
}

// storePrivateArtifact publishes a complete immutable file. Link creation
// fails if a resumed or uncertain attempt already owns this artifact, so a
// later process can never replace evidence from a prior dispatch.
func storePrivateArtifact(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".private-evaluation-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Link(temporaryName, path)
}

func loadPrivateResponse(directory, id string) (string, error) {
	data, err := os.ReadFile(buildPrivateResponsePath(directory, id))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
