package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageReportsBecomeIssuesWithTheirLinesAndFunction(t *testing.T) {
	root := t.TempDir()
	source := "package store\n\nfunc Load(ids []int) {\n\tfor range ids {\n\t\tquery()\n\t}\n}\n\nfunc query() {}\n"
	if err := os.WriteFile(filepath.Join(root, "store.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := contentHash([]byte(source))
	performance := PerformanceFileReport{Path: "store.go", ContentHash: hash, Status: "completed", Findings: []PerformanceFinding{{Category: "io", PotentialImpact: "unknown", Confidence: "medium",
		Title: "One query per id", ObservedPattern: "Load queries once per id.", Recommendation: "Query all ids at once.", StartLine: 4, EndLine: 6}}}
	security := SecurityFileReport{Path: "store.go", ContentHash: hash, Status: SecurityStatusCompleted, Source: SecuritySourceDeterministic, Findings: []SecurityFinding{{Rule: "go.shell.dynamic-command", CWE: "CWE-78",
		Title: "Command built from input", Anchor: SecuritySourceAnchor{Path: "store.go", StartLine: 9, EndLine: 9}, Severity: "critical", ObservedCondition: "A command is built from input.", Remediation: "Pass arguments separately."}}}
	findings := append(FindingsForPerformanceReport(performance), FindingsForSecurityReport(security)...)
	AttachSymbols(root, findings)
	if len(findings) != 2 {
		t.Fatalf("findings = %+v", findings)
	}
	perf, rule := findings[0], findings[1]
	if perf.Source != FindingSourceAI || perf.Confidence != FindingConfidenceSuggested || perf.Category != FindingCategoryPerformance || perf.Severity != "low" ||
		perf.Location != (FindingLocation{Path: "store.go", StartLine: 4, EndLine: 6, Symbol: "Load"}) || perf.Evidence != "Recommendation: Query all ids at once." {
		t.Fatalf("performance issue = %+v", perf)
	}
	if rule.Source != FindingSourceSecurityRules || rule.Confidence != FindingConfidenceToolReported || rule.Severity != "high" || rule.Rule != "go.shell.dynamic-command (CWE-78)" || rule.Location.Symbol != "query" {
		t.Fatalf("security issue = %+v", rule)
	}
	// A report that did not complete lists nothing; a changed file gets no
	// function name, since the lines no longer point at the same code.
	if failed := FindingsForPerformanceReport(PerformanceFileReport{Status: "failed", Findings: performance.Findings}); len(failed) != 0 {
		t.Fatalf("failed report listed %+v", failed)
	}
	changed := FindingsForPerformanceReport(performance)
	changed[0].FileHash = "sha256:other"
	AttachSymbols(root, changed)
	if changed[0].Location.Symbol != "" {
		t.Fatalf("symbol for other content: %q", changed[0].Location.Symbol)
	}
}
