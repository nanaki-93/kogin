package insighteval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/config"
)

func TestEngineeringInsightRecoveryV2BindsCompleteHistoricalEvidenceSet(t *testing.T) {
	if len(recoveryV2PredecessorFiles) != 19 {
		t.Fatalf("predecessor evidence count = %d, want campaign, grant ledger, and 17 receipts", len(recoveryV2PredecessorFiles))
	}
	for name, digest := range recoveryV2PredecessorFiles {
		if !isValidDigest(digest) {
			t.Fatalf("predecessor %q has invalid digest %q", name, digest)
		}
	}
}

// Historical accounting remains testable without reauthorizing v13 dispatch.
func TestEngineeringInsightRecoveryV2ReservesExactlyTwelveDevelopmentCases(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	for _, item := range cfg.Cases {
		if reserved, err := reserveRecoveryV2Attempt(directory, cfg, item.Expected); err != nil || !reserved {
			t.Fatalf("reservation = %t, %v", reserved, err)
		}
	}
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if period.DevelopmentRequests != 12 || period.CumulativeDevelopmentRequests != 60 || len(period.DevelopmentSchedule) != 12 || len(period.Reservations) != 12 {
		t.Fatalf("v2 accounting = %+v", period)
	}
	if reserved, err := reserveRecoveryV2Attempt(directory, cfg, cfg.Cases[0].Expected); err != nil || reserved {
		t.Fatalf("replayed reservation = %t, %v", reserved, err)
	}
	if err := consumeRecoveryV2Request(&period, EngineeringInsightDevelopmentRunMode); err == nil {
		t.Fatal("exceeded twelve-request budget")
	}
	manifest := engineeringInsightRunManifest{Receipt: buildRunnerReceipt(cfg)}
	if err := validateRunnerManifest(manifest, cfg); err != nil {
		t.Fatal(err)
	}
	manifest.Receipt.ProtocolVersion = ""
	if err := validateRunnerManifest(manifest, cfg); err == nil {
		t.Fatal("v2 manifest accepted a historical protocol identity")
	}
}

func TestEngineeringInsightRecoveryV2RejectsRetiredPromptWithoutConsumption(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	path := buildRecoveryV2PeriodPath(directory)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{recoveryV2PromptVersion, app.EngineeringInsightPromptVersion()} {
		cfg.PromptVersion = version
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
			t.Fatalf("retired campaign dispatched under %s: %v", version, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatal("rejection changed historical accounting")
		}
		if _, err := os.Stat(filepath.Join(directory, cfg.RunID+".json")); !os.IsNotExist(err) {
			t.Fatalf("rejection created a manifest: %v", err)
		}
	}
}

func TestEngineeringInsightRecoveryV2RejectsConcurrentReservationOwner(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	lock, err := lockRunnerFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	second, err := lockRunnerFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.lock"))
	if second != nil {
		defer second.Close()
	}
	if !errors.Is(err, ErrEngineeringInsightRunLocked) {
		t.Fatalf("concurrent reservation owner = %v", err)
	}
}

func TestEngineeringInsightRecoveryV2PreservesChargedReservationWithoutReplacement(t *testing.T) {
	client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
	cfg := recoveryV2DevelopmentTestConfig(t, client)
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	period.DevelopmentSchedule = buildRecoveryV2ScheduleIDs(cfg.Cases)
	period.DevelopmentRequests = 1
	period.CumulativeDevelopmentRequests = 49
	period.Reservations[period.DevelopmentSchedule[0]] = true
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	for index, item := range cfg.Cases {
		reserved, err := reserveRecoveryV2Attempt(directory, cfg, item.Expected)
		if err != nil || reserved != (index != 0) {
			t.Fatalf("charged attempt %d reserved=%t, %v", index, reserved, err)
		}
	}
	period, err = loadRecoveryV2Period(directory, cfg.Root)
	if err != nil || period.DevelopmentRequests != 12 || period.CumulativeDevelopmentRequests != 60 {
		t.Fatalf("charged accounting = %+v, %v", period, err)
	}
	for _, runID := range []string{cfg.RunID, "replacement-run"} {
		cfg.RunID = runID
		if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
			t.Fatalf("retired/replacement run dispatched: %v", err)
		}
	}
}

func TestEngineeringInsightRecoveryV2RejectsIdentityCorpusAndScheduleDrift(t *testing.T) {
	base := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	for name, mutate := range map[string]func(*EngineeringInsightRunnerConfig){
		"candidate": func(cfg *EngineeringInsightRunnerConfig) { cfg.CandidateID = "wrong" },
		"model":     func(cfg *EngineeringInsightRunnerConfig) { cfg.Model = "wrong" },
		"provider":  func(cfg *EngineeringInsightRunnerConfig) { cfg.Provider = "wrong" },
		"endpoint":  func(cfg *EngineeringInsightRunnerConfig) { cfg.Profile.APIBaseURL = "http://127.0.0.1:1234/v1" },
		"reasoning": func(cfg *EngineeringInsightRunnerConfig) { cfg.Profile.ReasoningEffort = "low" },
		"base":      func(cfg *EngineeringInsightRunnerConfig) { cfg.BaseRevision = strings.Repeat("b", 40) },
		"run":       func(cfg *EngineeringInsightRunnerConfig) { cfg.RunID = "replacement" },
		"source":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Source += "\n// drift" },
		"name":      func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.CaseName = "other" },
		"intent":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.Intent = "control" },
		"repeat":    func(cfg *EngineeringInsightRunnerConfig) { cfg.Cases[0].Expected.Repetition = 2 },
		"bytes": func(cfg *EngineeringInsightRunnerConfig) {
			cfg.CorpusJSON = append(append([]byte(nil), cfg.CorpusJSON...), '\n')
		},
		"extra sampler": func(cfg *EngineeringInsightRunnerConfig) {
			value := float32(0)
			cfg.Profile.MinP = &value
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.Cases = append([]EngineeringInsightRunnerCase(nil), base.Cases...)
			cfg.CorpusJSON = append([]byte(nil), base.CorpusJSON...)
			mutate(&cfg)
			client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
			cfg.Client = client
			if isRecoveryV2RunnerConfig(cfg) && validateRecoveryV2RunnerConfig(cfg) == nil && cfg.BaseRevision == base.BaseRevision {
				t.Fatal("historical identity/corpus/schedule guard accepted drift")
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
				t.Fatalf("drift dispatched: %v, calls=%d", err, client.calls.Load())
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2RejectsPredecessorAndCleanHeadDrift(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, EngineeringInsightRunnerConfig){
		"predecessor": func(t *testing.T, cfg EngineeringInsightRunnerConfig) {
			if err := os.WriteFile(filepath.Join(cfg.Root, runnerRelativeDirectory, "campaign.json"), []byte(`{"development_requests":47,"qualification_requests":24}`), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"head": func(t *testing.T, cfg EngineeringInsightRunnerConfig) {
			path := filepath.Join(cfg.Root, "changed.txt")
			if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("git", "-C", cfg.Root, "add", "changed.txt")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("stage changed head: %v: %s", err, output)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeEngineeringInsightClient{reply: runnerValidResponse()}
			cfg := recoveryV2DevelopmentTestConfig(t, client)
			mutate(t, cfg)
			if reserved, err := reserveRecoveryV2Attempt(filepath.Join(cfg.Root, runnerRelativeDirectory), cfg, cfg.Cases[0].Expected); err == nil || reserved {
				t.Fatalf("historical %s boundary accepted drift: %v", name, err)
			}
			if _, _, err := RunEngineeringInsightEvaluation(context.Background(), cfg); err == nil || client.calls.Load() != 0 {
				t.Fatalf("%s drift dispatched: %v, calls=%d", name, err, client.calls.Load())
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2PeriodRejectsUnboundAndMalformedReservations(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	period.DevelopmentSchedule = buildRecoveryV2ScheduleIDs(cfg.Cases)
	period.DevelopmentRequests = 1
	period.CumulativeDevelopmentRequests = 49
	period.Reservations["junk\x00development\x00junk\x001\x001"] = true
	if isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("unbound reservation ID was accepted")
	}
	period.Reservations = map[string]bool{period.DevelopmentSchedule[0]: true}
	if !isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("bound reservation ID was rejected")
	}
	period.DevelopmentSchedule[0] = "junk\x00development\x00substantive\x001\x001"
	if isValidRecoveryV2Period(period, cfg.Root) {
		t.Fatal("schedule with a reservation outside the verified corpus was accepted")
	}
}

func TestEngineeringInsightRecoveryV2ReservesFixedQualificationScheduleOnce(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	period, err := loadRecoveryV2Period(directory, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	development := v2DevelopmentExpectation()
	period.DevelopmentSchedule = make([]string, len(development.Schedule))
	period.DevelopmentRequests = recoveryV2DevelopmentRequests
	period.CumulativeDevelopmentRequests = recoveryV2DevelopmentLimit
	for index, attempt := range development.Schedule {
		id := buildExpectedAttemptID(attempt)
		period.DevelopmentSchedule[index] = id
		period.Reservations[id] = true
	}
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	manifest := engineeringInsightRunManifest{Finished: true, Receipt: v2Receipt(development, EngineeringInsightCollectionMode)}
	manifest.Receipt.RunID = recoveryV2DevelopmentRunID
	manifest.Receipt.BaseRevision = period.BaseRevision
	qualification := v2QualificationExpectation()
	cfg.Mode = EngineeringInsightQualificationRunMode
	cfg.RunID = recoveryV2QualificationRunID
	cfg.CorpusDigest = recoveryV2QualificationDigest
	cfg.Cases = make([]EngineeringInsightRunnerCase, len(qualification.Schedule))
	for index, attempt := range qualification.Schedule {
		cfg.Cases[index] = EngineeringInsightRunnerCase{Expected: attempt, Source: "package fixture\n"}
	}
	manifestPath := filepath.Join(directory, recoveryV2DevelopmentRunID+".json")
	manifest.Receipt.Attempts[0].Score = nil
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0]); err == nil || dispatch {
		t.Fatalf("qualification reserved against unscored development: dispatch=%v err=%v", dispatch, err)
	}
	manifest.Receipt.Attempts[0].Score = &EngineeringInsightAttemptScore{ResponseDigest: manifest.Receipt.Attempts[0].ResponseDigest, Correctness: 2, LocalRelevance: 2, TradeoffClarity: 2, UsefulVerification: 2, CriticalFalseClaim: true}
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0]); err == nil || dispatch {
		t.Fatalf("qualification reserved against failed development: dispatch=%v err=%v", dispatch, err)
	}
	manifest.Receipt.Attempts[0].Score.CriticalFalseClaim = false
	if err := storeRunnerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	for index, attempt := range qualification.Schedule {
		dispatch, err := reserveRecoveryV2Attempt(directory, cfg, attempt)
		if err != nil || !dispatch {
			t.Fatalf("qualification reservation %d: dispatch=%v err=%v", index, dispatch, err)
		}
	}
	dispatch, err := reserveRecoveryV2Attempt(directory, cfg, qualification.Schedule[0])
	if err != nil || dispatch {
		t.Fatalf("qualification replay reserved again: dispatch=%v err=%v", dispatch, err)
	}
	period, err = loadRecoveryV2Period(directory, cfg.Root)
	if err != nil || period.QualificationRequests != 24 || period.CumulativeQualificationRequests != 48 || len(period.QualificationSchedule) != 24 {
		t.Fatalf("qualification accounting = %+v, %v", period, err)
	}
}

func TestEngineeringInsightRecoveryV2PeriodJSONIsStrict(t *testing.T) {
	cfg := recoveryV2DevelopmentTestConfig(t, &fakeEngineeringInsightClient{reply: runnerValidResponse()})
	directory := filepath.Join(cfg.Root, runnerRelativeDirectory)
	path := buildRecoveryV2PeriodPath(directory)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"malformed":         []byte(`{"version":`),
		"extra key":         []byte(strings.Replace(string(valid), `{"version"`, `{"extra":true,"version"`, 1)),
		"null journal":      []byte(strings.Replace(string(valid), `"reservations":{}`, `"reservations":null`, 1)),
		"negative counter":  []byte(strings.Replace(string(valid), `"development_requests":0`, `"development_requests":-1`, 1)),
		"wrong schema":      []byte(strings.Replace(string(valid), `"schema_identity":"`, `"schema_identity":"wrong-`, 1)),
		"wrong runtime cap": []byte(strings.Replace(string(valid), `"runtime_context_tokens":119552`, `"runtime_context_tokens":119551`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadRecoveryV2Period(directory, cfg.Root); err == nil {
				t.Fatal("invalid period JSON was accepted")
			}
			if err := os.WriteFile(path, valid, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEngineeringInsightRecoveryV2CoordinatorBoundaryIsExact(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".kasuri", "autopilot", "coordinator")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	tasks := `"RCV-01":{"phase":"complete"},"RCV-02":{"phase":"complete"},"RCV-03":{"phase":"complete"},"RCV-04":{"phase":"complete"},"RCV-05":{"phase":"complete"},"RCV-06":{"phase":"complete"}`
	path := filepath.Join(directory, "RCV-recovery.json")
	for name, document := range map[string]string{
		"valid":         `{"phase":"RCV-07","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
		"wrong phase":   `{"phase":"RCV-06","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
		"active":        `{"phase":"RCV-07","scheduler_status":"ACTIVE","tasks":{` + tasks + `}}`,
		"incomplete":    `{"phase":"RCV-07","scheduler_status":"PAUSED","tasks":{"RCV-01":{"phase":"complete"}}}`,
		"duplicate key": `{"phase":"RCV-07","phase":"RCV-06","scheduler_status":"PAUSED","tasks":{` + tasks + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			err := validateRecoveryCoordinatorBoundary(root)
			if name == "valid" && err != nil {
				t.Fatalf("valid coordinator boundary: %v", err)
			}
			if name != "valid" && err == nil {
				t.Fatal("invalid coordinator boundary was accepted")
			}
		})
	}
}

func recoveryV2DevelopmentTestConfig(t *testing.T, client EngineeringInsightRunnerClient) EngineeringInsightRunnerConfig {
	t.Helper()
	root, base := recoveryV2TestRoot(t)
	data, err := os.ReadFile("../app/testdata/engineering-insight-eval/v2-development.json")
	if err != nil {
		t.Fatal(err)
	}
	corpus, ok := decodeRecoveryV2Corpus(data)
	if !ok {
		t.Fatal("decode v2 development corpus")
	}
	cases := make([]EngineeringInsightRunnerCase, len(corpus))
	for index, item := range corpus {
		cases[index] = EngineeringInsightRunnerCase{Expected: EngineeringInsightExpectedAttempt{CaseName: item.Name, Partition: item.Partition, Intent: item.Intent, Repetition: 1, Attempt: 1}, Source: item.Source}
	}
	topP := float32(.95)
	topK := 20
	return EngineeringInsightRunnerConfig{Root: root, RunID: recoveryV2DevelopmentRunID, Mode: EngineeringInsightDevelopmentRunMode, CandidateID: recoveryV2CandidateID, Provider: recoveryV2Provider, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion, CorpusID: recoveryV2CorpusID, CorpusDigest: recoveryV2DevelopmentDigest, CorpusJSON: data, BaseRevision: base, Profile: config.ModelProfile{Scope: config.BugModelScope, APIBaseURL: recoveryV2Endpoint, Model: recoveryV2Model, ReasoningEffort: mediumDevelopmentReasoningEffort, Temperature: 1, TopP: &topP, TopK: &topK, MaxTokens: qualificationOutputTokenCap, ContextMaxTokens: 16384}, Cases: cases, Client: client}
}

func recoveryV2TestRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	initCommand := exec.Command("git", "-C", root, "init")
	if output, err := initCommand.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	rootCommand := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	output, err := rootCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve git root: %v: %s", err, output)
	}
	root = strings.TrimSpace(string(output))
	gitCommand := func(args ...string) string {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	gitCommand("checkout", "-b", "codex/autopilot")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".kasuri/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand("add", ".gitignore")
	gitCommand("-c", "user.email=test@example.invalid", "-c", "user.name=Test", "commit", "-m", "fixture")
	base := gitCommand("rev-parse", "HEAD")
	directory := filepath.Join(root, runnerRelativeDirectory)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	campaign := []byte(`{"development_requests":48,"qualification_requests":24}`)
	if err := os.WriteFile(filepath.Join(directory, "campaign.json"), campaign, 0600); err != nil {
		t.Fatal(err)
	}
	originalPredecessors := recoveryV2PredecessorFiles
	sum := sha256.Sum256(campaign)
	recoveryV2PredecessorFiles = map[string]string{"campaign.json": hex.EncodeToString(sum[:])}
	t.Cleanup(func() { recoveryV2PredecessorFiles = originalPredecessors })
	schema, err := app.FileAnalysisResponseSchema().Identity()
	if err != nil {
		t.Fatal(err)
	}
	period := engineeringInsightRecoveryV2Period{Version: "v2", PeriodID: recoveryV2PeriodID, CanonicalRoot: root, Provider: recoveryV2Provider, Endpoint: recoveryV2Endpoint, CandidateID: recoveryV2CandidateID, Model: recoveryV2Model, PromptVersion: recoveryV2PromptVersion, CorpusID: recoveryV2CorpusID, DevelopmentCorpusDigest: recoveryV2DevelopmentDigest, QualificationCorpusDigest: recoveryV2QualificationDigest, BaseRevision: base, SchemaIdentity: schema, MaxOutputTokens: qualificationOutputTokenCap, InputContextTokens: 16384, RuntimeContextTokens: recoveryV2RuntimeContextTokens, AttemptTimeoutSeconds: qualificationAttemptTimeoutSeconds, ReasoningEffort: mediumDevelopmentReasoningEffort, Temperature: 1, TopP: .95, TopK: 20, Lanes: 1, CumulativeDevelopmentRequests: 48, CumulativeQualificationRequests: 24, DevelopmentLimit: recoveryV2DevelopmentLimit, QualificationLimit: recoveryV2QualificationLimit, PredecessorFiles: buildRecoveryV2PredecessorFiles(), DevelopmentSchedule: []string{}, QualificationSchedule: []string{}, Reservations: map[string]bool{}}
	if !isValidRecoveryV2Period(period, root) {
		canonical, _ := loadCanonicalEvaluationRoot(root)
		t.Fatalf("invalid test period: root=%q canonical=%q identity=%v canonicalIdentity=%v profile=%v accounting=%v predecessors=%+v", root, canonical, hasSameRecoveryV2PeriodIdentity(period, root), hasSameRecoveryV2PeriodIdentity(period, canonical), hasSameRecoveryV2PeriodProfile(period, schema), isValidRecoveryV2PeriodAccounting(period), period.PredecessorFiles)
	}
	if err := storeRunnerJSON(buildRecoveryV2PeriodPath(directory), period); err != nil {
		t.Fatal(err)
	}
	return root, base
}
