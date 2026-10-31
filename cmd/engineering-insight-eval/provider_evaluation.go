package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/insighteval"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

type providerEvaluationOptions struct {
	mode, root, runID, receiptPath, casesPath, configPath, candidateID, provider, promptVersion, corpusID, baseRevision string
	confirmRemote                                                                                                       bool
}

func runProviderEvaluation(options providerEvaluationOptions) error {
	if !validProviderEvaluationOptions(options) {
		return fmt.Errorf("mode, root, run-id, receipt, cases, config, candidate-id, provider, prompt-version, corpus-id, and base-revision are required")
	}
	profile, err := evaluationBugProfile(options.configPath)
	if err != nil {
		return err
	}
	actualBase, err := evaluationBaseRevision(options.root)
	if err != nil || actualBase != options.baseRevision {
		return fmt.Errorf("evaluation base revision is not the clean current candidate")
	}
	casesData, err := os.ReadFile(options.casesPath)
	if err != nil {
		return fmt.Errorf("read evaluation cases")
	}
	runnerCases, err := runnerCasesForMode(casesData, options.mode, options.corpusID)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(casesData)
	receipt, _, err := insighteval.RunEngineeringInsightEvaluation(context.Background(), insighteval.EngineeringInsightRunnerConfig{Root: options.root, RunID: options.runID, Mode: options.mode, CandidateID: options.candidateID, Provider: options.provider, Model: profile.Model, PromptVersion: options.promptVersion, CorpusID: options.corpusID, CorpusDigest: hex.EncodeToString(digest[:]), CorpusJSON: casesData, BaseRevision: options.baseRevision, Profile: profile, ConfirmRemoteProvider: options.confirmRemote, Cases: runnerCases, Client: llm.NewEvaluationClient(profile)})
	if err != nil {
		return err
	}
	return writeEvaluationReceipt(options.receiptPath, receipt)
}

func validProviderEvaluationOptions(options providerEvaluationOptions) bool {
	if !validRunnerModeForCommand(options.mode) || options.promptVersion != app.EngineeringInsightPromptVersion() {
		return false
	}
	for _, value := range []string{options.root, options.runID, options.receiptPath, options.casesPath, options.configPath, options.candidateID, options.provider, options.corpusID, options.baseRevision} {
		if value == "" {
			return false
		}
	}
	return true
}
func validRunnerModeForCommand(mode string) bool {
	return mode == insighteval.EngineeringInsightCollectRunMode || mode == insighteval.EngineeringInsightDevelopmentRunMode || mode == insighteval.EngineeringInsightQualificationRunMode
}

func evaluationBaseRevision(root string) (string, error) {
	head := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := head.Output()
	if err != nil {
		return "", err
	}
	status := exec.Command("git", "-C", root, "status", "--porcelain")
	dirty, err := status.Output()
	if err != nil || !permittedEvaluationDirtyState(string(dirty)) {
		return "", fmt.Errorf("candidate is dirty")
	}
	return strings.TrimSpace(string(output)), nil
}

func permittedEvaluationDirtyState(status string) bool {
	status = strings.TrimSpace(status)
	return status == "" || status == "M PLAN.md" || status == " M PLAN.md"
}

func evaluationBugProfile(path string) (config.ModelProfile, error) {
	cfg, err := config.LoadFromYAML(path)
	if err != nil {
		return config.ModelProfile{}, fmt.Errorf("load evaluation configuration")
	}
	profiles, err := config.ResolveModelProfiles(cfg)
	if err != nil {
		return config.ModelProfile{}, fmt.Errorf("load evaluation configuration")
	}
	profile := profiles.Bug
	profile.MaxTokens = 4096
	return profile, nil
}

type runnerCaseDocument struct {
	Name      string `json:"name"`
	Partition string `json:"partition"`
	Intent    string `json:"intent"`
	Source    string `json:"source"`
}

func runnerCasesForMode(data []byte, mode, corpusID string) ([]insighteval.EngineeringInsightRunnerCase, error) {
	if err := insighteval.ValidateStrictJSONDocument(data); err != nil {
		return nil, fmt.Errorf("invalid evaluation cases")
	}
	var document []runnerCaseDocument
	if json.Unmarshal(data, &document) != nil {
		return nil, fmt.Errorf("invalid evaluation cases")
	}
	selected := make([]runnerCaseDocument, 0, 24)
	for _, item := range document {
		if !validRunnerCaseDocument(item) {
			return nil, fmt.Errorf("invalid evaluation cases")
		}
		if selectsRunnerCase(mode, item.Partition) {
			selected = append(selected, item)
		}
	}
	if corpusID == "engineering-insight-v2" {
		if mode == insighteval.EngineeringInsightQualificationRunMode {
			if len(selected) != 24 {
				return nil, fmt.Errorf("evaluation cases do not define v2 qualification")
			}
			return repeatedRunnerCases(selected, 1), nil
		}
		if len(selected) != 12 {
			return nil, fmt.Errorf("evaluation cases do not define v2 development")
		}
		return repeatedRunnerCases(selected, 1), nil
	}
	if mode == insighteval.EngineeringInsightQualificationRunMode {
		if len(selected) != 12 {
			return nil, fmt.Errorf("evaluation cases do not define qualification")
		}
		return repeatedRunnerCases(selected, 2), nil
	}
	if len(selected) != 3 {
		return nil, fmt.Errorf("evaluation cases do not define development")
	}
	return repeatedRunnerCases(selected, 1), nil
}

func validRunnerCaseDocument(item runnerCaseDocument) bool {
	return item.Name != "" && item.Source != "" && validCasePartition(item.Partition) && validCaseIntent(item.Intent)
}
func selectsRunnerCase(mode, partition string) bool {
	if mode == insighteval.EngineeringInsightQualificationRunMode {
		return partition == "qualification"
	}
	return partition == "development"
}

func repeatedRunnerCases(cases []runnerCaseDocument, repetitions int) []insighteval.EngineeringInsightRunnerCase {
	result := make([]insighteval.EngineeringInsightRunnerCase, 0, len(cases)*repetitions)
	for repetition := 1; repetition <= repetitions; repetition++ {
		for _, item := range cases {
			result = append(result, insighteval.EngineeringInsightRunnerCase{Expected: insighteval.EngineeringInsightExpectedAttempt{CaseName: item.Name, Partition: item.Partition, Intent: item.Intent, Repetition: repetition, Attempt: 1}, Source: item.Source})
		}
	}
	return result
}
