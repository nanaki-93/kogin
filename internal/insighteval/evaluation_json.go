package insighteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

var (
	receiptJSONFields     = []string{"mode", "run_id", "candidate_id", "provider", "model", "prompt_version", "corpus_id", "corpus_digest", "base_revision", "max_requests", "max_output_tokens", "attempt_timeout_seconds", "consumption", "attempts"}
	consumptionJSONFields = []string{"requests", "output_tokens"}
	attemptJSONFields     = []string{"case_name", "partition", "intent", "repetition", "attempt", "candidate_id", "outcome", "usable_summary", "complete_summary", "optional_insight", "optional_section_degraded", "emitted_response", "response_digest", "output_tokens", "invalid_provider_metadata", "finish_reason", "elapsed_milliseconds", "score"}
	scoreJSONFields       = []string{"response_digest", "correctness", "local_relevance", "tradeoff_clarity", "useful_verification", "critical_false_claim", "retain_example"}
)

// DecodeEngineeringInsightEvaluationReceipt rejects malformed evidence without
// echoing arbitrary receipt content.
func DecodeEngineeringInsightEvaluationReceipt(data []byte) (EngineeringInsightEvaluationReceipt, error) {
	if err := validateNoDuplicateJSONKeys(data); err != nil {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("invalid evaluation receipt")
	}
	if err := validateReceiptFields(data); err != nil {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("invalid evaluation receipt")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt EngineeringInsightEvaluationReceipt
	if err := decoder.Decode(&receipt); err != nil || validateJSONEOF(decoder) != nil {
		return EngineeringInsightEvaluationReceipt{}, fmt.Errorf("invalid evaluation receipt")
	}
	return receipt, nil
}

// DecodeEngineeringInsightScores accepts only digest-bound reviewer scores.
// It applies the receipt's strict JSON rules before any private handoff is joined.
func DecodeEngineeringInsightScores(data []byte) (map[string]EngineeringInsightAttemptScore, error) {
	if validateNoDuplicateJSONKeys(data) != nil {
		return nil, fmt.Errorf("invalid evaluation scores")
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&raw) != nil || validateJSONEOF(decoder) != nil || len(raw) == 0 {
		return nil, fmt.Errorf("invalid evaluation scores")
	}
	result := make(map[string]EngineeringInsightAttemptScore, len(raw))
	for id, value := range raw {
		var fields map[string]json.RawMessage
		if strings.TrimSpace(id) == "" || json.Unmarshal(value, &fields) != nil || !isValidRequiredJSONObject(fields, scoreJSONFields) {
			return nil, fmt.Errorf("invalid evaluation scores")
		}
		valueDecoder := json.NewDecoder(bytes.NewReader(value))
		valueDecoder.DisallowUnknownFields()
		var score EngineeringInsightAttemptScore
		if valueDecoder.Decode(&score) != nil || validateJSONEOF(valueDecoder) != nil || !isValidDigest(score.ResponseDigest) || !isValidScore(score) {
			return nil, fmt.Errorf("invalid evaluation scores")
		}
		result[id] = score
	}
	return result, nil
}

// ValidateStrictJSONDocument rejects duplicate object keys and trailing JSON.
// Callers can combine it with their own schema validation without revealing input.
func ValidateStrictJSONDocument(data []byte) error {
	return validateNoDuplicateJSONKeys(data)
}

func validateReceiptFields(data []byte) error {
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(data, &receipt); err != nil || !hasJSONFields(receipt, receiptJSONFields...) || !hasNonNullJSONFields(receipt, receiptJSONFields...) {
		return fmt.Errorf("missing receipt field")
	}
	version, versioned := receipt["protocol_version"]
	if !versioned {
		if !hasOnlyJSONFields(receipt, receiptJSONFields...) {
			return fmt.Errorf("invalid receipt field")
		}
	} else if string(version) != `"v2"` || !hasOnlyJSONFields(receipt, append(receiptJSONFields, "protocol_version")...) {
		return fmt.Errorf("invalid protocol version")
	}
	if err := validateConsumptionFields(receipt["consumption"]); err != nil {
		return err
	}
	return validateAttemptFields(receipt["attempts"])
}

func validateConsumptionFields(data json.RawMessage) error {
	var consumption map[string]json.RawMessage
	if err := json.Unmarshal(data, &consumption); err != nil || !isValidRequiredJSONObject(consumption, consumptionJSONFields) {
		return fmt.Errorf("missing consumption field")
	}
	return nil
}

func validateAttemptFields(data json.RawMessage) error {
	var attempts []map[string]json.RawMessage
	if err := json.Unmarshal(data, &attempts); err != nil {
		return fmt.Errorf("invalid attempts")
	}
	for _, attempt := range attempts {
		if !hasJSONFields(attempt, attemptJSONFields...) || !hasOnlyJSONFields(attempt, attemptJSONFields...) || !hasNonNullJSONFields(attempt, attemptJSONFields[:len(attemptJSONFields)-1]...) {
			return fmt.Errorf("missing attempt field")
		}
		if err := validateScoreFields(attempt["score"]); err != nil {
			return fmt.Errorf("missing score field")
		}
	}
	return nil
}

func validateScoreFields(data json.RawMessage) error {
	if string(data) == "null" {
		return nil
	}
	var score map[string]json.RawMessage
	if err := json.Unmarshal(data, &score); err != nil || !isValidRequiredJSONObject(score, scoreJSONFields) {
		return fmt.Errorf("invalid score")
	}
	return nil
}

func isValidRequiredJSONObject(values map[string]json.RawMessage, fields []string) bool {
	return hasJSONFields(values, fields...) && hasOnlyJSONFields(values, fields...) && hasNonNullJSONFields(values, fields...)
}

func hasJSONFields(values map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		if _, present := values[field]; !present {
			return false
		}
	}
	return true
}

func hasOnlyJSONFields(values map[string]json.RawMessage, fields ...string) bool {
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for field := range values {
		if _, known := allowed[field]; !known {
			return false
		}
	}
	return true
}

func hasNonNullJSONFields(values map[string]json.RawMessage, fields ...string) bool {
	for _, field := range fields {
		if string(values[field]) == "null" {
			return false
		}
	}
	return true
}

func validateNoDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	return validateJSONEOF(decoder)
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("invalid object")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("duplicate key")
			}
			seen[name] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("invalid delimiter")
	}
}

func validateJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}
