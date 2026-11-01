package insighteval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
	"github.com/nanaki-93/mini-orca/v2/internal/config"
	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

const (
	EngineeringInsightCollectRunMode         = "collect"
	EngineeringInsightDevelopmentRunMode     = "development"
	EngineeringInsightQualificationRunMode   = "qualification"
	runnerRelativeDirectory                  = ".kasuri/autopilot/engineering-insight-evaluation"
	defaultDevelopmentRequestCap             = 6
	extendedDevelopmentRequestCap            = 12
	recoveryDevelopmentRequestCap            = 18
	finalDevelopmentRequestCap               = 24
	structuredDevelopmentRequestCap          = 30
	thinkingOffDevelopmentRequestCap         = 36
	thinkingSchemaDevelopmentRequestCap      = 42
	mediumDevelopmentRequestCap              = 48
	developmentGrantRequestCount             = 6
	recoveryDevelopmentAuthorizationID       = "qual05-qwen38-recovery-1"
	recoveryDevelopmentCandidateID           = "qwen38-v10-recovery-1"
	recoveryDevelopmentModel                 = "qwen/qwen3.8-27b"
	v11DevelopmentAuthorizationID            = "qual05-qwen38-schema-1"
	v11DevelopmentCandidateID                = "qwen38-v11-schema-1"
	v11DevelopmentModel                      = recoveryDevelopmentModel
	v11DevelopmentPromptVersion              = "file-analysis-v11"
	v12DevelopmentAuthorizationID            = "authqual05-qwen38-structured-1"
	v12DevelopmentCandidateID                = "qwen38-v12-structured-1"
	v12DevelopmentModel                      = recoveryDevelopmentModel
	v12DevelopmentPromptVersion              = "file-analysis-v12"
	thinkingOffDevelopmentAuthorizationID    = "qual05-qwen38-thinking-off-1"
	thinkingOffDevelopmentCandidateID        = "qwen38-v12-thinking-off-1"
	thinkingOffDevelopmentModel              = recoveryDevelopmentModel
	thinkingOffDevelopmentPromptVersion      = "file-analysis-v12"
	thinkingOffDevelopmentReasoningEffort    = "none"
	thinkingSchemaDevelopmentAuthorizationID = "qual05-qwen38-thinking-schema-1"
	thinkingSchemaDevelopmentCandidateID     = "qwen38-v12-thinking-schema-1"
	thinkingSchemaDevelopmentModel           = "./models/qwen38-v12-thinking-schema-1"
	thinkingSchemaDevelopmentPromptVersion   = "file-analysis-v12"
	thinkingSchemaDevelopmentReasoningEffort = "low"
	mediumDevelopmentAuthorizationID         = "qual05-qwen38-medium-1"
	mediumDevelopmentCandidateID             = "qwen38-v12-medium-1"
	mediumDevelopmentModel                   = "./models/qwen38-v12-medium-1"
	mediumDevelopmentPromptVersion           = "file-analysis-v12"
	mediumDevelopmentReasoningEffort         = "medium"
	recoveryV2PeriodID                       = "engineering-insight-v2-recovery-1"
	recoveryV2CandidateID                    = "qwen38-v13-recovery-1"
	recoveryV2Model                          = "./models/qwen38-v13-recovery-1"
	recoveryV2PromptVersion                  = "file-analysis-v13"
	recoveryV2CorpusID                       = "engineering-insight-v2"
	recoveryV2DevelopmentDigest              = "c85fc3c5855da261047dccd63498787efcbdec9186ab0cd9b1dfee364c6afc3f"
	recoveryV2QualificationDigest            = "7cb16ac34b337e172e97a14aa639ef00bec77fbc77085028c01e6e1fe4183229"
	recoveryV2DevelopmentRunID               = "rcv07-qwen38-v13-dev-1"
	recoveryV2QualificationRunID             = "rcv08-qwen38-v13-qual-1"
	recoveryV2DevelopmentRequests            = 12
	recoveryV2QualificationRequests          = 24
	recoveryV2DevelopmentLimit               = 60
	recoveryV2QualificationLimit             = 48
	recoveryV2RuntimeContextTokens           = 119552
	recoveryV2Provider                       = "configured-bug"
	recoveryV2Endpoint                       = "http://127.0.0.1:1235/v1"
)

var (
	ErrEngineeringInsightRunLocked     = errors.New("engineering insight evaluation is already running")
	ErrEngineeringInsightRunFinished   = errors.New("engineering insight evaluation run is already complete")
	ErrEngineeringInsightRunTerminated = errors.New("engineering insight evaluation run stopped after permanent request rejection")
	ErrEngineeringInsightRemoteDenied  = errors.New("evaluation remote provider requires explicit confirmation")
)

const permanentRequestRejectionReason = "permanent_request_rejected"

// EngineeringInsightRunnerCase is deliberately source-bearing only in memory.
// It is never included in a manifest or receipt.
type EngineeringInsightRunnerCase struct {
	Expected EngineeringInsightExpectedAttempt
	Source   string
}

// EngineeringInsightRunnerClient is the single request boundary used by the
// runner. The production client executes one transport attempt per call.
type EngineeringInsightRunnerClient interface {
	ChatWithJSONSchema(context.Context, []llm.ChatMessage, llm.JSONSchema) (*llm.ChatResponse, error)
}

type EngineeringInsightRunnerConfig struct {
	Root                  string
	RunID                 string
	Mode                  string
	CandidateID           string
	Provider              string
	Model                 string
	PromptVersion         string
	CorpusID              string
	CorpusDigest          string
	CorpusJSON            []byte
	BaseRevision          string
	Profile               config.ModelProfile
	ConfirmRemoteProvider bool
	Cases                 []EngineeringInsightRunnerCase
	Client                EngineeringInsightRunnerClient
}

// EngineeringInsightScoringHandoff keeps replies private for an independent
// reviewer. Call Discard when scoring is complete; no reply is persisted.
type EngineeringInsightScoringHandoff struct {
	RunID      string
	Responses  map[string]string
	privateDir string
}

type engineeringInsightRunManifest struct {
	Fingerprint    string                              `json:"fingerprint"`
	Finished       bool                                `json:"finished"`
	TerminalReason string                              `json:"terminal_reason,omitempty"`
	Receipt        EngineeringInsightEvaluationReceipt `json:"receipt"`
}

// engineeringInsightOptionalDiagnostics is private, source-free evidence for
// one emitted response. It deliberately remains outside the public receipt.
type engineeringInsightOptionalDiagnostics struct {
	RunID                       string                              `json:"run_id"`
	AttemptID                   string                              `json:"attempt_id"`
	ResponseDigest              string                              `json:"response_digest"`
	Insights                    []app.FileAnalysisInsightDiagnostic `json:"insights"`
	TaskSpecDegradedRiskIndices []int                               `json:"task_spec_degraded_risk_indices"`
}

type engineeringInsightCampaign struct {
	DevelopmentRequests   int `json:"development_requests"`
	QualificationRequests int `json:"qualification_requests"`
}

// engineeringInsightRecoveryV2Period is the sole successor authorization. It
// is intentionally separate from the frozen v1 campaign and its grant ledger.
// Reservations share the counter write so a crash cannot turn a charged slot
// into a new provider attempt.
type engineeringInsightRecoveryV2Period struct {
	Version                         string            `json:"version"`
	PeriodID                        string            `json:"period_id"`
	CanonicalRoot                   string            `json:"canonical_root"`
	Provider                        string            `json:"provider"`
	Endpoint                        string            `json:"endpoint"`
	CandidateID                     string            `json:"candidate_id"`
	Model                           string            `json:"model"`
	PromptVersion                   string            `json:"prompt_version"`
	CorpusID                        string            `json:"corpus_id"`
	DevelopmentCorpusDigest         string            `json:"development_corpus_digest"`
	QualificationCorpusDigest       string            `json:"qualification_corpus_digest"`
	BaseRevision                    string            `json:"base_revision"`
	SchemaIdentity                  string            `json:"schema_identity"`
	MaxOutputTokens                 int               `json:"max_output_tokens"`
	InputContextTokens              int               `json:"input_context_tokens"`
	RuntimeContextTokens            int               `json:"runtime_context_tokens"`
	AttemptTimeoutSeconds           int               `json:"attempt_timeout_seconds"`
	ReasoningEffort                 string            `json:"reasoning_effort"`
	Temperature                     float32           `json:"temperature"`
	TopP                            float32           `json:"top_p"`
	TopK                            int               `json:"top_k"`
	Lanes                           int               `json:"lanes"`
	DevelopmentRequests             int               `json:"development_requests"`
	QualificationRequests           int               `json:"qualification_requests"`
	CumulativeDevelopmentRequests   int               `json:"cumulative_development_requests"`
	CumulativeQualificationRequests int               `json:"cumulative_qualification_requests"`
	DevelopmentLimit                int               `json:"development_limit"`
	QualificationLimit              int               `json:"qualification_limit"`
	PredecessorFiles                map[string]string `json:"predecessor_files_sha256"`
	DevelopmentSchedule             []string          `json:"development_schedule"`
	QualificationSchedule           []string          `json:"qualification_schedule"`
	Reservations                    map[string]bool   `json:"reservations"`
}

var recoveryV2PredecessorFiles = map[string]string{
	"campaign.json": "d30821b8ba97a3914fbed615c01aed25ff5953a94fbec7f0de095c85df0ada2c", "development-grants.json": "e51c04994b479e2ace9e34121cf5cb2dd0e9d774ea825c6e403b1bd01d64bbd0",
	"qual05-v9-dev3-receipt.json": "f64c9a495a05f28aa2d15e6069de2293318b2ae5759dd94d48eca1fc0cacb10f", "qual05-v8-dev2-receipt.json": "32c8c6ad8654e195984747952c4f9898f1af14c664d10b2d30f830649da5346a", "qual05-v10-dev4-receipt.json": "c3a1d7c31ead7fcb329b3650767ce076baecece21ff0139cc36dc25ba6f7e0d9",
	"qual05-qwen38-thinking-off-dev1-receipt.json": "2aae836ea20ac7a8f4931a195400c5709180b18526aacfba2daa384426138b6e", "qual05-qwen38-v11-dev2-receipt.json": "13a32d8d0a10512069ea6703bdfb877b473c63660d04200e4b8a101876b60e7e", "qual05-qwen38-thinking-schema-dev1-receipt.json": "20c3354a989beb0d124f98145d47298f4ed516ee6e710dc5cf5f25555a3720c9",
	"qual05-v7-dev1-receipt.json": "f60d46fcfd3dc73c19ddfa28c1914cf1bb024ea35ed88835e508c02039d48aba", "qual05-qwen38-v12-dev2-receipt.json": "3688c2f0ba11ffa6fbf3559b31e492c9441e79b53439a1079b070afb12a8a0fc", "qual05-qwen38-dev2-receipt.json": "446f40922f5bdda1bdfab75f4137a59652d7cdf3a44162d1afd407926c8eeaff",
	"qual05-qwen38-medium-dev1-receipt.json": "6651383c177b78e7f5ea8b41bea3aea49e99ca98af5650f6452449c87205735c", "qual06-qwen38-medium-1-receipt.json": "e5f3a665d50376870369f0f577448b6df0d17fe4049a1b2289295f1816c4174e", "qual05-qwen38-dev1-receipt.json": "3088e4ab82da3cab170362db4f958233853e7c32bab3c9de2f6ecf87aaebe1c1",
	"qual05-qwen38-v12-dev1-receipt.json": "95f719680e90f8b198da1f34b5b5173a7e71a72feb863f0ddc3d2c82935b4546", "qual05-qwen38-medium-dev2-receipt.json": "260ee40baf7f76ccda776f9295afa55cae9e685becf43281368bbd971326dba2", "qual05-qwen38-thinking-off-dev2-receipt.json": "820eb7c9203a75ff6ad92428cf51c1de5b9b76970a657cc142345cdc15331d25",
	"qual05-qwen38-v11-dev1-receipt.json": "4b01d63027222a004c94b986c8c41b2f22ab04f0fda4534d7e2e78994a7d4c68", "qual05-qwen38-thinking-schema-dev2-receipt.json": "300e47e980a2cc5929a0bf5fd393061e7102ad5b919612a75c34c4790df84326",
}

type engineeringInsightDevelopmentGrantLedger struct {
	Grants []engineeringInsightDevelopmentGrant `json:"grants"`
}

type engineeringInsightDevelopmentGrant struct {
	AuthorizationID string `json:"authorization_id"`
	Requests        int    `json:"requests"`
	CandidateID     string `json:"candidate_id,omitempty"`
	Model           string `json:"model,omitempty"`
	PromptVersion   string `json:"prompt_version,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type engineeringInsightDevelopmentGrantIdentity struct {
	CandidateID     string
	Model           string
	PromptVersion   string
	ReasoningEffort string
}

type engineeringInsightDevelopmentGrantWindow struct {
	grantIndex int
	minimum    int
	maximum    int
	errorText  string
}

type engineeringInsightDevelopmentGrantRule struct {
	valid     func(string, engineeringInsightDevelopmentGrantIdentity) bool
	errorText string
}

// RunEngineeringInsightEvaluation executes the selected schedule sequentially.
// Each reservation is durable before Chat, so a process crash consumes that
// slot and resume never repeats an uncertain provider request.
func RunEngineeringInsightEvaluation(ctx context.Context, cfg EngineeringInsightRunnerConfig) (EngineeringInsightEvaluationReceipt, *EngineeringInsightScoringHandoff, error) {
	if err := validateRunnerConfig(cfg); err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}
	if err := validateRunnerDestination(cfg); err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, fmt.Errorf("create evaluation storage")
	}
	campaignLock, err := lockRunnerFile(filepath.Join(directory, "campaign.lock"))
	if err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}
	defer campaignLock.Close()
	if err := ensureEvaluationCampaign(directory); err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}
	lock, err := lockRunnerFile(filepath.Join(directory, cfg.RunID+".lock"))
	if err != nil {
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}
	defer lock.Close()

	manifestPath := filepath.Join(directory, cfg.RunID+".json")
	manifest, err := loadOrCreateRunnerManifest(manifestPath, cfg)
	if err != nil {
		if manifest.Finished {
			return manifest.Receipt, nil, err
		}
		return EngineeringInsightEvaluationReceipt{}, nil, err
	}

	handoff := &EngineeringInsightScoringHandoff{RunID: cfg.RunID, Responses: make(map[string]string), privateDir: buildPrivateResponseDirectory(directory, cfg.RunID)}
	if err := executeRemainingAttempts(ctx, cfg, directory, manifestPath, &manifest, handoff); err != nil {
		return manifest.Receipt, handoff, err
	}
	manifest.Finished = true
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		handoff.Discard()
		return manifest.Receipt, nil, err
	}
	return manifest.Receipt, handoff, nil
}
