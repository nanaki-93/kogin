package app

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/project"
)

const repositoryQuerySchema = `{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512},"query":{"type":"string","maxLength":256},"start_line":{"type":"integer","minimum":1,"maximum":1000000},"end_line":{"type":"integer","minimum":1,"maximum":1000000}},"required":["path"],"additionalProperties":false}`
const repositoryEvidenceSchema = `{"type":"object","properties":{"status":{"type":"string","enum":["ok","scope_required","unavailable","stale"]},"evidence":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"path":{"type":"string"},"hash":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"},"kind":{"type":"string"},"text":{"type":"string"},"confidence":{"type":"string"}},"required":["path","hash","start_line","end_line","kind","text","confidence"],"additionalProperties":false}},"requested":{"type":"object"},"truncated":{"type":"boolean"}},"required":["status","evidence","truncated"],"additionalProperties":false}`

func repositoryTools(reader *project.AgentContext) []agentruntime.ToolSpec {
	descriptions := map[string]string{
		"read_snippet":      "Read only the admitted source span. A wider path or line range returns scope_required.",
		"search_repository": "Search literal text within an admitted file; returns at most 20 hash-bound matches.",
		"inspect_imports":   "Inspect admitted indexed imports. An import is not permission to read its destination.",
		"inspect_impact":    "Inspect heuristic import relationships within admitted scope; this is not a verified call graph.",
	}
	var result []agentruntime.ToolSpec
	for _, name := range slices.Sorted(maps.Keys(descriptions)) {
		result = append(result, agentruntime.ToolSpec{Name: name, Description: descriptions[name], InputSchema: json.RawMessage(repositoryQuerySchema), OutputSchema: json.RawMessage(repositoryEvidenceSchema), Run: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			var query project.AgentQuery
			if err := json.Unmarshal(args, &query); err != nil {
				return nil, err
			}
			evidence, err := reader.Retrieve(ctx, name, query)
			if err != nil {
				return nil, err
			}
			return json.Marshal(evidence)
		}})
	}
	return result
}
