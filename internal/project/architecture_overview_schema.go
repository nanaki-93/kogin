package project

import (
	"encoding/json"
	"fmt"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
)

// Optional insights use null in strict provider output; the parser still accepts
// omitted insights from older reports and independently enforces byte limits.
const projectAnalysisResponseSchemaDocument = `{
 "type":"object","additionalProperties":false,
 "required":["purpose","architecture","components","walkthroughs","risks","engineering_insight"],
 "properties":{
  "purpose":{"type":"string","minLength":1,"maxLength":%[3]d},
  "architecture":{"type":"string","minLength":1,"maxLength":%[4]d},
  "components":{"type":"array","maxItems":%[5]d,"items":{"$ref":"#/$defs/text"}},
  "walkthroughs":{"type":"array","maxItems":%[6]d,"items":{"$ref":"#/$defs/walkthrough"}},
  "risks":{"type":"array","maxItems":%[1]d,"items":{"$ref":"#/$defs/risk"}},
  "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
 },
 "$defs":{
  "text":{"type":"string","minLength":1,"maxLength":%[2]d},
  "walkthrough":{
   "type":"object","additionalProperties":false,
   "required":["title","summary","steps"],
   "properties":{
    "title":{"type":"string","minLength":1,"maxLength":%[7]d},
    "summary":{"type":"string","minLength":1,"maxLength":%[8]d},
    "steps":{"type":"array","minItems":1,"maxItems":%[9]d,"items":{"$ref":"#/$defs/step"}}
   }
  },
  "step":{
   "type":"object","additionalProperties":false,
   "required":["text","path","symbol"],
   "properties":{
    "text":{"type":"string","minLength":1,"maxLength":%[10]d},
    "path":{"type":"string","minLength":1,"maxLength":%[11]d},
    "symbol":{"type":"string","maxLength":%[12]d}
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
	document := fmt.Sprintf(projectAnalysisResponseSchemaDocument,
		maxProjectAnalysisItems, maxProjectAnalysisItemBytes, maxOverviewPurpose, maxOverviewArchitecture, maxOverviewComponents,
		maxWalkthroughs, maxWalkthroughTitle, maxWalkthroughSummary, maxWalkthroughSteps, maxWalkthroughStepText, maxWalkthroughPath, maxWalkthroughSymbol)
	return llm.JSONSchema{Name: "project_analysis_response", Schema: json.RawMessage(document)}
}
