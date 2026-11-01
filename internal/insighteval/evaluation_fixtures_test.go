package insighteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

type engineeringInsightFixture struct {
	Name                       string                       `json:"name"`
	Partition                  string                       `json:"partition"`
	Intent                     string                       `json:"intent"`
	ReferenceMechanisms        []string                     `json:"reference_mechanisms"`
	PermittedUncertainty       []string                     `json:"permitted_uncertainty"`
	CriticalFalseClaimExamples []string                     `json:"critical_false_claim_examples"`
	ScoringAnchors             map[string]map[string]string `json:"scoring_anchors"`
	Source                     string                       `json:"source"`
	Anchor                     struct {
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Evidence  string `json:"evidence"`
	} `json:"anchor"`
	ExpectInsight   bool   `json:"expect_insight"`
	SanitizedOutput string `json:"sanitized_output"`
}

func TestEngineeringInsightEvaluationV1FixturesHavePartitionedGroundedRubric(t *testing.T) {
	fixtures := loadEngineeringInsightFixtures(t, "cases.json")
	seen := make(map[string]bool, len(fixtures))
	counts := make(map[string]int)
	qualificationIntents := make(map[string]int)
	developmentMechanisms := make(map[string]struct{})

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			if fixture.Name == "" || seen[fixture.Name] {
				t.Fatalf("fixture name is empty or repeated: %q", fixture.Name)
			}
			seen[fixture.Name] = true
			assertFixturePartitionAndIntent(t, fixture)
			assertFixtureRubric(t, fixture)
			assertFixtureSourceAnchor(t, fixture)
			assertFixtureSemanticOutput(t, fixture)
		})
		counts[fixture.Partition]++
		if fixture.Partition == qualificationPartition {
			qualificationIntents[fixture.Intent]++
		} else {
			for _, mechanism := range fixture.ReferenceMechanisms {
				developmentMechanisms[mechanism] = struct{}{}
			}
		}
	}

	if counts[developmentPartition] != 3 || counts[qualificationPartition] != 12 {
		t.Fatalf("case partitions = %+v, want 3 development and 12 qualification", counts)
	}
	if qualificationIntents[substantiveIntent] != 8 || qualificationIntents[controlIntent] != 4 {
		t.Fatalf("qualification intents = %+v, want 8 substantive and 4 controls", qualificationIntents)
	}
	for _, fixture := range fixtures {
		if fixture.Partition != qualificationPartition {
			continue
		}
		for _, mechanism := range fixture.ReferenceMechanisms {
			if _, reused := developmentMechanisms[mechanism]; reused {
				t.Fatalf("qualification case %q reuses a development reference mechanism %q", fixture.Name, mechanism)
			}
		}
	}
}

func TestEngineeringInsightEvaluationFixturesCompileInOfflineTemporaryModules(t *testing.T) {
	assertFixturesCompileInOfflineTemporaryModules(t, loadEngineeringInsightFixtures(t, "cases.json"))
}

func TestEngineeringInsightEvaluationV2DevelopmentFixturesHaveGroundedRubric(t *testing.T) {
	fixtures := loadEngineeringInsightFixtures(t, "v2-development.json")
	intents := make(map[string]int)
	assertUniqueFixtureNamesAndSources(t, fixtures)
	assertV2DevelopmentSubstantiveSourceIdentities(t, fixtures)

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			assertFixturePartitionAndIntent(t, fixture)
			if fixture.Partition != developmentPartition {
				t.Fatalf("partition = %q, want development", fixture.Partition)
			}
			assertFixtureRubric(t, fixture)
			assertFixtureSourceAnchor(t, fixture)
			assertFixtureSemanticOutput(t, fixture)
			assertFixtureReferenceInsightBounds(t, fixture)
		})
		intents[fixture.Intent]++
	}

	if len(fixtures) != 12 || intents[substantiveIntent] != 8 || intents[controlIntent] != 4 {
		t.Fatalf("v2 development fixtures = %d with intents %+v, want 12 (8 substantive, 4 controls)", len(fixtures), intents)
	}
}

func TestEngineeringInsightEvaluationV2DevelopmentFixturesCompileInOfflineTemporaryModules(t *testing.T) {
	assertFixturesCompileInOfflineTemporaryModules(t, loadEngineeringInsightFixtures(t, "v2-development.json"))
}

func TestEngineeringInsightEvaluationV2QualificationFixturesHaveSealedAbsentState(t *testing.T) {
	fixtures, published := loadPublishedV2QualificationFixtures(t)
	if !published {
		t.Skip("v2 qualification corpus remains evaluator-sealed")
	}

	combined := append(loadEngineeringInsightFixtures(t, "v2-development.json"), fixtures...)
	assertUniqueFixtureNamesAndSources(t, combined)
	intents := make(map[string]int)
	for _, fixture := range fixtures {
		assertFixturePartitionAndIntent(t, fixture)
		if fixture.Partition != qualificationPartition {
			t.Fatalf("qualification fixture partition = %q", fixture.Partition)
		}
		assertFixtureRubric(t, fixture)
		assertFixtureSourceAnchor(t, fixture)
		assertFixtureSemanticOutput(t, fixture)
		assertFixtureReferenceInsightBounds(t, fixture)
		intents[fixture.Intent]++
	}
	if len(fixtures) != 24 || intents[substantiveIntent] != 16 || intents[controlIntent] != 8 {
		t.Fatalf("v2 qualification fixture counts = %d with intents %+v, want 24 (16 substantive, 8 controls)", len(fixtures), intents)
	}
}

func assertFixturePartitionAndIntent(t *testing.T, fixture engineeringInsightFixture) {
	t.Helper()
	if fixture.Partition != developmentPartition && fixture.Partition != qualificationPartition {
		t.Fatalf("unsupported fixture partition %q", fixture.Partition)
	}
	if fixture.Intent != substantiveIntent && fixture.Intent != controlIntent {
		t.Fatalf("unsupported fixture intent %q", fixture.Intent)
	}
	if fixture.ExpectInsight != (fixture.Intent == substantiveIntent) {
		t.Fatalf("expect_insight=%t does not match %s intent", fixture.ExpectInsight, fixture.Intent)
	}
}

func assertFixtureRubric(t *testing.T, fixture engineeringInsightFixture) {
	t.Helper()
	if err := validateFixtureRubric(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestEngineeringInsightFixtureRubricRejectsUnexpectedScoreKey(t *testing.T) {
	fixture := engineeringInsightFixture{
		ReferenceMechanisms:        []string{"A visible mechanism."},
		PermittedUncertainty:       []string{"An unshown boundary."},
		CriticalFalseClaimExamples: []string{"An unsupported claim."},
		ScoringAnchors: map[string]map[string]string{
			"correctness":         {"0": "low", "1": "middle", "2": "high"},
			"local_relevance":     {"0": "low", "1": "middle", "2": "high"},
			"tradeoff_clarity":    {"0": "low", "1": "middle", "2": "high"},
			"useful_verification": {"0": "low", "1": "middle", "2": "high"},
		},
	}
	if err := validateFixtureRubric(fixture); err != nil {
		t.Fatalf("valid rubric rejected: %v", err)
	}
	fixture.ScoringAnchors["correctness"]["3"] = "unexpected"
	if err := validateFixtureRubric(fixture); err == nil {
		t.Fatal("rubric accepted an unexpected nested score key")
	}
}

func validateFixtureRubric(fixture engineeringInsightFixture) error {
	for label, values := range map[string][]string{
		"reference mechanism":          fixture.ReferenceMechanisms,
		"permitted uncertainty":        fixture.PermittedUncertainty,
		"critical false-claim example": fixture.CriticalFalseClaimExamples,
	} {
		if len(values) == 0 {
			return fmt.Errorf("fixture has no %s", label)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("fixture has blank %s", label)
			}
		}
	}
	if len(fixture.ScoringAnchors) != 4 {
		return fmt.Errorf("fixture scoring dimensions = %d, want 4", len(fixture.ScoringAnchors))
	}
	for _, dimension := range []string{"correctness", "local_relevance", "tradeoff_clarity", "useful_verification"} {
		anchors := fixture.ScoringAnchors[dimension]
		if len(anchors) != 3 {
			return fmt.Errorf("fixture %s score anchors = %d, want 3", dimension, len(anchors))
		}
		for _, score := range []string{"0", "1", "2"} {
			if strings.TrimSpace(anchors[score]) == "" {
				return fmt.Errorf("fixture is missing %s score anchor %s", dimension, score)
			}
		}
		for score := range anchors {
			if score != "0" && score != "1" && score != "2" {
				return fmt.Errorf("fixture has unexpected %s score anchor %s", dimension, score)
			}
		}
	}
	return nil
}

func assertFixtureSemanticOutput(t *testing.T, fixture engineeringInsightFixture) {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixture.SanitizedOutput), &wire); err != nil {
		t.Fatalf("sanitized output is not JSON: %v", err)
	}
	assessment := app.AssessFileAnalysisEvaluation(fixture.SanitizedOutput, project.IndexFile{Path: "fixture.go", Language: "Go"}, fixture.Source)
	if !assessment.UsableSummary || assessment.Diagnostics.Degraded() {
		t.Fatalf("sanitized output rejected or degraded: %+v", assessment)
	}
	if !fixture.ExpectInsight {
		if _, present := wire["engineering_insight"]; present {
			t.Fatal("control contains an engineering insight instead of intentionally omitting it")
		}
		if assessment.Diagnostics.OptionalInsight() != "omitted" {
			t.Fatalf("control retained an insight: %+v", assessment.Diagnostics)
		}
		return
	}
	if assessment.Diagnostics.OptionalInsight() != "present" {
		t.Fatal("grounded insight was omitted")
	}
	insight := wire["engineering_insight"]
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(insight, &fields); err != nil {
		t.Fatalf("insight structure = %v", err)
	}
	for _, field := range []string{"mechanism", "why_it_matters_here", "tradeoff_or_failure_mode", "transferable_lesson"} {
		if len(fields[field]) == 0 {
			t.Fatalf("insight is missing %q: %s", field, insight)
		}
	}
}

func loadEngineeringInsightFixtures(t *testing.T, fileName string) []engineeringInsightFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "app", "testdata", "engineering-insight-eval", fileName))
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := decodeEngineeringInsightFixtures(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("engineering insight fixture set is empty")
	}
	return fixtures
}

func TestDecodeEngineeringInsightFixturesIsStrict(t *testing.T) {
	for name, data := range map[string][]byte{
		"unknown fixture field":   []byte(`[{"name":"case","unexpected":true}]`),
		"duplicate fixture field": []byte(`[{"name":"first","name":"second"}]`),
		"trailing document":       []byte(`[] []`),
		"malformed document":      []byte(`[{`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEngineeringInsightFixtures(data); err == nil {
				t.Fatal("invalid fixture envelope accepted")
			}
		})
	}
}

func decodeEngineeringInsightFixtures(data []byte) ([]engineeringInsightFixture, error) {
	if err := ValidateStrictJSONDocument(data); err != nil {
		return nil, fmt.Errorf("invalid fixture document: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fixtures []engineeringInsightFixture
	if err := decoder.Decode(&fixtures); err != nil {
		return nil, fmt.Errorf("invalid fixture envelope: %w", err)
	}
	return fixtures, nil
}

func loadPublishedV2QualificationFixtures(t *testing.T) ([]engineeringInsightFixture, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "app", "testdata", "engineering-insight-eval", "v2-qualification.json"))
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	fixtures, err := decodeEngineeringInsightFixtures(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) == 0 {
		t.Fatal("published v2 qualification fixture set is empty")
	}
	return fixtures, true
}

func assertFixturesCompileInOfflineTemporaryModules(t *testing.T, fixtures []engineeringInsightFixture) {
	t.Helper()
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte(fixture.Source), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", "./...")
			command.Dir = root
			command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("offline isolated fixture compile: %v\n%s", err, output)
			}
		})
	}
}

func assertUniqueFixtureNamesAndSources(t *testing.T, fixtures []engineeringInsightFixture) {
	t.Helper()
	names := make(map[string]struct{}, len(fixtures))
	sources := make(map[[sha256.Size]byte]string, len(fixtures))
	for _, fixture := range fixtures {
		if fixture.Name == "" {
			t.Fatal("fixture name is empty")
		}
		if _, duplicate := names[fixture.Name]; duplicate {
			t.Fatalf("fixture name is repeated: %q", fixture.Name)
		}
		names[fixture.Name] = struct{}{}
		digest := sha256.Sum256([]byte(fixture.Source))
		if original, duplicate := sources[digest]; duplicate {
			t.Fatalf("fixture source is repeated by %q and %q", original, fixture.Name)
		}
		sources[digest] = fixture.Name
	}
}

// These source digests bind the eight human-reviewed substantive cases to their
// reviewed control flow. They deliberately do not infer mechanism coverage from
// prose keywords.
var v2DevelopmentSubstantiveSourceDigests = map[string]string{
	"v2-development-snapshot-read-lock":           "2db77b1f8da401ebe0abf7452f43a7db2d8aea782ece26f38984300c7a4c72ce",
	"v2-development-cancellation-before-loop":     "c0b7b97f3bde0fb4c0ad4a1bac6a177bfbf89fc3740c27741fe559abf850f7e8",
	"v2-development-filtered-output-capacity":     "4eca6e6932fea3edb4419f94dc414928beff4068ac24402c7faeaa93f907e5fc",
	"v2-development-bounded-opaque-refresh":       "612e2a609930699459ab5f3380dafdb4003409d490b5f97794dcf6ee58d72b38",
	"v2-development-sync-map-idempotency":         "3fdb21c6d2f07a54f5c1156ed001f22c9417bad669e5374362e87031dd52654f",
	"v2-development-read-authorization-boundary":  "b74c2a4a77762bac44115aad72034f1ba25649228de7b190d4ee7b8b7d320c54",
	"v2-development-delegated-token-verification": "80e71d00089153d84cec64ab0c962a43c2d68789304d4b26b02dd6afbdae6d92",
	"v2-development-buffered-send-contrast":       "ad6b619925404a210275b1b89d894cd125236c2ebceda949b63c9544d370a3dd",
}

func assertV2DevelopmentSubstantiveSourceIdentities(t *testing.T, fixtures []engineeringInsightFixture) {
	t.Helper()
	seen := make(map[string]bool, len(v2DevelopmentSubstantiveSourceDigests))
	for _, fixture := range fixtures {
		if fixture.Intent != substantiveIntent {
			continue
		}
		expected, known := v2DevelopmentSubstantiveSourceDigests[fixture.Name]
		if !known {
			t.Fatalf("substantive fixture %q has no reviewed source identity", fixture.Name)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256([]byte(fixture.Source))); actual != expected {
			t.Fatalf("substantive fixture %q source identity changed", fixture.Name)
		}
		seen[fixture.Name] = true
	}
	if len(seen) != len(v2DevelopmentSubstantiveSourceDigests) {
		t.Fatalf("reviewed substantive source identities = %d, want %d", len(seen), len(v2DevelopmentSubstantiveSourceDigests))
	}
}

func assertFixtureReferenceInsightBounds(t *testing.T, fixture engineeringInsightFixture) {
	t.Helper()
	if !fixture.ExpectInsight {
		return
	}

	var wire struct {
		EngineeringInsight *struct {
			Mechanism             string `json:"mechanism"`
			WhyItMattersHere      string `json:"why_it_matters_here"`
			TradeoffOrFailureMode string `json:"tradeoff_or_failure_mode"`
			TransferableLesson    string `json:"transferable_lesson"`
		} `json:"engineering_insight"`
	}
	if err := json.Unmarshal([]byte(fixture.SanitizedOutput), &wire); err != nil {
		t.Fatalf("decode reference insight: %v", err)
	}
	if wire.EngineeringInsight == nil {
		t.Fatal("substantive fixture has no reference insight")
	}
	for name, value := range map[string]string{
		"mechanism":                wire.EngineeringInsight.Mechanism,
		"why_it_matters_here":      wire.EngineeringInsight.WhyItMattersHere,
		"tradeoff_or_failure_mode": wire.EngineeringInsight.TradeoffOrFailureMode,
		"transferable_lesson":      wire.EngineeringInsight.TransferableLesson,
	} {
		if len([]rune(value)) > 250 {
			t.Fatalf("reference insight %s has %d runes, want at most 250", name, len([]rune(value)))
		}
	}
}

func assertFixtureSourceAnchor(t *testing.T, fixture engineeringInsightFixture) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(fixture.Source, "\n"), "\n")
	if fixture.Anchor.StartLine <= 0 || fixture.Anchor.EndLine < fixture.Anchor.StartLine || fixture.Anchor.EndLine > len(lines) {
		t.Fatalf("invalid source anchor %+v for %d source lines", fixture.Anchor, len(lines))
	}
	anchoredSource := strings.Join(lines[fixture.Anchor.StartLine-1:fixture.Anchor.EndLine], "\n")
	if fixture.Anchor.Evidence == "" || !strings.Contains(anchoredSource, fixture.Anchor.Evidence) {
		t.Fatalf("source anchor %+v does not contain its evidence in %q", fixture.Anchor, anchoredSource)
	}
}
