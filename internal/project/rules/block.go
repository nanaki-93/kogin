package rules

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// blockInfo is the info string of the fenced rules block in AGENTS.md.
const blockInfo = "kasuri"

// block is the rules one AGENTS.md declares. Paths are relative to the
// directory of that AGENTS.md. A nil budget is not declared there.
type block struct {
	Budgets struct {
		FileLines          *int `yaml:"file_lines"`
		FunctionLines      *int `yaml:"function_lines"`
		FunctionComplexity *int `yaml:"function_complexity"`
	} `yaml:"budgets"`
	Imports   []importDecl `yaml:"imports"`
	TestFiles []string     `yaml:"test_files"`
	// Checks are the commands that test and lint the subproject rooted at
	// this AGENTS.md's directory. An empty string declares that there is none.
	Checks struct {
		Test *string `yaml:"test"`
		Lint *string `yaml:"lint"`
	} `yaml:"checks"`
}

type importDecl struct {
	From  string   `yaml:"from"`
	Never []string `yaml:"never"`
}

// parseAgents reads the rules an AGENTS.md declares: its fenced kasuri
// block when it has one, otherwise the plain-language lines older guides use
// ("max file lines 800", "`a` must never import `b`").
func parseAgents(content string) (block, bool, error) {
	text, found, err := fencedBlock(content)
	if err != nil || !found {
		return legacyBlock(content), false, err
	}
	var parsed block
	decoder := yaml.NewDecoder(strings.NewReader(text))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil && !errors.Is(err, io.EOF) {
		return block{}, true, fmt.Errorf("the %s block does not parse: %s", blockInfo, strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if err := validateBlock(parsed); err != nil {
		return block{}, true, err
	}
	return parsed, true, nil
}

func fencedBlock(content string) (string, bool, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for i, line := range lines {
		fence, info := splitFence(line)
		if fence == "" || info != blockInfo {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if closing, rest := splitFence(lines[j]); closing != "" && strings.HasPrefix(closing, fence[:3]) && len(closing) >= len(fence) && rest == "" {
				return strings.Join(lines[i+1:j], "\n"), true, nil
			}
		}
		return "", true, fmt.Errorf("the %s block is not closed", blockInfo)
	}
	return "", false, nil
}

// splitFence returns a code fence (``` or ~~~, three or more) at the start of
// line and its info string.
func splitFence(line string) (string, string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return "", ""
	}
	end := 0
	for end < len(trimmed) && trimmed[end] == trimmed[0] {
		end++
	}
	if end < 3 {
		return "", ""
	}
	return trimmed[:end], strings.TrimSpace(trimmed[end:])
}

func validateBlock(parsed block) error {
	for _, budget := range []*int{parsed.Budgets.FileLines, parsed.Budgets.FunctionLines, parsed.Budgets.FunctionComplexity} {
		if budget != nil && *budget < 0 {
			return fmt.Errorf("the %s block has a negative budget", blockInfo)
		}
	}
	for _, rule := range parsed.Imports {
		if normalizePrefix(rule.From) == "" || len(normalizePaths(rule.Never)) == 0 {
			return fmt.Errorf("every import rule in the %s block needs from and never", blockInfo)
		}
	}
	for _, command := range []*string{parsed.Checks.Test, parsed.Checks.Lint} {
		if command != nil && *command != "" && len(SplitCommand(*command)) == 0 {
			return fmt.Errorf("the %s block has a check command that is not a plain argument list", blockInfo)
		}
	}
	for _, glob := range parsed.TestFiles {
		if _, ok := compileGlob(glob); !ok {
			return fmt.Errorf("test_files has an invalid pattern %q", glob)
		}
	}
	return nil
}

var (
	backtickSpanPattern   = regexp.MustCompile("`([^`]+)`")
	plainPathTokenPattern = regexp.MustCompile(`([A-Za-z0-9_./-]+)`)
	budgetNumberPattern   = regexp.MustCompile(`(\d+)`)
)

func legacyBlock(content string) block {
	var parsed block
	for _, line := range strings.Split(content, "\n") {
		parseLegacyBudget(line, &parsed)
		if from, never, ok := parseLegacyImportRule(line); ok {
			parsed.Imports = append(parsed.Imports, importDecl{From: from, Never: never})
		}
	}
	return parsed
}

func parseLegacyBudget(line string, parsed *block) {
	lower := strings.ToLower(line)
	for _, spec := range []struct {
		phrase string
		target **int
	}{
		{"max function complexity", &parsed.Budgets.FunctionComplexity},
		{"max function lines", &parsed.Budgets.FunctionLines},
		{"max file lines", &parsed.Budgets.FileLines},
	} {
		idx := strings.Index(lower, spec.phrase)
		if idx < 0 {
			continue
		}
		match := budgetNumberPattern.FindStringSubmatch(lower[idx+len(spec.phrase):])
		if len(match) != 2 {
			continue
		}
		if value, err := strconv.Atoi(match[1]); err == nil && value > 0 {
			*spec.target = &value
		}
	}
}

func parseLegacyImportRule(line string) (string, []string, bool) {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, "must never import")
	if idx < 0 {
		return "", nil, false
	}
	before := extractRulePaths(line[:idx])
	afterClause := line[idx+len("must never import"):]
	if semi := strings.IndexByte(afterClause, ';'); semi >= 0 {
		afterClause = afterClause[:semi]
	}
	after := normalizePaths(extractRulePaths(afterClause))
	if len(before) == 0 || len(after) == 0 || normalizePrefix(before[0]) == "" {
		return "", nil, false
	}
	return normalizePrefix(before[0]), after, true
}

func extractRulePaths(segment string) []string {
	var out []string
	for _, m := range backtickSpanPattern.FindAllStringSubmatch(segment, -1) {
		candidate := strings.Trim(strings.TrimSpace(m[1]), ".,;:")
		if candidate != "" && !strings.ContainsAny(candidate, " \t") {
			out = append(out, candidate)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, tokenMatch := range plainPathTokenPattern.FindAllStringSubmatch(segment, -1) {
		tok := strings.Trim(strings.TrimSpace(tokenMatch[1]), ".,;:-")
		if strings.Contains(tok, "/") || strings.Contains(tok, ".") {
			out = append(out, tok)
		}
	}
	return out
}

func normalizePrefix(raw string) string {
	return strings.Trim(filepath.ToSlash(strings.TrimSpace(raw)), "/")
}

func normalizePaths(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if norm := normalizePrefix(item); norm != "" && !slices.Contains(out, norm) {
			out = append(out, norm)
		}
	}
	return out
}
