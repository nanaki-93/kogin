package project

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

// AgentScope is an admitted source span, never a grant to edit a file.
type AgentScope struct {
	Path      string `json:"path"`
	Hash      string `json:"hash"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type AgentQuery struct {
	Path      string `json:"path"`
	Query     string `json:"query,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
}

type AgentEvidence struct {
	AgentScope
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Confidence string `json:"confidence"`
}

type AgentEvidenceResult struct {
	Status    string          `json:"status"`
	Evidence  []AgentEvidence `json:"evidence"`
	Requested *AgentQuery     `json:"requested,omitempty"`
	Truncated bool            `json:"truncated"`
}

// AgentContext owns a copy of admitted index facts. It cannot discover files or
// widen its reader's scope. Heuristic relationships remain explicitly advisory.
type AgentContext struct {
	files  map[string]IndexFile
	scopes map[string]AgentScope
	read   func(context.Context, string, string) ([]byte, error)
}

func NewAgentContext(index *ProjectIndex, scopes []AgentScope, read func(context.Context, string, string) ([]byte, error)) (*AgentContext, error) {
	if index == nil || read == nil || len(scopes) == 0 || len(scopes) > 64 {
		return nil, fmt.Errorf("bounded indexed agent scope is required")
	}
	result := &AgentContext{files: make(map[string]IndexFile), scopes: make(map[string]AgentScope), read: read}
	for _, scope := range scopes {
		file := functionContextFile(index, scope.Path)
		if file == nil || file.Binary || file.ContentHash != scope.Hash || scope.StartLine < 1 || scope.EndLine < scope.StartLine || scope.EndLine > file.LineCount {
			return nil, fmt.Errorf("invalid indexed agent scope")
		}
		if _, exists := result.scopes[scope.Path]; exists {
			return nil, fmt.Errorf("duplicate agent scope")
		}
		file.Imports = slices.Clone(file.Imports)
		file.Diagnostics = nil
		result.files[scope.Path], result.scopes[scope.Path] = *file, scope
	}
	return result, nil
}

func (c *AgentContext) Retrieve(ctx context.Context, kind string, query AgentQuery) (AgentEvidenceResult, error) {
	result := AgentEvidenceResult{Status: "ok", Evidence: []AgentEvidence{}}
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	scope, present := c.scopes[query.Path]
	if !present || outsideAgentSpan(scope, query) {
		result.Status, result.Requested = "scope_required", &query
		return result, nil
	}
	data, err := c.read(ctx, scope.Path, scope.Hash)
	if err != nil {
		return result, err
	}
	switch kind {
	case "read_snippet":
		if query.StartLine > 0 {
			scope.StartLine, scope.EndLine = query.StartLine, query.EndLine
		}
		result.add(scope, kind, sourceLines(string(data), scope.StartLine, scope.EndLine), "exact")
	case "search_repository":
		if strings.TrimSpace(query.Query) == "" || len(query.Query) > 256 {
			return result, fmt.Errorf("search text must contain 1–256 bytes")
		}
		for offset, line := range strings.Split(sourceLines(string(data), scope.StartLine, scope.EndLine), "\n") {
			if strings.Contains(line, query.Query) {
				match := scope
				match.StartLine, match.EndLine = scope.StartLine+offset, scope.StartLine+offset
				result.add(match, kind, line, "exact")
			}
		}
	case "inspect_imports", "inspect_impact":
		c.indexEvidence(&result, kind, scope)
	default:
		return result, fmt.Errorf("unsupported repository query")
	}
	return result, context.Cause(ctx)
}

func outsideAgentSpan(scope AgentScope, query AgentQuery) bool {
	if query.StartLine == 0 && query.EndLine == 0 {
		return false
	}
	return query.StartLine < scope.StartLine || query.EndLine > scope.EndLine || query.EndLine < query.StartLine
}

func (c *AgentContext) indexEvidence(result *AgentEvidenceResult, kind string, scope AgentScope) {
	file := c.files[scope.Path]
	switch kind {
	case "inspect_imports":
		for _, path := range file.Imports {
			result.add(scope, kind, path, "indexed")
		}
	case "inspect_impact":
		index := &ProjectIndex{}
		for _, path := range slices.Sorted(maps.Keys(c.files)) {
			index.Files = append(index.Files, c.files[path])
		}
		impact := BuildImpactPreview(index, scope.Path)
		for _, reference := range impact.References {
			result.add(c.scopes[reference.Path], kind, reference.Reason, "heuristic")
		}
	}
}

func (r *AgentEvidenceResult) add(scope AgentScope, kind, text, confidence string) {
	if len(r.Evidence) == 20 {
		r.Truncated = true
		return
	}
	remaining := 8192
	encoded, _ := json.Marshal(r.Evidence)
	itemMetadata, _ := json.Marshal(AgentEvidence{AgentScope: scope, Kind: kind, Confidence: confidence})
	payloadRemaining := (12 << 10) - len(encoded) - len(itemMetadata) - 256
	for _, item := range r.Evidence {
		remaining -= len(item.Text)
	}
	remaining = min(remaining, payloadRemaining)
	if remaining <= 0 {
		r.Truncated = true
		return
	}
	if len(text) > remaining {
		text = textutil.Truncate(text, remaining)
		r.Truncated = true
	}
	r.Evidence = append(r.Evidence, AgentEvidence{AgentScope: scope, Kind: kind, Text: text, Confidence: confidence})
}
