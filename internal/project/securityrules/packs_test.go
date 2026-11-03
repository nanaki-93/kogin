package securityrules

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project/syntax"
)

var grammarSamples = map[string]string{"python": "a.py", "javascript": "a.js", "typescript": "a.ts", "tsx": "a.tsx", "java": "A.java",
	"kotlin": "a.kt", "ruby": "a.rb", "php": "a.php", "c_sharp": "a.cs", "go": "a.go", "c": "a.c", "cpp": "a.cpp", "rust": "a.rs"}

func TestEveryRuleCompilesForItsGrammars(t *testing.T) {
	for _, rule := range packRules {
		for _, grammar := range strings.Fields(rule.grammars) {
			sample := grammarSamples[grammar]
			if sample == "" || families[grammar] == "" || syntax.Grammar(sample) != grammar {
				t.Fatalf("rule %s names grammar %q with no sample", rule.kind.id, grammar)
			}
			if _, err := syntax.Queries(sample, []byte(""), []string{rule.query}); err != nil {
				t.Fatalf("%s rule %s: %v", grammar, rule.kind.id, err)
			}
		}
	}
}

// Each sample marks the lines a rule must find with "<- rule"; every other
// line must stay quiet.
func TestRulePacksFindRisksInEveryLanguage(t *testing.T) {
	for path, source := range packSamples {
		t.Run(path, func(t *testing.T) {
			candidates, err := scanCandidates(IndexFile{Path: path}, strings.ReplaceAll(source, "\t", "  "))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, candidate := range candidates {
				got = append(got, fmt.Sprintf("%d %s", candidate.finding.Anchor.StartLine, candidate.finding.Rule))
			}
			want := expectedFindings(source)
			sort.Strings(got)
			got = unique(got)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func expectedFindings(source string) []string {
	var want []string
	for index, line := range strings.Split(source, "\n") {
		if _, marks, ok := strings.Cut(line, "<- "); ok {
			for _, rule := range strings.Fields(marks) {
				want = append(want, fmt.Sprintf("%d %s", index+1, rule))
			}
		}
	}
	sort.Strings(want)
	return want
}

func unique(values []string) []string {
	out := values[:0]
	for index, value := range values {
		if index == 0 || value != values[index-1] {
			out = append(out, value)
		}
	}
	return out
}

func TestScanFileKeepsTheMostSevereFindingsFirst(t *testing.T) {
	source := "import hashlib\n" + strings.Repeat("hashlib.md5(b)\n", 6) + "eval(code)\n"
	scan, err := ScanFile(IndexFile{Path: "digest.py"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.Truncated || len(scan.Findings) != maxSecurityFindings || scan.Findings[0].Rule != "python.eval.dynamic-code" || scan.Findings[1].Anchor.StartLine != 2 {
		t.Fatalf("bounded scan = %+v", scan)
	}
}

func TestMinifiedLinesAreNotReviewed(t *testing.T) {
	source := "var a=1;" + strings.Repeat("b=a+1;", 100) + "eval(x);\neval(y);\n"
	candidates, err := scanCandidates(IndexFile{Path: "vendor.js"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].line != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestSecretsAndWeakDigestsInTestsAreTestVectors(t *testing.T) {
	source := "password = \"hunter2hunter2\"\nhashlib.md5(b)\nrequests.get(url, verify=False)\neval(code)\n"
	candidates, err := scanCandidates(IndexFile{Path: "tests/test_login.py"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].finding.Rule != "python.eval.dynamic-code" {
		t.Fatalf("candidates = %+v", candidates)
	}
	if vendored, err := scanCandidates(IndexFile{Path: "static/vendor/jquery.js"}, "eval(code)\n"); err != nil || len(vendored) != 0 {
		t.Fatalf("vendored code = %+v, %v", vendored, err)
	}
	if _, err := ScanFile(IndexFile{Path: "notes.txt"}, "eval(x)"); err != ErrNoRulePack || Covers("notes.txt") || !Covers("main.go") || !Covers("app.tsx") {
		t.Fatalf("a file no pack reads: err = %v", err)
	}
}
