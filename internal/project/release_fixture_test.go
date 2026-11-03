package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

func TestReleaseFixtureRepresentsPolicyAndParserAcceptanceCases(t *testing.T) {
	root := filepath.Join("testdata", "release-fixture")
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", "private.pem", "config.yaml"} {
		if decision := policy.Decide(path); decision.Include {
			t.Fatalf("release fixture secret %s was included: %+v", path, decision)
		}
	}
	for _, path := range []string{"main.go", "main_test.go", "malformed.go.fixture", "parser_failure.go.fixture", "vet_failure.go.fixture", "test_failure.go.fixture", "ai_suggestion.json.fixture"} {
		if decision := policy.Decide(path); !decision.Include {
			t.Fatalf("release fixture source %s was excluded: %+v", path, decision)
		}
	}
	mainSource, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	imports, diagnostics := symbols.ExtractGoFacts("main.go", mainSource)
	if imports == nil || len(diagnostics) != 0 {
		t.Fatalf("main fixture facts = imports:%+v diagnostics:%+v", imports, diagnostics)
	}
	brokenSource, err := os.ReadFile(filepath.Join(root, "malformed.go.fixture"))
	if err != nil {
		t.Fatal(err)
	}
	_, diagnostics = symbols.ExtractGoFacts("malformed.go", brokenSource)
	if len(diagnostics) == 0 {
		t.Fatal("expected malformed fixture diagnostics")
	}
	for _, fixture := range []struct {
		path string
		text string
	}{
		{"vet_failure.go.fixture", "fmt.Printf(\"%d\", \"not a number\")"},
		{"test_failure.go.fixture", "expected release-fixture test finding"},
	} {
		data, err := os.ReadFile(filepath.Join(root, fixture.path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), fixture.text) {
			t.Fatalf("release fixture %s does not contain %q", fixture.path, fixture.text)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "ai_suggestion.json.fixture"))
	if err != nil {
		t.Fatal(err)
	}
	var suggestion struct {
		Source     string `json:"source"`
		Confidence string `json:"confidence"`
	}
	if err := json.Unmarshal(data, &suggestion); err != nil {
		t.Fatal(err)
	}
	if suggestion.Source != FindingSourceAI || suggestion.Confidence != FindingConfidenceSuggested {
		t.Fatalf("AI suggestion provenance = %+v", suggestion)
	}
}
