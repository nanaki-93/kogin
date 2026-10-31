package insighteval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

func executeRemainingAttempts(ctx context.Context, cfg EngineeringInsightRunnerConfig, directory, manifestPath string, manifest *engineeringInsightRunManifest, handoff *EngineeringInsightScoringHandoff) error {
	for index, item := range cfg.Cases {
		if index < len(manifest.Receipt.Attempts) {
			continue
		}
		dispatch, err := reserveRunnerAttempt(directory, cfg, item.Expected)
		if err != nil {
			return err
		}
		// A reservation is recorded as unknown before prompt delivery. If this
		// process dies after this write, resume leaves the consumed slot intact.
		manifest.Receipt.Attempts = append(manifest.Receipt.Attempts, buildUnknownReservedAttempt(item.Expected, cfg.CandidateID))
		manifest.Receipt.Consumption.Requests++
		if err := storeRunnerJSON(manifestPath, manifest); err != nil {
			return err
		}
		if !dispatch {
			continue
		}

		attempt, response, diagnostics, err := executeRunnerAttempt(ctx, cfg, item)
		permanentRejection := errors.Is(err, llm.ErrStructuredRequestRejected)
		if err != nil && !permanentRejection {
			attempt = buildFailedRunnerAttempt(item.Expected, cfg.CandidateID, err)
		}
		if response != "" {
			// Publish the source-free diagnostic before the raw private response.
			// A failure between either immutable write and the receipt update leaves
			// the reserved attempt unknown, and resume intentionally never replays it.
			if err := storePrivateOptionalDiagnostics(directory, engineeringInsightOptionalDiagnostics{RunID: cfg.RunID, AttemptID: buildExpectedAttemptID(item.Expected), ResponseDigest: attempt.ResponseDigest, Insights: diagnostics.Insights, TaskSpecDegradedRiskIndices: diagnostics.TaskSpecDegradedRiskIndices}); err != nil {
				return err
			}
			if err := storePrivateResponse(handoff.privateDir, buildExpectedAttemptID(item.Expected), response); err != nil {
				return err
			}
		}
		manifest.Receipt.Attempts[index] = attempt
		manifest.Receipt.Consumption.OutputTokens += attempt.OutputTokens
		if permanentRejection {
			manifest.Finished = true
			manifest.TerminalReason = permanentRequestRejectionReason
		}
		if err := storeRunnerJSON(manifestPath, manifest); err != nil {
			return err
		}
		if response != "" {
			handoff.Responses[buildExpectedAttemptID(item.Expected)] = response
		}
		if permanentRejection {
			return ErrEngineeringInsightRunTerminated
		}
	}
	return nil
}

func reserveRunnerAttempt(directory string, cfg EngineeringInsightRunnerConfig, expected EngineeringInsightExpectedAttempt) (bool, error) {
	if isRecoveryV2RunnerConfig(cfg) {
		return reserveRecoveryV2Attempt(directory, cfg, expected)
	}
	path := filepath.Join(directory, "campaign.json")
	campaign, err := loadEvaluationCampaign(path)
	if err != nil {
		return false, fmt.Errorf("read evaluation campaign")
	}
	if cfg.Mode == EngineeringInsightQualificationRunMode {
		if campaign.QualificationRequests >= qualificationRequestCap {
			return false, fmt.Errorf("qualification request budget is exhausted")
		}
		campaign.QualificationRequests++
	} else {
		cap, err := loadDevelopmentRequestCap(directory, campaign.DevelopmentRequests, cfg)
		if err != nil {
			return false, err
		}
		if campaign.DevelopmentRequests >= cap {
			return false, fmt.Errorf("development request budget is exhausted")
		}
		campaign.DevelopmentRequests++
	}
	return true, storeRunnerJSON(path, campaign)
}

func ensureEvaluationCampaign(directory string) error {
	path := filepath.Join(directory, "campaign.json")
	if _, err := loadEvaluationCampaign(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read evaluation campaign")
	}
	runs, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil || len(runs) != 0 {
		return fmt.Errorf("read evaluation campaign")
	}
	return storeRunnerJSON(path, engineeringInsightCampaign{})
}

func loadEvaluationCampaign(path string) (engineeringInsightCampaign, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return engineeringInsightCampaign{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || fields["development_requests"] == nil || fields["qualification_requests"] == nil {
		return engineeringInsightCampaign{}, fmt.Errorf("invalid campaign")
	}
	var campaign engineeringInsightCampaign
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&campaign) != nil || campaign.DevelopmentRequests < 0 || campaign.DevelopmentRequests > mediumDevelopmentRequestCap || campaign.QualificationRequests < 0 || campaign.QualificationRequests > qualificationRequestCap {
		return engineeringInsightCampaign{}, fmt.Errorf("invalid campaign")
	}
	return campaign, nil
}

func buildUnknownReservedAttempt(expected EngineeringInsightExpectedAttempt, candidate string) EngineeringInsightEvaluationAttempt {
	return EngineeringInsightEvaluationAttempt{CaseName: expected.CaseName, Partition: expected.Partition, Intent: expected.Intent, Repetition: expected.Repetition, Attempt: expected.Attempt, CandidateID: candidate, Outcome: "unknown", OptionalInsight: "not_evaluated", FinishReason: "unknown"}
}
