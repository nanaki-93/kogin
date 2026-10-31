package syntax

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Capture is one node a query captured: its text, kind and lines.
type Capture struct {
	Text string
	// Kind is the grammar's name for the node ("call_expression").
	Kind string
	// Literal reports a string literal with nothing interpolated into it,
	// in any grammar: its text is fixed in the source.
	Literal   bool
	StartLine int
	EndLine   int
}

// Match is one match of a query: its pattern and captures by name.
type Match struct {
	Pattern  int
	Captures map[string]Capture
}

// ErrNoGrammar is returned for a file no embedded grammar reads.
var ErrNoGrammar = errors.New("no grammar for this file type")

// Grammar names the grammar that reads a path ("python", "tsx"); empty when
// none does. Go files are named "go".
func Grammar(path string) string {
	if entry := grammars.DetectLanguage(filepath.Base(path)); entry != nil {
		return entry.Name
	}
	return ""
}

var (
	queriesMu sync.Mutex
	queries   = map[[2]string]*gotreesitter.Query{}
)

// Queries parses source once with the grammar its path names and runs each
// tree-sitter query on it, returning the matches of each in order. Queries
// are compiled once per grammar. A file the parser stops on, or that is too
// large, is an error, never an empty result.
func Queries(path string, source []byte, queries []string) ([][]Match, error) {
	entry := grammars.DetectLanguage(filepath.Base(path))
	if entry == nil {
		return nil, ErrNoGrammar
	}
	if len(source) > maxSourceBytes {
		return nil, fmt.Errorf("file too large to parse")
	}
	g, err := loadGrammar(entry)
	if err != nil {
		return nil, err
	}
	compiled := make([]*gotreesitter.Query, len(queries))
	for i, query := range queries {
		if compiled[i], err = compileQuery(g, query); err != nil {
			return nil, err
		}
	}
	parser := gotreesitter.NewParser(g.language)
	parser.SetTimeoutMicros(parseTimeoutMicros)
	parser.SetMemoryBudgetBytes(parseMemoryBudget)
	tree, err := parser.ParseStrict(source)
	if tree != nil {
		defer tree.Release()
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", g.name, err)
	}
	results := make([][]Match, len(compiled))
	for i, query := range compiled {
		for _, found := range query.Execute(tree) {
			match := Match{Pattern: found.PatternIndex, Captures: map[string]Capture{}}
			for _, capture := range found.Captures {
				node := capture.Node
				kind := node.Type(g.language)
				match.Captures[capture.Name] = Capture{Text: node.Text(source), Kind: kind, Literal: literal(node, kind, g.language),
					StartLine: int(node.StartPoint().Row) + 1, EndLine: int(node.EndPoint().Row) + 1}
			}
			results[i] = append(results[i], match)
		}
	}
	return results, nil
}

func compileQuery(g *grammar, query string) (*gotreesitter.Query, error) {
	queriesMu.Lock()
	defer queriesMu.Unlock()
	key := [2]string{g.name, query}
	if compiled := queries[key]; compiled != nil {
		return compiled, nil
	}
	compiled, err := gotreesitter.NewQuery(query, g.language)
	if err != nil {
		return nil, fmt.Errorf("the %s query does not compile: %w", g.name, err)
	}
	queries[key] = compiled
	return compiled, nil
}

// stringKinds are the string literal nodes of the grammars, and textKinds the
// nodes that hold their fixed text. A string node holding anything else (an
// interpolation, a variable) is built at run time.
var (
	stringKinds = map[string]bool{"string": true, "string_literal": true, "template_string": true, "encapsed_string": true,
		"interpreted_string_literal": true, "raw_string_literal": true, "verbatim_string_literal": true, "nowdoc": true,
		"subshell": true, "shell_command_expression": true}
	textKinds = map[string]bool{"string_start": true, "string_end": true, "string_content": true, "string_fragment": true,
		"string_literal_content": true, "interpreted_string_literal_content": true, "raw_string_literal_content": true,
		"raw_string_content": true, "raw_string_delimiter": true, "escape_sequence": true, "nowdoc_body": true, "nowdoc_string": true, "heredoc_start": true, "heredoc_end": true}
)

// literal reports a string literal, or literals joined at compile time
// ("a" "b" in Python and C, "a" + "b" elsewhere).
func literal(node *gotreesitter.Node, kind string, language *gotreesitter.Language) bool {
	if stringKinds[kind] {
		return onlyText(node, language)
	}
	if !joinKinds[kind] || node.NamedChildCount() == 0 {
		return false
	}
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if !literal(child, child.Type(language), language) {
			return false
		}
	}
	return true
}

// joinKinds join their operands; a join of literals is a literal.
var joinKinds = map[string]bool{"concatenated_string": true, "binary_expression": true, "binary_operator": true, "binary": true,
	"additive_expression": true, "parenthesized_expression": true}

func onlyText(node *gotreesitter.Node, language *gotreesitter.Language) bool {
	for i := 0; i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if !textKinds[child.Type(language)] || !onlyText(child, language) {
			return false
		}
	}
	return true
}
