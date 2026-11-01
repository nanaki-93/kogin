package insighteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/app"
)

func (handoff *EngineeringInsightScoringHandoff) Discard() {
	for key := range handoff.Responses {
		handoff.Responses[key] = ""
		delete(handoff.Responses, key)
	}
	if isValidRunnerID(handoff.RunID) && handoff.privateDir != "" {
		_ = os.RemoveAll(handoff.privateDir)
	}
}

// AuthorizeEngineeringInsightRecoveryV2 records the only reviewed successor
// period. It has no caller-controlled candidate, cap, corpus, or identity.
func AuthorizeEngineeringInsightRecoveryV2(root string) error {
	canonicalRoot, err := loadCanonicalEvaluationRoot(root)
	if err != nil {
		return fmt.Errorf("recovery evaluation root is invalid")
	}
	baseRevision, err := loadCleanCanonicalRecoveryBase(canonicalRoot)
	if err != nil {
		return fmt.Errorf("recovery evaluation base is not a clean canonical checkout")
	}
	if err := validateRecoveryV2Predecessors(canonicalRoot); err != nil {
		return err
	}
	if err := validateRecoveryCoordinatorBoundary(canonicalRoot); err != nil {
		return err
	}
	schemaIdentity, err := app.FileAnalysisResponseSchema().Identity()
	if err != nil {
		return fmt.Errorf("recovery response schema is invalid")
	}
	directory := filepath.Join(canonicalRoot, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create evaluation storage")
	}
	lock, err := lockRunnerFile(filepath.Join(directory, "campaign.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	path := buildRecoveryV2PeriodPath(directory)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("recovery successor period is already authorized")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read recovery successor period")
	}
	period := engineeringInsightRecoveryV2Period{
		Version: "v2", PeriodID: recoveryV2PeriodID, CanonicalRoot: canonicalRoot,
		CandidateID: recoveryV2CandidateID, Provider: recoveryV2Provider, Endpoint: recoveryV2Endpoint, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion,
		CorpusID: recoveryV2CorpusID, DevelopmentCorpusDigest: recoveryV2DevelopmentDigest,
		QualificationCorpusDigest: recoveryV2QualificationDigest, BaseRevision: baseRevision,
		SchemaIdentity: schemaIdentity, MaxOutputTokens: qualificationOutputTokenCap, InputContextTokens: 16384, RuntimeContextTokens: recoveryV2RuntimeContextTokens,
		AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, ReasoningEffort: mediumDevelopmentReasoningEffort,
		Temperature: 1, TopP: .95, TopK: 20, Lanes: 1,
		DevelopmentLimit: recoveryV2DevelopmentLimit, QualificationLimit: recoveryV2QualificationLimit,
		CumulativeDevelopmentRequests: 48, CumulativeQualificationRequests: 24,
		PredecessorFiles: buildRecoveryV2PredecessorFiles(), DevelopmentSchedule: []string{}, QualificationSchedule: []string{}, Reservations: make(map[string]bool),
	}
	return storeRunnerJSON(path, period)
}

func loadCleanCanonicalRecoveryBase(root string) (string, error) {
	worktree, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil || strings.TrimSpace(string(worktree)) != root {
		return "", fmt.Errorf("not canonical")
	}
	gitDir, err := exec.Command("git", "-C", root, "rev-parse", "--absolute-git-dir").Output()
	if err != nil || strings.TrimSpace(string(gitDir)) != filepath.Join(root, ".git") {
		return "", fmt.Errorf("linked worktree")
	}
	branch, err := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(branch)) != "codex/autopilot" {
		return "", fmt.Errorf("wrong branch")
	}
	status, err := exec.Command("git", "-C", root, "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil || strings.TrimSpace(string(status)) != "" {
		return "", fmt.Errorf("unclean checkout")
	}
	base, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil || !isValidRecoveryV2BaseRevision(strings.TrimSpace(string(base))) {
		return "", fmt.Errorf("invalid head")
	}
	return strings.TrimSpace(string(base)), nil
}

func validateRecoveryCoordinatorBoundary(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".kasuri", "autopilot", "coordinator", "RCV-recovery.json"))
	if err != nil || ValidateStrictJSONDocument(data) != nil {
		return fmt.Errorf("recovery coordinator boundary is invalid")
	}
	var boundary struct {
		Phase           string `json:"phase"`
		SchedulerStatus string `json:"scheduler_status"`
		Tasks           map[string]struct {
			Phase string `json:"phase"`
		} `json:"tasks"`
	}
	if json.Unmarshal(data, &boundary) != nil || boundary.Phase != "RCV-07" || boundary.SchedulerStatus != "PAUSED" {
		return fmt.Errorf("recovery coordinator boundary is inactive")
	}
	for _, task := range []string{"RCV-01", "RCV-02", "RCV-03", "RCV-04", "RCV-05", "RCV-06"} {
		if boundary.Tasks[task].Phase != "complete" {
			return fmt.Errorf("recovery coordinator boundary is incomplete")
		}
	}
	return nil
}

func validateRecoveryV2Predecessors(root string) error {
	directory := filepath.Join(root, runnerRelativeDirectory)
	for name, want := range recoveryV2PredecessorFiles {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("recovery predecessor evidence is missing")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("recovery predecessor evidence changed")
		}
	}
	return nil
}

func isValidRecoveryV2BaseRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func buildRecoveryV2PeriodPath(directory string) string {
	return filepath.Join(directory, recoveryV2PeriodID+".json")
}

func loadCanonicalEvaluationRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func buildRecoveryV2PredecessorFiles() map[string]string {
	result := make(map[string]string, len(recoveryV2PredecessorFiles))
	for name, digest := range recoveryV2PredecessorFiles {
		result[name] = digest
	}
	return result
}

// GrantEngineeringInsightDevelopmentBudget records the original append-only
// recovery authorization. It remains unbound because it predates dispatch
// identity binding.
func GrantEngineeringInsightDevelopmentBudget(root, authorizationID string, requests int) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{})
}

// GrantEngineeringInsightRecoveryDevelopmentBudget records the one approved
// model-bound recovery authorization. Dispatch validates this identity again
// before any request from the grant is reserved.
func GrantEngineeringInsightRecoveryDevelopmentBudget(root, authorizationID string, requests int, candidateID, model string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model})
}

// GrantEngineeringInsightV11DevelopmentBudget records the one approved v11
// model-bound extension. Its prompt identity is checked again at dispatch.
func GrantEngineeringInsightV11DevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model, PromptVersion: promptVersion})
}

// GrantEngineeringInsightV12DevelopmentBudget records the one approved
// schema-constrained pilot extension with fixed dispatch identity.
func GrantEngineeringInsightV12DevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model, PromptVersion: promptVersion})
}

// GrantEngineeringInsightThinkingOffDevelopmentBudget records the one approved
// thinking-disabled extension with its fixed structured dispatch identity.
func GrantEngineeringInsightThinkingOffDevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model, PromptVersion: promptVersion, ReasoningEffort: thinkingOffDevelopmentReasoningEffort})
}

// GrantEngineeringInsightThinkingSchemaDevelopmentBudget records the one
// approved thinking-enabled structured-output extension. Its fixed dispatch
// identity is checked again before every request from the grant is reserved.
func GrantEngineeringInsightThinkingSchemaDevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model, PromptVersion: promptVersion, ReasoningEffort: thinkingSchemaDevelopmentReasoningEffort})
}

// GrantEngineeringInsightMediumDevelopmentBudget records the one approved
// medium-reasoning recovery extension with its fixed structured dispatch identity.
func GrantEngineeringInsightMediumDevelopmentBudget(root, authorizationID string, requests int, candidateID, model, promptVersion string) error {
	return grantEngineeringInsightDevelopmentBudget(root, authorizationID, requests, engineeringInsightDevelopmentGrantIdentity{CandidateID: candidateID, Model: model, PromptVersion: promptVersion, ReasoningEffort: mediumDevelopmentReasoningEffort})
}

func grantEngineeringInsightDevelopmentBudget(root, authorizationID string, requests int, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) error {
	if !isValidDevelopmentGrantRequest(root, authorizationID, requests) {
		return fmt.Errorf("development budget grant is invalid")
	}
	directory := filepath.Join(root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create evaluation storage")
	}
	lock, err := lockRunnerFile(filepath.Join(directory, "campaign.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := ensureEvaluationCampaign(directory); err != nil {
		return err
	}
	return storeDevelopmentGrant(directory, authorizationID, requests, dispatchIdentity)
}

func isValidDevelopmentGrantRequest(root, authorizationID string, requests int) bool {
	return strings.TrimSpace(root) != "" && isValidRunnerID(authorizationID) && requests == developmentGrantRequestCount
}

func storeDevelopmentGrant(directory, authorizationID string, requests int, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) error {
	ledger, _, err := loadDevelopmentGrantLedger(directory)
	if err != nil {
		return err
	}
	grant, err := buildNextDevelopmentGrant(ledger, authorizationID, requests, dispatchIdentity)
	if err != nil {
		return err
	}
	campaign, err := loadEvaluationCampaign(filepath.Join(directory, "campaign.json"))
	if err != nil {
		return fmt.Errorf("read evaluation campaign")
	}
	priorCap := buildDevelopmentGrantCap(ledger)
	if campaign.DevelopmentRequests != priorCap {
		return fmt.Errorf("development prior budget is not exhausted")
	}
	ledger.Grants = append(ledger.Grants, grant)
	if err := storeRunnerJSON(buildDevelopmentGrantLedgerPath(directory), ledger); err != nil {
		return err
	}
	return nil
}

func buildNextDevelopmentGrant(ledger engineeringInsightDevelopmentGrantLedger, authorizationID string, requests int, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) (engineeringInsightDevelopmentGrant, error) {
	if hasDevelopmentGrantAuthorization(ledger, authorizationID) {
		return engineeringInsightDevelopmentGrant{}, fmt.Errorf("development budget grant already exists")
	}
	if len(ledger.Grants) >= len(engineeringInsightDevelopmentGrantRules) {
		return engineeringInsightDevelopmentGrant{}, fmt.Errorf("development request budget extension is exhausted")
	}
	rule := engineeringInsightDevelopmentGrantRules[len(ledger.Grants)]
	if !rule.valid(authorizationID, dispatchIdentity) {
		return engineeringInsightDevelopmentGrant{}, fmt.Errorf("%s", rule.errorText)
	}
	return engineeringInsightDevelopmentGrant{AuthorizationID: authorizationID, Requests: requests, CandidateID: dispatchIdentity.CandidateID, Model: dispatchIdentity.Model, PromptVersion: dispatchIdentity.PromptVersion, ReasoningEffort: dispatchIdentity.ReasoningEffort}, nil
}

var engineeringInsightDevelopmentGrantRules = []engineeringInsightDevelopmentGrantRule{
	{valid: isValidOriginalDevelopmentGrant, errorText: "development budget grant identity is invalid"},
	{valid: isValidRecoveryDevelopmentGrant, errorText: "development recovery grant is invalid"},
	{valid: isValidV11DevelopmentGrant, errorText: "development v11 grant is invalid"},
	{valid: isValidV12DevelopmentGrant, errorText: "development structured-output grant is invalid"},
	{valid: isValidThinkingOffDevelopmentGrant, errorText: "development thinking-off grant is invalid"},
	{valid: isValidThinkingSchemaDevelopmentGrant, errorText: "development thinking-schema grant is invalid"},
	{valid: isValidMediumDevelopmentGrant, errorText: "development medium-reasoning grant is invalid"},
}

func hasDevelopmentGrantAuthorization(ledger engineeringInsightDevelopmentGrantLedger, authorizationID string) bool {
	for _, grant := range ledger.Grants {
		if grant.AuthorizationID == authorizationID {
			return true
		}
	}
	return false
}

func isValidOriginalDevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID != recoveryDevelopmentAuthorizationID && authorizationID != v11DevelopmentAuthorizationID && authorizationID != v12DevelopmentAuthorizationID && authorizationID != thinkingOffDevelopmentAuthorizationID && authorizationID != thinkingSchemaDevelopmentAuthorizationID && authorizationID != mediumDevelopmentAuthorizationID && dispatchIdentity == (engineeringInsightDevelopmentGrantIdentity{})
}

func isValidRecoveryDevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == recoveryDevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == recoveryDevelopmentCandidateID &&
		dispatchIdentity.Model == recoveryDevelopmentModel &&
		dispatchIdentity.PromptVersion == "" &&
		dispatchIdentity.ReasoningEffort == ""
}

func isValidV11DevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == v11DevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == v11DevelopmentCandidateID &&
		dispatchIdentity.Model == v11DevelopmentModel &&
		dispatchIdentity.PromptVersion == v11DevelopmentPromptVersion &&
		dispatchIdentity.ReasoningEffort == ""
}

func isValidV12DevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == v12DevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == v12DevelopmentCandidateID &&
		dispatchIdentity.Model == v12DevelopmentModel &&
		dispatchIdentity.PromptVersion == v12DevelopmentPromptVersion &&
		dispatchIdentity.ReasoningEffort == ""
}

func isValidThinkingOffDevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == thinkingOffDevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == thinkingOffDevelopmentCandidateID &&
		dispatchIdentity.Model == thinkingOffDevelopmentModel &&
		dispatchIdentity.PromptVersion == thinkingOffDevelopmentPromptVersion &&
		dispatchIdentity.ReasoningEffort == thinkingOffDevelopmentReasoningEffort
}

func isValidThinkingSchemaDevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == thinkingSchemaDevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == thinkingSchemaDevelopmentCandidateID &&
		dispatchIdentity.Model == thinkingSchemaDevelopmentModel &&
		dispatchIdentity.PromptVersion == thinkingSchemaDevelopmentPromptVersion &&
		dispatchIdentity.ReasoningEffort == thinkingSchemaDevelopmentReasoningEffort
}

func isValidMediumDevelopmentGrant(authorizationID string, dispatchIdentity engineeringInsightDevelopmentGrantIdentity) bool {
	return authorizationID == mediumDevelopmentAuthorizationID &&
		dispatchIdentity.CandidateID == mediumDevelopmentCandidateID &&
		dispatchIdentity.Model == mediumDevelopmentModel &&
		dispatchIdentity.PromptVersion == mediumDevelopmentPromptVersion &&
		dispatchIdentity.ReasoningEffort == mediumDevelopmentReasoningEffort
}

func buildDevelopmentGrantCap(ledger engineeringInsightDevelopmentGrantLedger) int {
	cap := defaultDevelopmentRequestCap
	for _, grant := range ledger.Grants {
		cap += grant.Requests
	}
	return cap
}

// LoadEngineeringInsightScoringHandoff reads private replies for a completed
// run. The caller must discard it after scoring; this never touches receipts.
func LoadEngineeringInsightScoringHandoff(root, runID string) (*EngineeringInsightScoringHandoff, error) {
	if !isValidRunnerID(runID) {
		return nil, fmt.Errorf("evaluation run ID is invalid")
	}
	directory := filepath.Join(root, runnerRelativeDirectory)
	manifest, exists, err := loadRunManifest(filepath.Join(directory, runID+".json"))
	if err != nil || !exists || !manifest.Finished {
		return nil, fmt.Errorf("evaluation run is not ready for scoring")
	}
	handoff := &EngineeringInsightScoringHandoff{RunID: runID, Responses: make(map[string]string), privateDir: buildPrivateResponseDirectory(directory, runID)}
	for _, attempt := range manifest.Receipt.Attempts {
		if !attempt.EmittedResponse {
			continue
		}
		id := buildExpectedAttemptID(EngineeringInsightExpectedAttempt{CaseName: attempt.CaseName, Partition: attempt.Partition, Intent: attempt.Intent, Repetition: attempt.Repetition, Attempt: attempt.Attempt})
		response, err := loadPrivateResponse(handoff.privateDir, id)
		if err != nil {
			return nil, fmt.Errorf("read private evaluation response")
		}
		digest := sha256.Sum256([]byte(response))
		if hex.EncodeToString(digest[:]) != attempt.ResponseDigest {
			return nil, fmt.Errorf("private evaluation response does not match receipt")
		}
		handoff.Responses[id] = response
	}
	return handoff, nil
}

func loadDevelopmentRequestCap(directory string, consumed int, cfg EngineeringInsightRunnerConfig) (int, error) {
	ledger, _, err := loadDevelopmentGrantLedger(directory)
	if err != nil {
		return 0, err
	}
	cap := buildDevelopmentGrantCap(ledger)
	if cap > mediumDevelopmentRequestCap {
		return 0, fmt.Errorf("invalid development grant ledger")
	}
	if err := validateDevelopmentGrantDispatch(ledger.Grants, consumed, cfg); err != nil {
		return 0, err
	}
	return cap, nil
}

func validateDevelopmentGrantDispatch(grants []engineeringInsightDevelopmentGrant, consumed int, cfg EngineeringInsightRunnerConfig) error {
	for _, window := range []engineeringInsightDevelopmentGrantWindow{
		{grantIndex: 1, minimum: extendedDevelopmentRequestCap, maximum: recoveryDevelopmentRequestCap, errorText: "development recovery grant does not match dispatch identity"},
		{grantIndex: 2, minimum: recoveryDevelopmentRequestCap, maximum: finalDevelopmentRequestCap, errorText: "development v11 grant does not match dispatch identity"},
		{grantIndex: 3, minimum: finalDevelopmentRequestCap, maximum: structuredDevelopmentRequestCap, errorText: "development structured-output grant does not match dispatch identity"},
		{grantIndex: 4, minimum: structuredDevelopmentRequestCap, maximum: thinkingOffDevelopmentRequestCap, errorText: "development thinking-off grant does not match dispatch identity"},
		{grantIndex: 5, minimum: thinkingOffDevelopmentRequestCap, maximum: thinkingSchemaDevelopmentRequestCap, errorText: "development thinking-schema grant does not match dispatch identity"},
		{grantIndex: 6, minimum: thinkingSchemaDevelopmentRequestCap, errorText: "development medium-reasoning grant does not match dispatch identity"},
	} {
		if len(grants) > window.grantIndex && consumed >= window.minimum && (window.maximum == 0 || consumed < window.maximum) && !isMatchingDevelopmentGrantDispatch(cfg, grants[window.grantIndex]) {
			return fmt.Errorf("%s", window.errorText)
		}
	}
	return nil
}

func isMatchingDevelopmentGrantDispatch(cfg EngineeringInsightRunnerConfig, grant engineeringInsightDevelopmentGrant) bool {
	return cfg.CandidateID == grant.CandidateID &&
		cfg.Model == grant.Model &&
		cfg.Profile.Model == grant.Model &&
		(grant.PromptVersion == "" || cfg.PromptVersion == grant.PromptVersion) &&
		(grant.ReasoningEffort == "" || cfg.Profile.ReasoningEffort == grant.ReasoningEffort) &&
		!app.FileAnalysisEvaluationProvider(cfg.Profile).RemoteProvider
}

func buildDevelopmentGrantLedgerPath(directory string) string {
	return filepath.Join(directory, "development-grants.json")
}

func loadDevelopmentGrantLedger(directory string) (engineeringInsightDevelopmentGrantLedger, bool, error) {
	path := buildDevelopmentGrantLedgerPath(directory)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return engineeringInsightDevelopmentGrantLedger{}, false, nil
	}
	if err != nil || ValidateStrictJSONDocument(data) != nil {
		return engineeringInsightDevelopmentGrantLedger{}, false, fmt.Errorf("invalid development grant ledger")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 1 || fields["grants"] == nil {
		return engineeringInsightDevelopmentGrantLedger{}, false, fmt.Errorf("invalid development grant ledger")
	}
	var ledger engineeringInsightDevelopmentGrantLedger
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ledger) != nil || validateJSONEOF(decoder) != nil || !isValidDevelopmentGrantLedger(ledger) {
		return engineeringInsightDevelopmentGrantLedger{}, false, fmt.Errorf("invalid development grant ledger")
	}
	return ledger, true, nil
}

func isValidDevelopmentGrantLedger(ledger engineeringInsightDevelopmentGrantLedger) bool {
	if len(ledger.Grants) < 1 || len(ledger.Grants) > 7 {
		return false
	}
	seen := make(map[string]bool, len(ledger.Grants))
	for index, grant := range ledger.Grants {
		if !isValidRunnerID(grant.AuthorizationID) || grant.Requests != developmentGrantRequestCount || seen[grant.AuthorizationID] {
			return false
		}
		if !isValidDevelopmentGrantAt(index, grant) {
			return false
		}
		seen[grant.AuthorizationID] = true
	}
	return true
}

func isValidDevelopmentGrantAt(index int, grant engineeringInsightDevelopmentGrant) bool {
	identity := engineeringInsightDevelopmentGrantIdentity{CandidateID: grant.CandidateID, Model: grant.Model, PromptVersion: grant.PromptVersion, ReasoningEffort: grant.ReasoningEffort}
	switch index {
	case 0:
		return isValidOriginalDevelopmentGrant(grant.AuthorizationID, identity)
	case 1:
		return isValidRecoveryDevelopmentGrant(grant.AuthorizationID, identity)
	case 2:
		return isValidV11DevelopmentGrant(grant.AuthorizationID, identity)
	case 3:
		return isValidV12DevelopmentGrant(grant.AuthorizationID, identity)
	case 4:
		return isValidThinkingOffDevelopmentGrant(grant.AuthorizationID, identity)
	case 5:
		return isValidThinkingSchemaDevelopmentGrant(grant.AuthorizationID, identity)
	case 6:
		return isValidMediumDevelopmentGrant(grant.AuthorizationID, identity)
	default:
		return false
	}
}
