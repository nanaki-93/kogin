package project

import (
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/kogin/v2/internal/llm"
)

// Optional insights use null in strict provider output; the parser still accepts
// omitted insights from older reports and independently enforces byte limits.
// The limits here are loose on purpose: CLI runners check a reply against the
// schema and refuse it whole, so the prompt asks for short text and the
// parser trims what is longer (boundedWalkthroughs) instead.
const projectAnalysisResponseSchemaDocument = `{
 "type":"object","additionalProperties":false,
 "required":["purpose","architecture","components","walkthroughs","risks","engineering_insight"],
 "properties":{
  "purpose":{"$ref":"#/$defs/text"},
  "architecture":{"$ref":"#/$defs/text"},
  "components":{"$ref":"#/$defs/textList"},
  "walkthroughs":{"type":"array","maxItems":%[1]d,"items":{"$ref":"#/$defs/walkthrough"}},
  "risks":{"type":"array","maxItems":%[1]d,"items":{"$ref":"#/$defs/risk"}},
  "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
 },
 "$defs":{
  "text":{"type":"string","minLength":1,"maxLength":%[2]d},
  "textList":{"type":"array","maxItems":%[1]d,"items":{"$ref":"#/$defs/text"}},
  "walkthrough":{
   "type":"object","additionalProperties":false,
   "required":["title","summary","steps"],
   "properties":{
    "title":{"$ref":"#/$defs/text"},
    "summary":{"$ref":"#/$defs/text"},
    "steps":{"type":"array","minItems":1,"maxItems":%[1]d,"items":{"$ref":"#/$defs/step"}}
   }
  },
  "step":{
   "type":"object","additionalProperties":false,
   "required":["text","path","symbol"],
   "properties":{
    "text":{"$ref":"#/$defs/text"},
    "path":{"$ref":"#/$defs/text"},
    "symbol":{"type":"string","maxLength":%[2]d}
   }
  },
  "risk":{
   "type":"object","additionalProperties":false,
   "required":["severity","summary","engineering_insight"],
   "properties":{
    "severity":{"type":"string","enum":["low","medium","high"]},
    "summary":{"$ref":"#/$defs/text"},
    "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
   }
  },
  "optionalInsight":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/insight"}]},
  "insightText":{"type":"string","minLength":1,"maxLength":250},
  "insight":{
   "type":"object","additionalProperties":false,
   "required":["mechanism","why_it_matters_here","tradeoff_or_failure_mode","transferable_lesson"],
   "properties":{
    "mechanism":{"$ref":"#/$defs/insightText"},
    "why_it_matters_here":{"$ref":"#/$defs/insightText"},
    "tradeoff_or_failure_mode":{"$ref":"#/$defs/insightText"},
    "transferable_lesson":{"$ref":"#/$defs/insightText"}
   }
  }
 }
}`

func projectAnalysisResponseSchema() llm.JSONSchema {
	document := fmt.Sprintf(projectAnalysisResponseSchemaDocument, maxProjectAnalysisItems, maxProjectAnalysisItemBytes)
	return llm.JSONSchema{Name: "project_analysis_response", Schema: json.RawMessage(document)}
}
