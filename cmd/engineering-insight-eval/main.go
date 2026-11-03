// Command engineering-insight-eval validates source-free collection and
// qualification receipts. It never contacts a provider.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/insighteval"
)

func main() {
	if evaluationRunModeRequested(os.Args[1:]) {
		if err := runEvaluationMode(os.Args[1:]); err != nil {
			fail(err)
		}
		return
	}
	options, err := evaluationOptionsFromFlags()
	if err != nil {
		failWithStatus(err, 2)
	}
	receipt, err := loadReceipt(options.receiptPath)
	if err != nil {
		fail(err)
	}
	expected, err := options.expectationFor(receipt.Mode)
	if err != nil {
		fail(err)
	}
	report, err := insighteval.ValidateEngineeringInsightEvaluationReceipt(receipt, expected)
	if err != nil {
		fail(err)
	}
	fmt.Printf("%s: %d/%d usable, %d/%d complete, %d useful substantive, %d omitted controls, %d critical claims; successful latency median %.0fms p95 %.0fms (%d timeout/censored)\n", report.Outcome, report.UsableAttempts, report.ScheduledAttempts, report.CompleteAttempts, report.ScheduledAttempts, report.UsefulSubstantiveAttempts, report.OmittedControls, report.CriticalFalseClaims, report.Latency.MedianMilliseconds, report.Latency.P95Milliseconds, report.Latency.TimeoutOrCensoredAttempts)
	failWithStatus(nil, evaluationExitStatus(report.Outcome))
}

func evaluationRunModeRequested(args []string) bool {
	for index, arg := range args {
		if strings.HasPrefix(arg, "-mode=") || (arg == "-mode" && index+1 < len(args)) {
			return true
		}
	}
	return false
}

func runEvaluationMode(args []string) error {
	flags := flag.NewFlagSet("engineering-insight-eval", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "", "collect, development, qualification, authorize-recovery-v2, grant-development, handoff, score, export, or discard")
	root := flags.String("root", ".", "project root containing private evaluation state")
	runID := flags.String("run-id", "", "new or resumable evaluation run identity")
	receiptPath := flags.String("receipt", "", "source-free receipt destination")
	casesPath := flags.String("cases", "", "evaluation case set")
	configPath := flags.String("config", "config.yaml", "configured local daemon model profile")
	candidateID := flags.String("candidate-id", "", "selected candidate identity")
	provider := flags.String("provider", "", "selected provider label without endpoint or credential")
	model := flags.String("model", "", "selected model identity for a development grant")
	promptVersion := flags.String("prompt-version", "", "selected prompt identity")
	corpusID := flags.String("corpus-id", "", "selected corpus identity")
	baseRevision := flags.String("base-revision", "", "selected base revision")
	confirmRemote := flags.Bool("confirm-remote-provider", false, "confirm this exact configured remote destination")
	scoresPath := flags.String("scores", "", "digest-bound score JSON for score mode")
	authorizationID := flags.String("authorization-id", "", "unique explicit development-budget authorization")
	requests := flags.Int("requests", 0, "explicit development-budget grant request count")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid evaluation command")
	}
	if *mode == "authorize-recovery-v2" && !onlyRecoveryAuthorizationFlags(flags) {
		return fmt.Errorf("recovery authorization accepts only mode and root")
	}
	if handled, err := runAuthorizationMode(*mode, *root, *runID, *receiptPath, *casesPath, *candidateID, *provider, *model, *promptVersion, *corpusID, *baseRevision, *authorizationID, *requests); handled {
		return err
	}
	if !insighteval.ValidEngineeringInsightEvaluationRunID(*runID) {
		return fmt.Errorf("evaluation run ID is invalid")
	}
	if handled, err := runAuxiliaryEvaluationMode(*mode, *root, *runID, *receiptPath, *scoresPath); handled {
		return err
	}
	return runProviderEvaluation(providerEvaluationOptions{mode: *mode, root: *root, runID: *runID, receiptPath: *receiptPath, casesPath: *casesPath, configPath: *configPath, candidateID: *candidateID, provider: *provider, promptVersion: *promptVersion, corpusID: *corpusID, baseRevision: *baseRevision, confirmRemote: *confirmRemote})
}

func onlyRecoveryAuthorizationFlags(flags *flag.FlagSet) bool {
	seenRoot := false
	valid := true
	flags.Visit(func(item *flag.Flag) {
		if item.Name == "root" {
			seenRoot = true
		} else if item.Name != "mode" {
			valid = false
		}
	})
	return valid && seenRoot && flags.NArg() == 0
}

func runAuthorizationMode(mode, root, runID, receiptPath, casesPath, candidateID, provider, model, promptVersion, corpusID, baseRevision, authorizationID string, requests int) (bool, error) {
	if mode == "authorize-recovery-v2" {
		if root == "" || runID != "" || receiptPath != "" || casesPath != "" || candidateID != "" || provider != "" || model != "" || promptVersion != "" || corpusID != "" || baseRevision != "" || authorizationID != "" || requests != 0 {
			return true, fmt.Errorf("recovery authorization accepts only root")
		}
		return true, insighteval.AuthorizeEngineeringInsightRecoveryV2(root)
	}
	if mode != "grant-development" {
		return false, nil
	}
	return true, grantDevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
}

func grantDevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	switch authorizationID {
	case "qual05-qwen38-medium-1":
		return insighteval.GrantEngineeringInsightMediumDevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
	case "qual05-qwen38-thinking-schema-1":
		return insighteval.GrantEngineeringInsightThinkingSchemaDevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
	case "qual05-qwen38-thinking-off-1":
		return insighteval.GrantEngineeringInsightThinkingOffDevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
	case "authqual05-qwen38-structured-1":
		return insighteval.GrantEngineeringInsightV12DevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
	case "qual05-qwen38-schema-1":
		return insighteval.GrantEngineeringInsightV11DevelopmentBudget(root, authorizationID, requests, candidateID, model, promptVersion)
	case "qual05-qwen38-recovery-1":
		return insighteval.GrantEngineeringInsightRecoveryDevelopmentBudget(root, authorizationID, requests, candidateID, model)
	default:
		if candidateID == "" && model == "" && promptVersion == "" {
			return insighteval.GrantEngineeringInsightDevelopmentBudget(root, authorizationID, requests)
		}
		return fmt.Errorf("development budget grant identity is invalid")
	}
}

func runAuxiliaryEvaluationMode(mode, root, runID, receiptPath, scoresPath string) (bool, error) {
	if mode == "score" {
		return true, scoreEvaluationRun(root, runID, receiptPath, scoresPath)
	}
	if mode == "export" {
		return true, exportEvaluationRun(root, runID, receiptPath)
	}
	if mode != "handoff" && mode != "discard" {
		return false, nil
	}
	if runID == "" {
		return true, fmt.Errorf("run-id is required")
	}
	handoff, err := insighteval.LoadEngineeringInsightScoringHandoff(root, runID)
	if err != nil {
		return true, err
	}
	if mode == "discard" {
		handoff.Discard()
		return true, nil
	}
	for id := range handoff.Responses {
		fmt.Println(id)
	}
	return true, nil
}

func scoreEvaluationRun(root, runID, receiptPath, scoresPath string) error {
	if runID == "" || scoresPath == "" || receiptPath == "" {
		return fmt.Errorf("run-id, scores, and receipt are required")
	}
	data, err := os.ReadFile(scoresPath)
	if err != nil {
		return fmt.Errorf("read evaluation scores")
	}
	scores, err := insighteval.DecodeEngineeringInsightScores(data)
	if err != nil {
		return err
	}
	receipt, err := insighteval.StoreEngineeringInsightScores(root, runID, scores)
	if err != nil {
		return err
	}
	return writeEvaluationReceipt(receiptPath, receipt)
}

func exportEvaluationRun(root, runID, receiptPath string) error {
	if runID == "" || receiptPath == "" {
		return fmt.Errorf("run-id and receipt are required")
	}
	receipt, err := insighteval.LoadEngineeringInsightEvaluationReceipt(root, runID)
	if err != nil {
		return err
	}
	return writeEvaluationReceipt(receiptPath, receipt)
}

func writeEvaluationReceipt(path string, receipt insighteval.EngineeringInsightEvaluationReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("write evaluation receipt")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("write evaluation receipt")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write evaluation receipt")
	}
	return nil
}

func evaluationExitStatus(outcome insighteval.EngineeringInsightEvaluationOutcome) int {
	if outcome == insighteval.EngineeringInsightCollectionOutcome || outcome == insighteval.EngineeringInsightPassedOutcome {
		return 0
	}
	if outcome == insighteval.EngineeringInsightIncompleteOutcome {
		return 2
	}
	return 1
}

func fail(err error) { failWithStatus(err, 1) }

func failWithStatus(err error, status int) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "engineering-insight-eval:", err)
	}
	if status != 0 {
		os.Exit(status)
	}
}
