package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestCorpusVersionsAndNegativeControlsAreExplicit(t *testing.T) {
	document, err := decodeCorpus(corpusData)
	if err != nil {
		t.Fatal(err)
	}
	version, hash := CorpusIdentity()
	if version != document.Version || hash != "c2418299cf83286b874584d879b78f563e3273469adcd6249790aca52b6cd92b" {
		t.Fatal("corpus identity lost")
	}
	controls := 0
	for _, item := range document.Cases {
		if item.Control {
			controls++
		}
		if (item.Expected == "passed") != !item.Control {
			t.Fatal("negative control disguised", item.ID)
		}
	}
	if controls != 5 {
		t.Fatal("missing calibration cases")
	}
	for _, invalid := range [][]byte{nil, []byte(`null`), []byte(`{"version":"wrong","cases":[]}`), append(bytes.Clone(corpusData), []byte(` {}`)...), bytes.Replace(corpusData, []byte(`"goal":`), []byte(`"unknown":`), 1)} {
		if _, err := decodeCorpus(invalid); err == nil {
			t.Fatal("invalid corpus accepted")
		}
	}
}

func TestRegressionOracleIsIndependentAndCannotExecuteModelCode(t *testing.T) {
	checks := []regression{{2, 3, "5"}, {-4, 1, "-3"}, {0, 0, "0"}}
	if regressionCount("a+b", checks) != 3 || regressionCount("a-b", checks) != 1 {
		t.Fatal("regression oracle cannot distinguish a wrong candidate")
	}
	for _, expression := range []string{`os.ReadFile("private.env")`, `func() int {for {} }()`, `a << 999999999`, `1e9999999`, `a / 0`, `a + secret`, strings.Repeat("1+", 128) + "1"} {
		if regressionCount(expression, checks) != 0 {
			t.Fatal("unsafe or invalid expression accepted", expression)
		}
	}
	if regressionCount("a < b", []regression{{1, 2, "true"}, {2, 2, "false"}}) != 2 {
		t.Fatal("boolean regression oracle failed")
	}
}

func TestUnsupportedClaimsRequireExactObservedEvidence(t *testing.T) {
	files := map[string]string{"calc.go": "first\nsecond"}
	claim := app.AgentEvaluationCitation{Path: "calc.go", Hash: digest([]byte(files["calc.go"])), StartLine: 2, EndLine: 2, Quote: "second"}
	answer := app.AgentEvaluationAnswer{Expression: "a+b", Evidence: []app.AgentEvaluationCitation{claim}, Checks: []string{}}
	steps := []app.AgentEvaluationStep{{Tool: "read_snippet", Status: "ok", Evidence: []project.AgentEvidence{{AgentScope: project.AgentScope{Path: claim.Path, Hash: claim.Hash, StartLine: 2, EndLine: 2}, Text: "second"}}}}
	if unsupportedClaims(answer, files, steps) != 0 || unsupportedClaims(answer, files, nil) != 1 {
		t.Fatal("unobserved source became evidence")
	}
	for _, kind := range []string{"quote", "hash", "range", "checks"} {
		copy := answer
		copy.Evidence = append([]app.AgentEvaluationCitation(nil), answer.Evidence...)
		switch kind {
		case "quote":
			copy.Evidence[0].Quote = "invented"
		case "hash":
			copy.Evidence[0].Hash = "wrong"
		case "range":
			copy.Evidence[0].StartLine = -1
		case "checks":
			copy.Checks = []string{"PRIVATE_CLAIM"}
		}
		if unsupportedClaims(copy, files, steps) != 1 {
			t.Fatal("unsupported claim passed", kind)
		}
	}
	data, _ := json.Marshal(CaseReceipt{ID: "fixture", State: "incomplete", Failures: []string{"invalid_output"}})
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("receipt leaked claim")
	}
}
