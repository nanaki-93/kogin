package securityrules

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
)

// SecurityRuleSetVersion names the rules below and the Go AST rules. Bump it
// when a rule changes so stored rule reports are scanned again.
const SecurityRuleSetVersion = "rules-v2"

// ruleKind is what a family of patterns finds, in every language.
type ruleKind struct {
	id, category, title, severity, confidence, cwe     string
	observed, preconditions, remediation, verification string
	// skipTests leaves test and fixture files out: their keys and digests
	// are test vectors, and tests turn verification off on purpose.
	skipTests bool
}

// packRule is one tree-sitter query for some grammars. Its captures:
//   - @match anchors the finding (required);
//   - @dynamic must not be a fixed string literal;
//   - @sql must not be a fixed literal and must read as an SQL statement;
//   - @built must be a string assembled at run time (interpolation or
//     concatenation), and @sqlbuilt one that reads as an SQL statement;
//   - @name must name a secret, and @secret must be a literal that looks
//     like one.
type packRule struct {
	kind     *ruleKind
	grammars string
	query    string
}

var (
	sqlStatement = regexp.MustCompile(`(?is)(\bselect\s.*\sfrom\b|\binsert\s+into\b|\bupdate\s+\S+\s+set\b|\bdelete\s+from\b|\bexec(ute)?\s+\w|\bdrop\s+table\b)`)
	secretName   = regexp.MustCompile(`(?i)(passw(or)?d|passwd|pwd|secret|api_?key|access_?key|private_?key|(auth|access|api|bearer|refresh|session|github|slack)_?token)$`)
	placeholder  = regexp.MustCompile(`(?i)^(\*+|x+|\.+|-+|changeme|change_me|password|secret|example|sample|dummy|test|testing|placeholder|todo|null|none|redacted|your[-_ ].*|<.*>|\$\{.*\}|\{\{.*\}\}|%.*%|\$[a-z_][a-z0-9_]*)$`)
	envName      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	// namesASecret matches values that name a secret instead of being one:
	// a route, a label or a key such as "forgot_password".
	namesASecret = regexp.MustCompile(`(?i)(password|passwd|secret|token|api_?key)`)
	builtKinds   = map[string]bool{"string": true, "template_string": true, "encapsed_string": true, "string_literal": true,
		"interpolated_string_expression": true, "binary": true, "binary_expression": true, "binary_operator": true, "additive_expression": true}
	// vendored and bundled code is someone else's to fix.
	vendorPath = regexp.MustCompile(`(?i)(^|/)(vendor|third[_-]?party|node_modules|bower_components)/|[.-]min\.js$|\.bundle\.js$`)
	testPath   = regexp.MustCompile(`(?i)(^|/)(tests?|spec|specs|__tests__|testdata|fixtures?|mocks?|examples?|e2e|cypress)/|(_test\.go|_test\.py|_spec\.rb|\.test\.[cm]?[jt]sx?|\.spec\.[cm]?[jt]sx?|Tests?\.(java|kt|cs))$|(^|/)test_[^/]*\.py$`)
)

// families names the language of a grammar in rule IDs.
var families = map[string]string{"python": "python", "javascript": "javascript", "typescript": "typescript", "tsx": "typescript",
	"java": "java", "kotlin": "kotlin", "ruby": "ruby", "php": "php", "c_sharp": "csharp", "go": "go", "c": "c", "cpp": "cpp", "rust": "rust"}

// Covers reports that a rule pack reads the file at path.
func Covers(filePath string) bool {
	return families[syntax.Grammar(filePath)] != ""
}

// ScanFile evaluates the deterministic rules for the file's language: the Go
// AST rules for Go, and the tree-sitter rule pack its grammar has. Findings
// are bounded, most severe first and then in source order.
func ScanFile(indexed IndexFile, source string) (SecurityRuleScanResult, error) {
	candidates, err := scanCandidates(indexed, source)
	if err != nil {
		return SecurityRuleScanResult{}, err
	}
	return bound(candidates), nil
}

func scanCandidates(indexed IndexFile, source string) ([]ruleCandidate, error) {
	grammar := syntax.Grammar(indexed.Path)
	family := families[grammar]
	if family == "" {
		return nil, ErrNoRulePack
	}
	if vendorPath.MatchString(indexed.Path) {
		return nil, nil
	}
	var candidates []ruleCandidate
	if grammar == "go" {
		found, err := scanGoCandidates(indexed, source)
		if err != nil {
			return nil, err
		}
		candidates = found
	}
	var rules []*packRule
	var queries []string
	for i := range packRules {
		rule := &packRules[i]
		if strings.Contains(" "+rule.grammars+" ", " "+grammar+" ") && !(rule.kind.skipTests && testPath.MatchString(indexed.Path)) {
			rules, queries = append(rules, rule), append(queries, rule.query)
		}
	}
	results, err := syntax.Queries(indexed.Path, []byte(source), queries)
	if err != nil {
		return nil, fmt.Errorf("%s security rules: %w", family, err)
	}
	lines := strings.Split(source, "\n")
	for i, matches := range results {
		for _, match := range matches {
			// A line this long is minified or generated, not code a person
			// maintains; its matches would only repeat what its source says.
			if anchor, ok := match.Captures["match"]; ok && len(lines[anchor.StartLine-1]) <= maxRuleLineBytes && accepts(match.Captures) {
				candidates = append(candidates, ruleCandidate{line: anchor.StartLine, finding: rules[i].finding(family, indexed.Path, anchor)})
			}
		}
	}
	return candidates, nil
}

const maxRuleLineBytes = 500

// ErrNoRulePack is returned for a file no rule pack reads.
var ErrNoRulePack = errors.New("no security rule pack covers this file type")

// captureChecks says what each named capture must be for a match to count.
var captureChecks = map[string]func(syntax.Capture) bool{
	"dynamic": func(c syntax.Capture) bool { return !c.Literal && !strings.HasPrefix(c.Text, "&") },
	"sql":     func(c syntax.Capture) bool { return !c.Literal && sqlStatement.MatchString(c.Text) },
	"built":   func(c syntax.Capture) bool { return !c.Literal && builtKinds[c.Kind] },
	"sqlbuilt": func(c syntax.Capture) bool {
		return !c.Literal && builtKinds[c.Kind] && sqlStatement.MatchString(c.Text)
	},
	"name":   func(c syntax.Capture) bool { return secretName.MatchString(strings.Trim(c.Text, `"':`)) },
	"secret": looksSecret,
}

func accepts(captures map[string]syntax.Capture) bool {
	for name, capture := range captures {
		if check := captureChecks[name]; check != nil && !check(capture) {
			return false
		}
	}
	return true
}

// looksSecret keeps literals long enough to be a credential that are not an
// obvious placeholder or the name of an environment variable.
func looksSecret(value syntax.Capture) bool {
	if !value.Literal {
		return false
	}
	text := strings.TrimSpace(unquote(value.Text))
	if len(text) < 8 || strings.ContainsAny(text, " \t\n") || placeholder.MatchString(text) || envName.MatchString(text) || namesASecret.MatchString(text) {
		return false
	}
	first := text[0]
	return strings.Trim(text, string(first)) != ""
}

// unquote drops the quotes, prefixes and raw-string markers around a literal.
func unquote(text string) string {
	text = strings.TrimLeft(text, "@$rbuRBUfF")
	return strings.Trim(text, "\"'`")
}

func (rule *packRule) finding(family, filePath string, anchor syntax.Capture) SecurityFinding {
	kind := rule.kind
	finding := SecurityFinding{
		Rule: family + "." + kind.id, Category: kind.category, Title: kind.title,
		Severity: kind.severity, Confidence: kind.confidence, EvidenceKind: "rule_match", CWE: kind.cwe,
		ObservedCondition: kind.observed, Preconditions: kind.preconditions, Remediation: kind.remediation, VerificationIdea: kind.verification,
		Anchor: SecuritySourceAnchor{Path: filePath, StartLine: anchor.StartLine, EndLine: anchor.EndLine},
		Triage: SecurityTriageOpen, VerificationState: SecurityVerificationUnverified,
	}
	finding.ID = SecurityFindingID(finding)
	return finding
}

type ruleCandidate struct {
	line    int
	finding SecurityFinding
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}

// bound keeps the most severe findings first, then source order, drops
// duplicates, and caps the result.
func bound(candidates []ruleCandidate) SecurityRuleScanResult {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := severityRank[candidates[i].finding.Severity], severityRank[candidates[j].finding.Severity]
		if left != right {
			return left < right
		}
		return candidates[i].line < candidates[j].line
	})
	result := SecurityRuleScanResult{Findings: []SecurityFinding{}}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate.finding.ID]; duplicate {
			continue
		}
		seen[candidate.finding.ID] = struct{}{}
		if len(result.Findings) == maxSecurityFindings {
			result.Truncated = true
			continue
		}
		result.Findings = append(result.Findings, candidate.finding)
	}
	return result
}
