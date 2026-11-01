package app

import (
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

// The model supplies line ranges only. Declaration labels are derived from the
// index after validating the range, so spans across declarations remain valid.
const performanceReviewResponseSchemaDocument = `{
 "type":"object","additionalProperties":false,"required":["findings"],
 "properties":{"findings":{"type":"array","maxItems":%d,"items":{"$ref":"#/$defs/finding"}}},
 "$defs":{
  "prose":{"type":"string","minLength":1,"maxLength":1000},
  "line":{"type":"integer","minimum":1,"maximum":%d},
  "finding":{
   "type":"object","additionalProperties":false,
   "required":["category","potential_impact","confidence","title","observed_pattern","workload_conditions","recommendation","tradeoff","verification_plan","start_line","end_line"],
   "properties":{
    "category":{"enum":["cpu","memory","io","concurrency","caching","ui"]},
    "potential_impact":{"enum":["high","medium","low","unknown"]},
    "confidence":{"enum":["high","medium","low"]},
    "title":{"$ref":"#/$defs/prose"},
    "observed_pattern":{"$ref":"#/$defs/prose"},
    "workload_conditions":{"$ref":"#/$defs/prose"},
    "recommendation":{"$ref":"#/$defs/prose"},
    "tradeoff":{"$ref":"#/$defs/prose"},
    "verification_plan":{"$ref":"#/$defs/prose"},
    "start_line":{"$ref":"#/$defs/line"},"end_line":{"$ref":"#/$defs/line"},
    "engineering_insight":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/insight"}]}
   }
  },
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

func performanceReviewResponseSchema(snapshot performanceReviewSnapshot) llm.JSONSchema {
	maxFindings := 5
	if snapshot.file.LineCount == 0 {
		maxFindings = 0
	}
	return llm.JSONSchema{Name: "performance_review_response", Schema: json.RawMessage(fmt.Sprintf(performanceReviewResponseSchemaDocument, maxFindings, max(1, snapshot.file.LineCount)))}
}
