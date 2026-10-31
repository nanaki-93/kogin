package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/mini-orca/v2/internal/project/syntax"
)

const (
	maxToolReadBytes   = 1 << 20
	maxToolReadLines   = 2000
	defaultToolLines   = 400
	maxToolLineChars   = 500
	maxToolListed      = 400
	maxToolMatches     = 100
	maxToolSearchFiles = 4000
	maxToolOutlined    = 200
)

func (run *taskToolRun) readTools() []toolSpec {
	return []toolSpec{
		{name: "read_file", readOnly: true, call: run.readFile,
			description: "Read a project file with line numbers. Use offset and limit to page through a long file.",
			schema:      `{"type":"object","properties":{"path":{"type":"string","description":"Project-relative path"},"offset":{"type":"integer","minimum":1,"description":"First line to read (1-based)"},"limit":{"type":"integer","minimum":1,"maximum":2000,"description":"Number of lines (default 400)"}},"required":["path"],"additionalProperties":false}`},
		{name: "list_files", readOnly: true, call: run.listFiles,
			description: "List project files, optionally under a directory and matching a glob such as *.ts.",
			schema:      `{"type":"object","properties":{"dir":{"type":"string","description":"Project-relative directory"},"pattern":{"type":"string","description":"Glob on the file name, or on the path when it contains a slash"}},"additionalProperties":false}`},
		{name: "search", readOnly: true, call: run.search,
			description: "Search project files for text (or a regular expression) and return matching lines as path:line: text.",
			schema:      `{"type":"object","properties":{"query":{"type":"string","minLength":1},"regex":{"type":"boolean","description":"Treat query as an RE2 regular expression"},"ignore_case":{"type":"boolean"},"pattern":{"type":"string","description":"Only files whose name (or path) matches this glob"}},"required":["query"],"additionalProperties":false}`},
		{name: "outline", readOnly: true, call: run.outline,
			description: "List the declarations of a file (functions, methods, types, classes) with their lines, in any language the parser knows.",
			schema:      `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`},
		{name: "find_definitions", readOnly: true, call: run.findDefinitions,
			description: "Find where a function, method, type or class is declared across the project.",
			schema:      `{"type":"object","properties":{"name":{"type":"string","minLength":1,"description":"Name, optionally qualified like Store.add"}},"required":["name"],"additionalProperties":false}`},
		{name: "find_references", readOnly: true, call: run.findReferences,
			description: "Find lines that use a name as a whole word across the project.",
			schema:      `{"type":"object","properties":{"name":{"type":"string","minLength":1}},"required":["name"],"additionalProperties":false}`},
	}
}

func (run *taskToolRun) readText(file string) (string, string, error) {
	clean, full, err := run.readable(file)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("%s does not exist or is not a file", clean)
	}
	if info.Size() > maxToolReadBytes {
		return "", "", fmt.Errorf("%s is larger than 1 MiB; search it instead", clean)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", "", err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", "", fmt.Errorf("%s is not a text file", clean)
	}
	return clean, string(data), nil
}

func (run *taskToolRun) readFile(_ context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	clean, text, err := run.readText(input.Path)
	if err != nil {
		return "", err
	}
	lines := splitToolLines(text)
	first := max(input.Offset, 1)
	count := input.Limit
	if count <= 0 {
		count = defaultToolLines
	}
	count = min(count, maxToolReadLines)
	if first > len(lines) && len(lines) > 0 {
		return "", fmt.Errorf("%s has %d lines", clean, len(lines))
	}
	last := min(first+count-1, len(lines))
	var out strings.Builder
	fmt.Fprintf(&out, "%s (lines %d-%d of %d)\n", clean, first, last, len(lines))
	for number := first; number <= last; number++ {
		fmt.Fprintf(&out, "%6d  %s\n", number, clip(lines[number-1], maxToolLineChars))
	}
	if last < len(lines) {
		fmt.Fprintf(&out, "… %d more lines; read on with offset %d.\n", len(lines)-last, last+1)
	}
	return out.String(), nil
}

func splitToolLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func clip(line string, limit int) string {
	if len(line) <= limit {
		return line
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + " …"
}

// projectFiles lists the files in the worktree, tracked and new, that the
// context policy admits.
func (run *taskToolRun) projectFiles(ctx context.Context) ([]string, error) {
	if run.listed != nil {
		return run.listed, nil
	}
	listed, err := gitRead(ctx, run.root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	seen := map[string]bool{}
	for _, file := range strings.Split(listed, "\x00") {
		if file == "" || seen[file] || worktreeMetadata(file) || !run.policy.Decide(file).Include {
			continue
		}
		if info, err := os.Lstat(run.root + "/" + file); err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[file] = true
		files = append(files, file)
	}
	return files, nil
}

// matchesGlob matches a glob against the file name, or against the whole
// path when the glob has a slash.
func matchesGlob(glob, file string) bool {
	if glob == "" {
		return true
	}
	if strings.Contains(glob, "/") {
		ok, _ := path.Match(glob, file)
		return ok
	}
	ok, _ := path.Match(glob, path.Base(file))
	return ok
}

func (run *taskToolRun) listFiles(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Dir     string `json:"dir"`
		Pattern string `json:"pattern"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	prefix := ""
	if strings.TrimSpace(input.Dir) != "" && strings.TrimSpace(input.Dir) != "." {
		dir, err := cleanToolPath(input.Dir)
		if err != nil {
			return "", err
		}
		prefix = dir + "/"
	}
	files, err := run.projectFiles(ctx)
	if err != nil {
		return "", err
	}
	var listed []string
	for _, file := range files {
		if strings.HasPrefix(file, prefix) && matchesGlob(input.Pattern, file) {
			listed = append(listed, file)
		}
	}
	if len(listed) == 0 {
		return "No files match.", nil
	}
	more := ""
	if len(listed) > maxToolListed {
		more = fmt.Sprintf("\n… and %d more; narrow dir or pattern.", len(listed)-maxToolListed)
		listed = listed[:maxToolListed]
	}
	return strings.Join(listed, "\n") + more, nil
}

type toolMatch struct {
	file string
	line int
	text string
}

// grep finds lines matching expression in the project's text files.
func (run *taskToolRun) grep(ctx context.Context, expression *regexp.Regexp, glob string, limit int) ([]toolMatch, bool, error) {
	files, err := run.projectFiles(ctx)
	if err != nil {
		return nil, false, err
	}
	var matches []toolMatch
	for index, file := range files {
		if index >= maxToolSearchFiles || ctx.Err() != nil {
			return matches, true, ctx.Err()
		}
		if !matchesGlob(glob, file) {
			continue
		}
		data, err := os.ReadFile(run.root + "/" + file)
		if err != nil || len(data) > maxToolReadBytes || bytes.IndexByte(data, 0) >= 0 || !expression.Match(data) {
			continue
		}
		for number, line := range splitToolLines(string(data)) {
			if expression.MatchString(line) {
				matches = append(matches, toolMatch{file: file, line: number + 1, text: line})
				if len(matches) >= limit {
					return matches, true, nil
				}
			}
		}
	}
	return matches, false, nil
}

func formatMatches(matches []toolMatch, truncated bool) string {
	if len(matches) == 0 {
		return "No matches."
	}
	var out strings.Builder
	for _, match := range matches {
		fmt.Fprintf(&out, "%s:%d: %s\n", match.file, match.line, clip(strings.TrimSpace(match.text), 300))
	}
	if truncated {
		out.WriteString("… more matches; narrow the query or pattern.\n")
	}
	return out.String()
}

func (run *taskToolRun) search(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Query      string `json:"query"`
		Regex      bool   `json:"regex"`
		IgnoreCase bool   `json:"ignore_case"`
		Pattern    string `json:"pattern"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	expression := input.Query
	if !input.Regex {
		expression = regexp.QuoteMeta(expression)
	}
	if input.IgnoreCase {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil || strings.TrimSpace(input.Query) == "" {
		return "", fmt.Errorf("invalid query: %v", err)
	}
	matches, truncated, err := run.grep(ctx, compiled, input.Pattern, maxToolMatches)
	if err != nil {
		return "", err
	}
	return formatMatches(matches, truncated), nil
}

func (run *taskToolRun) outline(_ context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	clean, text, err := run.readText(input.Path)
	if err != nil {
		return "", err
	}
	symbols := syntax.Outline(clean, []byte(text))
	if len(symbols) == 0 {
		return "No declarations found; the parser may not know this language. Read the file instead.", nil
	}
	var out strings.Builder
	for _, symbol := range symbols {
		fmt.Fprintf(&out, "%d %s %s: %s\n", symbol.Line, symbol.Kind, symbol.Name, symbol.Signature)
	}
	return out.String(), nil
}

func (run *taskToolRun) findDefinitions(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	name := strings.TrimSpace(input.Name)
	short := name[strings.LastIndex(name, ".")+1:]
	if short == "" {
		return "", fmt.Errorf("name a declaration")
	}
	// Only files that contain the name are parsed.
	matches, _, err := run.grep(ctx, regexp.MustCompile(`\b`+regexp.QuoteMeta(short)+`\b`), "", maxToolMatches*4)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	seen := map[string]bool{}
	for _, match := range matches {
		if seen[match.file] || len(seen) >= maxToolOutlined {
			continue
		}
		seen[match.file] = true
		data, err := os.ReadFile(run.root + "/" + match.file)
		if err != nil {
			continue
		}
		for _, symbol := range syntax.Outline(match.file, data) {
			if symbol.Name == name || strings.HasSuffix(symbol.Name, "."+name) {
				fmt.Fprintf(&out, "%s:%d %s: %s\n", match.file, symbol.Line, symbol.Kind, symbol.Signature)
			}
		}
	}
	if out.Len() == 0 {
		return "No declaration found by that name. Try search, or find_references.", nil
	}
	return out.String(), nil
}

func (run *taskToolRun) findReferences(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeToolArguments(arguments, &input); err != nil {
		return "", err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return "", fmt.Errorf("name what to find")
	}
	matches, truncated, err := run.grep(ctx, regexp.MustCompile(`\b`+regexp.QuoteMeta(name)+`\b`), "", maxToolMatches)
	if err != nil {
		return "", err
	}
	return formatMatches(matches, truncated), nil
}
