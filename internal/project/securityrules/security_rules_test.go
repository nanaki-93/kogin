package securityrules

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project/symbols"
)

func TestGoSecurityRulesMatchesOnlyExactSourcePatterns(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		source     string
		rules      []string
		conditions []string
	}{
		{
			name: "TLS config literal", path: "transport.go",
			source:     "package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n",
			rules:      []string{"go.tls.insecure-skip-verify"},
			conditions: []string{"composite literal"},
		},
		{
			name: "TLS alias in test fixture", path: "transport_test.go",
			source:     "package fixture\nimport secure \"crypto/tls\"\nfunc TestConfig() { _ = secure.Config{InsecureSkipVerify: true} }\n",
			rules:      []string{"go.tls.insecure-skip-verify"},
			conditions: []string{"composite literal"},
		},
		{
			name: "TLS config assignment", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() { config := tls.Config{}; config.InsecureSkipVerify = true }\n",
			rules:  []string{"go.tls.insecure-skip-verify"}, conditions: []string{"field assignment"},
		},
		{
			name: "parenthesized pointer TLS config assignment", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() { config := tls.Config{}; (&config).InsecureSkipVerify = (true) }\n",
			rules:  []string{"go.tls.insecure-skip-verify"},
		},
		{
			name: "indexed TLS config collections", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() {\n\tvar values []tls.Config\n\tvar pointers map[string]*tls.Config\n\tvalues[0].InsecureSkipVerify = true\n\tpointers[\"x\"].InsecureSkipVerify = true\n}\n",
			rules:  []string{"go.tls.insecure-skip-verify", "go.tls.insecure-skip-verify"},
		},
		{
			name: "inferred TLS config collection assignment", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() {\n\tvalues, array, pointers := []tls.Config{}, [1]*tls.Config{}, map[string]*tls.Config{}\n\tvalues[0].InsecureSkipVerify = true\n\tarray[0].InsecureSkipVerify = true\n\tpointers[\"x\"].InsecureSkipVerify = true\n}\n",
			rules:  []string{"go.tls.insecure-skip-verify", "go.tls.insecure-skip-verify", "go.tls.insecure-skip-verify"},
		},
		{
			name: "package TLS config and collection bindings", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nvar config tls.Config\nvar values = []tls.Config{}\nfunc Run() {\n\tconfig.InsecureSkipVerify = true\n\tvalues[0].InsecureSkipVerify = true\n}\n",
			rules:  []string{"go.tls.insecure-skip-verify", "go.tls.insecure-skip-verify"},
		},
		{
			name: "dot imports", path: "dot.go",
			source:     "package fixture\nimport (. \"crypto/tls\"; . \"os/exec\")\nfunc Run(input string) { _ = Config{InsecureSkipVerify: true}; _ = Command(\"sh\", \"-c\", input) }\n",
			rules:      []string{"go.tls.insecure-skip-verify", "go.shell.dynamic-command"},
			conditions: []string{"composite literal", "sh-family -c"},
		},
		{
			name: "shadowed dot imports", path: "dot.go",
			source: "package fixture\nimport (. \"crypto/tls\"; . \"os/exec\")\nfunc Run(input string) { Config := struct{}{}; Command := func(string, string, string) any { return nil }; _ = Config; _ = Command(\"sh\", \"-c\", input) }\n",
		},
		{
			name: "dot-imported TLS config shadowed by local type", path: "dot.go",
			source: "package fixture\nimport . \"crypto/tls\"\nfunc Run() { type Config struct { InsecureSkipVerify bool }; _ = Config{InsecureSkipVerify: true} }\n",
		},
		{
			name: "dot-imported TLS config shadowed by type parameter", path: "dot.go",
			source: "package fixture\nimport . \"crypto/tls\"\nfunc Run[Config ~struct { InsecureSkipVerify bool }]() { _ = Config{InsecureSkipVerify: true} }\n",
		},
		{
			name: "dot-imported TLS config shadowed by receiver type parameter", path: "dot.go",
			source: "package fixture\nimport . \"crypto/tls\"\ntype Box[T ~struct { InsecureSkipVerify bool }] struct{}\nfunc (box Box[Config]) Run() { _ = Config{InsecureSkipVerify: true} }\n",
		},
		{
			name: "local type shadow ends with its block", path: "dot.go",
			source: "package fixture\nimport . \"crypto/tls\"\nfunc Run() { { type Config struct { InsecureSkipVerify bool }; _ = Config{InsecureSkipVerify: true} }; _ = Config{InsecureSkipVerify: true} }\n",
			rules:  []string{"go.tls.insecure-skip-verify"},
		},
		{
			name: "lookalike dot imports", path: "dot.go",
			source: "package fixture\nimport (. \"example.test/tls\"; . \"example.test/exec\")\nfunc Run(input string) { _ = Config{InsecureSkipVerify: true}; _ = Command(\"sh\", \"-c\", input) }\n",
		},
		{
			name: "dynamic shell command", path: "command.go",
			source:     "package fixture\nimport runner \"os/exec\"\nfunc Run(input string) { _ = runner.Command(\"sh\", \"-c\", \"echo \"+input) }\n",
			rules:      []string{"go.shell.dynamic-command"},
			conditions: []string{"sh-family -c"},
		},
		{
			name: "dynamic shell command context", path: "command.go",
			source:     "package fixture\nimport (\"context\"; \"os/exec\")\nfunc Run(input string) { _ = exec.CommandContext(context.Background(), \"bash\", \"-c\", input) }\n",
			rules:      []string{"go.shell.dynamic-command"},
			conditions: []string{"sh-family -c"},
		},
		{
			name: "dynamic cmd command", path: "command.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"cmd.exe\", \"/C\", input) }\n",
			rules:  []string{"go.shell.dynamic-command"}, conditions: []string{"cmd /c"},
		},
		{
			name: "dynamic PowerShell command", path: "command.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(input string) { _ = exec.Command(\"pwsh\", \"-Command\", input) }\n",
			rules:  []string{"go.shell.dynamic-command"}, conditions: []string{"PowerShell -Command"},
		},
		{
			name: "static shell command", path: "command.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run() { _ = exec.Command(\"sh\", \"-c\", \"echo \"+\"safe\") }\n",
		},
		{
			name: "parenthesized static shell command", path: "command.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run() { _ = exec.Command((\"sh\"), (\"-c\"), (\"echo \"+\"safe\")) }\n",
		},
		{
			name: "static cmd and PowerShell commands", path: "command.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run() { _ = exec.Command(\"cmd\", \"/c\", \"echo safe\"); _ = exec.Command(\"powershell\", \"-c\", \"Write-Output safe\") }\n",
		},
		{
			name: "TLS false literal", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: false} }\n",
		},
		{
			name: "shadowed true identifier", path: "transport.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run(true bool) { _ = tls.Config{InsecureSkipVerify: true}; config := tls.Config{}; config.InsecureSkipVerify = true }\n",
		},
		{
			name: "lookalike import", path: "lookalike.go",
			source: "package fixture\nimport tls \"example.test/tls\"\nfunc Run() { _ = tls.Config{InsecureSkipVerify: true} }\n",
		},
		{
			name: "lookalike config assignment", path: "lookalike.go",
			source: "package fixture\nimport tls \"example.test/tls\"\nfunc Run() { config := tls.Config{}; config.InsecureSkipVerify = true }\n",
		},
		{
			name: "shadowed TLS import", path: "shadow.go",
			source: "package fixture\nimport \"crypto/tls\"\nfunc Run() { tls := struct{}{}; _ = tls.Config{InsecureSkipVerify: true} }\n",
		},
		{
			name: "shadowed exec import", path: "shadow.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(input string) { exec := struct{ Command func(string, string, string) any }{}; _ = exec.Command(\"sh\", \"-c\", input) }\n",
		},
		{
			name: "switch clauses do not share shadows", path: "switch.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(input string) { switch input { case \"first\": exec := struct{}{}; _ = exec; case \"second\": _ = exec.Command(\"sh\", \"-c\", input) } }\n",
			rules:  []string{"go.shell.dynamic-command"},
		},
		{
			name: "type switch clauses do not share shadows", path: "switch.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(value any, input string) { switch value.(type) { case string: exec := struct{}{}; _ = exec; case nil: _ = exec.Command(\"sh\", \"-c\", input) } }\n",
			rules:  []string{"go.shell.dynamic-command"},
		},
		{
			name: "select clauses do not share shadows", path: "select.go",
			source: "package fixture\nimport \"os/exec\"\nfunc Run(ch <-chan int, input string) { select { case <-ch: exec := struct{}{}; _ = exec; case <-ch: _ = exec.Command(\"sh\", \"-c\", input) } }\n",
			rules:  []string{"go.shell.dynamic-command"},
		},
		{
			name: "unrelated command", path: "lookalike.go",
			source: "package fixture\nimport fake \"example.test/exec\"\nfunc Run(input string) { _ = fake.Command(\"sh\", \"-c\", input) }\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			indexed := securityRuleIndexedFile(test.path, test.source)
			scan, err := ScanFile(indexed, test.source)
			if err != nil {
				t.Fatal(err)
			}
			findings := scan.Findings
			if len(findings) != len(test.rules) {
				t.Fatalf("findings = %+v, want rules %v", findings, test.rules)
			}
			for index, rule := range test.rules {
				finding := findings[index]
				if finding.Rule != rule || finding.EvidenceKind != "rule_match" || finding.Anchor.Path != test.path || finding.Anchor.StartLine < 3 || finding.Anchor.EndLine != finding.Anchor.StartLine {
					t.Fatalf("finding = %+v", finding)
				}
				if !strings.Contains(finding.Preconditions, "unknown") || !strings.Contains(finding.Preconditions, "syntactic review") {
					t.Fatalf("finding is overconfident: %+v", finding)
				}
				if index < len(test.conditions) && !strings.Contains(finding.ObservedCondition, test.conditions[index]) {
					t.Fatalf("finding condition = %q, want %q", finding.ObservedCondition, test.conditions[index])
				}
			}
		})
	}
}

func TestGoSecurityRulesBoundsResultsInSourceOrder(t *testing.T) {
	source := "package fixture\nimport \"os/exec\"\nfunc Run(input string) {\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n}\n"
	scan, err := ScanFile(securityRuleIndexedFile("command.go", source), source)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.Truncated || len(scan.Findings) != maxSecurityFindings {
		t.Fatalf("bounded scan = %+v", scan)
	}
	for index, finding := range scan.Findings {
		if finding.Anchor.StartLine != index+4 {
			t.Fatalf("finding %d anchor = %+v", index, finding.Anchor)
		}
	}
}

func TestGoSecurityRulesSortsCandidatesBeforeCapping(t *testing.T) {
	source := "package fixture\nimport (\"crypto/tls\"; \"os/exec\")\nvar values []tls.Config\nfunc Run(input string) {\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\t_ = exec.Command(\"sh\", \"-c\", input)\n\tvalues[0].InsecureSkipVerify, _ = true, exec.Command(\"sh\", \"-c\", input)\n}\n"
	scan, err := ScanFile(securityRuleIndexedFile("ordered.go", source), source)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.Truncated || len(scan.Findings) != maxSecurityFindings {
		t.Fatalf("ordered bounded scan = %+v", scan)
	}
	if last := scan.Findings[len(scan.Findings)-1]; last.Rule != "go.tls.insecure-skip-verify" || last.Anchor.StartLine != 9 {
		t.Fatalf("fifth source-ordered finding = %+v", last)
	}
}

func securityRuleIndexedFile(path, source string) IndexFile {
	imports, _ := symbols.ExtractGoFacts(path, []byte(source))
	return IndexFile{Path: path, ContentHash: contentHash([]byte(source)), Language: "Go", LineCount: strings.Count(source, "\n"), Imports: imports}
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
