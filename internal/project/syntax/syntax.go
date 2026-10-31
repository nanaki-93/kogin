// Package syntax reads the structure of a source file in any language the
// embedded grammars cover: whether it parses, and its functions with their
// line spans, cyclomatic complexity and a hash of their text. Go files are read
// with go/ast so the numbers match gocyclo; every other language goes through
// a pure-Go tree-sitter runtime (no CGO).
package syntax

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// maxSourceBytes bounds what one Analyze call parses. Larger files are
// reported as not measured instead of slowing every gate down.
const maxSourceBytes = 2 << 20

// Function is one function, method or constructor in a source file.
type Function struct {
	// Name is qualified by the definitions that enclose it ("Store.add";
	// "T.Add" for a Go method on *T).
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Complexity int    `json:"complexity"`
	// Hash identifies the function's text with indentation and blank lines
	// ignored, so a function moved verbatim to another file keeps its hash.
	Hash string `json:"hash"`
}

// Lines is the number of lines the function spans.
func (f Function) Lines() int {
	return f.EndLine - f.StartLine + 1
}

// File is what the syntax layer knows about one source file.
type File struct {
	// Grammar names the parser that read the file ("go", "typescript");
	// empty when no grammar covers the file.
	Grammar string `json:"grammar,omitempty"`
	// Measured reports that the file's functions were found and measured.
	Measured bool `json:"measured"`
	// Reason says why functions were not measured.
	Reason string `json:"reason,omitempty"`
	// SyntaxError is the first syntax error ("line 3: ..."); empty when the
	// file parses cleanly or was not parsed.
	SyntaxError string     `json:"syntax_error,omitempty"`
	Functions   []Function `json:"functions,omitempty"`
}

// Analyze parses source as the language its path names.
func Analyze(path string, source []byte) File {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return analyzeGo(path, source)
	}
	if len(source) > maxSourceBytes {
		return File{Reason: "file too large to parse"}
	}
	return analyzeTreeSitter(path, source)
}

// textHash hashes text with surrounding whitespace on each line and blank
// lines dropped.
func textHash(text string) string {
	var normalized strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			normalized.WriteString(trimmed)
			normalized.WriteByte('\n')
		}
	}
	sum := sha256.Sum256([]byte(normalized.String()))
	return hex.EncodeToString(sum[:12])
}

// Enclosing names the innermost function whose span holds the line; empty
// when none does.
func Enclosing(functions []Function, line int) string {
	name, span := "", 0
	for _, function := range functions {
		if function.StartLine <= line && line <= function.EndLine && (name == "" || function.Lines() < span) {
			name, span = function.Name, function.Lines()
		}
	}
	return name
}
