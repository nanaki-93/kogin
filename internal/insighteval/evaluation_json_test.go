package insighteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeEngineeringInsightScoresIsStrict(t *testing.T) {
	valid := `{"case":{"response_digest":"` + strings.Repeat("a", 64) + `","correctness":2,"local_relevance":2,"tradeoff_clarity":2,"useful_verification":2,"critical_false_claim":false,"retain_example":false}}`
	if _, err := DecodeEngineeringInsightScores([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		`{"case":{"response_digest":"` + strings.Repeat("a", 64) + `","correctness":2,"local_relevance":2,"tradeoff_clarity":2,"useful_verification":2,"critical_false_claim":false,"retain_example":false,"extra":true}}`,
		`{"case":{"response_digest":"` + strings.Repeat("a", 64) + `","response_digest":"` + strings.Repeat("b", 64) + `","correctness":2,"local_relevance":2,"tradeoff_clarity":2,"useful_verification":2,"critical_false_claim":false,"retain_example":false}}`,
		valid + ` {}`,
	} {
		if _, err := DecodeEngineeringInsightScores([]byte(data)); err == nil {
			t.Fatal("invalid score document accepted")
		}
	}
}

func TestDecodeEngineeringInsightEvaluationReceiptRejectsUnsafeJSON(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field":   `{"unexpected":true}`,
		"trailing JSON":   `{} {}`,
		"duplicate field": `{"mode":"qualification","mode":"collection"}`,
		"missing fields":  `{"mode":"qualification"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEngineeringInsightEvaluationReceipt([]byte(data)); err == nil || strings.Contains(err.Error(), "unexpected") {
				t.Fatal("unsafe JSON was accepted or exposed")
			}
		})
	}
}

func TestDecodeEngineeringInsightEvaluationReceiptRejectsNullAndCaseAliasFields(t *testing.T) {
	receipt, err := json.Marshal(qualificationReceipt())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"required scalar null": strings.Replace(string(receipt), `"provider":"provider"`, `"provider":null`, 1),
		"case alias":           strings.Replace(string(receipt), `"critical_false_claim":false`, `"critical_false_claim":true,"CRITICAL_FALSE_CLAIM":false`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEngineeringInsightEvaluationReceipt([]byte(data)); err == nil || strings.Contains(err.Error(), "CRITICAL") {
				t.Fatal("unsafe field alias or null was accepted or exposed")
			}
		})
	}
}

func TestDecodeEngineeringInsightEvaluationReceiptKeepsV1AndV2SchemasSeparate(t *testing.T) {
	v1, err := json.Marshal(qualificationReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeEngineeringInsightEvaluationReceipt(v1); err != nil {
		t.Fatalf("historical v1 receipt changed: %v", err)
	}
	for _, version := range []string{"null", `""`, `"v1"`, `"v3"`} {
		candidate := strings.Replace(string(v1), `{"mode"`, `{"protocol_version":`+version+`,"mode"`, 1)
		if _, err := DecodeEngineeringInsightEvaluationReceipt([]byte(candidate)); err == nil {
			t.Fatalf("v1 receipt accepted protocol_version %s", version)
		}
	}
	v2, err := json.Marshal(v2Receipt(v2DevelopmentExpectation(), EngineeringInsightCollectionMode))
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodeEngineeringInsightEvaluationReceipt(v2); err != nil || decoded.ProtocolVersion != "v2" {
		t.Fatalf("valid v2 receipt = %+v, %v", decoded, err)
	}
}
