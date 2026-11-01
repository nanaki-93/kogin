package app

import (
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// Generation is constrained to the core finding fields. The project parser
// still owns identity, source-free prose, and anchor/declaration validation.
const securityReviewResponseSchemaDocument = `{
  "type":"object",
  "additionalProperties":false,
  "required":["findings"],
  "properties":{
    "findings":{"type":"array","maxItems":%d,"items":{"$ref":"#/$defs/finding"}}
  },
  "$defs":{
    "identifier":{"type":"string","minLength":1,"maxLength":128,"pattern":"^[a-z0-9][a-z0-9._-]*$"},
    "prose":{"type":"string","minLength":1,"maxLength":512},
    "anchor":%s,
    "finding":{
      "type":"object",
      "additionalProperties":false,
      "required":["rule","category","title","source_anchor","severity","confidence","evidence_kind","observed_condition","preconditions_or_unknowns","remediation","verification_idea"],
      "properties":{
        "rule":{"$ref":"#/$defs/identifier"},
        "category":{"$ref":"#/$defs/identifier"},
        "title":{"$ref":"#/$defs/prose"},
        "source_anchor":{"$ref":"#/$defs/anchor"},
        "severity":{"enum":["critical","high","medium","low","info"]},
        "confidence":{"enum":["high","medium","low"]},
        "evidence_kind":{"enum":["model_suspicion"]},
        "observed_condition":{"$ref":"#/$defs/prose"},
        "preconditions_or_unknowns":{"$ref":"#/$defs/prose"},
        "remediation":{"$ref":"#/$defs/prose"},
        "verification_idea":{"$ref":"#/$defs/prose"},
        "cwe":{"type":"string","maxLength":32,"pattern":"^(CWE-[1-9][0-9]*)?$"},
        "reference":{"type":"string","maxLength":512,"pattern":"^(https://[^ ]+)?$"}
      }
    }
  }
}`

func securityReviewResponseSchema(snapshot securityReviewSnapshot) (llm.JSONSchema, error) {
	first, last := 1, max(1, snapshot.file.LineCount)
	required := []string{"path", "start_line", "end_line"}
	line := map[string]any{"type": "integer", "minimum": first, "maximum": last}
	anchor, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false, "required": required,
		"properties": map[string]any{
			"path":       map[string]any{"enum": []string{snapshot.file.Path}},
			"start_line": line, "end_line": line,
		},
	})
	if err != nil {
		return llm.JSONSchema{}, fmt.Errorf("encode security review anchor schema: %w", err)
	}
	maxFindings := 5
	if snapshot.file.LineCount == 0 {
		maxFindings = 0
	}
	return llm.JSONSchema{Name: "security_review_response", Schema: json.RawMessage(fmt.Sprintf(securityReviewResponseSchemaDocument, maxFindings, anchor))}, nil
}
