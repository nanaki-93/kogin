package project

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/brand"
)

type ignoreRule struct {
	base          string
	pattern       string
	segments      []ignoreSegment
	negated       bool
	directoryOnly bool
	anchored      bool
}

type ignoreSegment struct {
	pattern string
	match   *regexp.Regexp
}

// Capture nested rules in the policy identity as well as its decisions. A new
// policy observes added/removed ignore files without retaining a stale cache.
func projectIgnoreRules(root string) ([]ignoreRule, error) {
	var rules []ignoreRule
	err := filepath.WalkDir(root, func(directory string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if directory != root && ignoredProjectDirectories[entry.Name()] {
			return filepath.SkipDir
		}
		base, err := filepath.Rel(root, directory)
		if err != nil {
			return err
		}
		base = filepath.ToSlash(base)
		if base == "." {
			base = ""
		}
		for _, name := range append([]string{".gitignore"}, brand.IgnoreFiles()...) {
			local, err := readIgnoreRules(filepath.Join(directory, name), base)
			if err != nil {
				return err
			}
			rules = append(rules, local...)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read project ignore rules: %w", err)
	}
	return rules, nil
}

func readIgnoreRules(filename, base string) ([]ignoreRule, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Git does not follow a symlink used as an ignore file. Special files must
	// not turn policy inspection into an unbounded pipe/device read either.
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read ignore file %s: %w", filename, err)
	}
	var rules []ignoreRule
	for _, line := range strings.Split(string(data), "\n") {
		if rule, ok := parseIgnoreRule(base, strings.TrimSuffix(line, "\r")); ok {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func parseIgnoreRule(base, pattern string) (ignoreRule, bool) {
	pattern = trimIgnoreSpaces(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return ignoreRule{}, false
	}
	rule := ignoreRule{base: base, pattern: pattern, negated: strings.HasPrefix(pattern, "!")}
	if rule.negated {
		pattern = strings.TrimPrefix(pattern, "!")
	}
	rule.directoryOnly = strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	rule.anchored = strings.Contains(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return ignoreRule{}, false
	}
	for _, segment := range strings.Split(pattern, "/") {
		rule.segments = append(rule.segments, ignoreSegment{segment, ignoreSegmentPattern(segment)})
	}
	return rule, true
}

func trimIgnoreSpaces(pattern string) string {
	for strings.HasSuffix(pattern, " ") {
		slashes := 0
		for i := len(pattern) - 2; i >= 0 && pattern[i] == '\\'; i-- {
			slashes++
		}
		if slashes%2 != 0 {
			break
		}
		pattern = strings.TrimSuffix(pattern, " ")
	}
	return pattern
}

// A negated child cannot reopen an excluded parent directory. Evaluate each
// ancestor first, preserving the ordering of root and nested ignore files.
func ignoredPath(rules []ignoreRule, relative string, directory bool) bool {
	parts := strings.Split(relative, "/")
	for i := range parts {
		candidate := strings.Join(parts[:i+1], "/")
		isDirectory := i < len(parts)-1 || directory
		ignored := false
		for _, rule := range rules {
			if rule.matches(candidate, isDirectory) {
				ignored = !rule.negated
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

func (rule ignoreRule) matches(candidate string, directory bool) bool {
	if rule.directoryOnly && !directory {
		return false
	}
	if rule.base != "" {
		prefix := rule.base + "/"
		if !strings.HasPrefix(candidate, prefix) {
			return false
		}
		candidate = strings.TrimPrefix(candidate, prefix)
	}
	if !rule.anchored {
		return rule.segments[0].match.MatchString(filepath.Base(candidate))
	}
	return matchIgnoreSegments(rule.segments, strings.Split(candidate, "/"))
}

func matchIgnoreSegments(pattern []ignoreSegment, parts []string) bool {
	// Iterative glob matching bounds repeated ** patterns by path depth rather
	// than exploring every possible split recursively.
	matched := make([]bool, len(parts)+1)
	matched[0] = true
	for i, segment := range pattern {
		next := make([]bool, len(parts)+1)
		for j := range next {
			if segment.pattern == "**" {
				next[j] = (i < len(pattern)-1 && matched[j]) || j > 0 && (matched[j-1] || next[j-1])
			} else if j > 0 && matched[j-1] {
				next[j] = segment.match.MatchString(parts[j-1])
			}
		}
		matched = next
	}
	return matched[len(parts)]
}

func ignoreSegmentPattern(pattern string) *regexp.Regexp {
	characters := []rune(pattern)
	var expression strings.Builder
	expression.WriteString("(?s)^")
	for i := 0; i < len(characters); i++ {
		switch characters[i] {
		case '*':
			expression.WriteString(".*")
		case '?':
			expression.WriteByte('.')
		case '\\':
			if i+1 < len(characters) {
				i++
			}
			expression.WriteString(regexp.QuoteMeta(string(characters[i])))
		case '[':
			class, end := ignoreCharacterClass(characters, i)
			expression.WriteString(class)
			i = end
		default:
			expression.WriteString(regexp.QuoteMeta(string(characters[i])))
		}
	}
	expression.WriteByte('$')
	return regexp.MustCompile(expression.String())
}

func ignoreCharacterClass(pattern []rune, start int) (string, int) {
	end := start + 1
	if end < len(pattern) && (pattern[end] == '!' || pattern[end] == '^') {
		end++
	}
	if end < len(pattern) && pattern[end] == ']' {
		end++
	}
	for ; end < len(pattern); end++ {
		if pattern[end] == '[' && end+1 < len(pattern) && pattern[end+1] == ':' {
			if closeIdx := findPOSIXClassEnd(pattern, end); closeIdx >= 0 {
				end = closeIdx
				continue
			}
		}
		if pattern[end] == ']' {
			class := string(pattern[start : end+1])
			class = strings.Replace(class, "[!", "[^", 1)
			if _, err := regexp.Compile(class); err == nil {
				return class, end
			}
			break
		}
	}
	// An unclosed/invalid character class is a literal bracket in Git patterns.
	return `\[`, start
}

func findPOSIXClassEnd(pattern []rune, start int) int {
	for i := start + 2; i+1 < len(pattern); i++ {
		if pattern[i] == ':' && pattern[i+1] == ']' {
			return i + 1
		}
	}
	return -1
}
