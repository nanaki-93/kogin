package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePerformanceFindingsKeepsOnlyAnchoredSourceBasedOpportunities(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	valid := validPerformanceFindingsJSON()
	findings, warning, err := ParsePerformanceFindings(valid, "main.go", source)
	if err != nil || warning != "" || len(findings) != 1 || findings[0].ID == "" {
		t.Fatalf("findings = %+v, %q, %v", findings, warning, err)
	}
	partial := strings.Replace(valid, `"start_line":3,"end_line":3`, `"start_line":30,"end_line":30`, 1)
	if _, _, err := ParsePerformanceFindings(partial, "main.go", source); err == nil {
		t.Fatal("all-invalid nonempty output became empty success")
	}
}

func TestParsePerformanceFindingsAllowsEmptyAndWarnsForInvalidAnchors(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	findings, warning, err := ParsePerformanceFindings(`{"findings":[]}`, "main.go", source)
	if err != nil || warning != "" || len(findings) != 0 {
		t.Fatalf("empty findings = %+v, %q, %v", findings, warning, err)
	}
	validFinding := strings.TrimSuffix(strings.TrimPrefix(validPerformanceFindingsJSON(), `{"findings":[`), `]}`)
	invalidAnchor := strings.Replace(validFinding, `"start_line":3,"end_line":3`, `"start_line":30,"end_line":30`, 1)
	mixed := `{"findings":[` + validFinding + `,` + invalidAnchor + `]}`
	findings, warning, err = ParsePerformanceFindings(mixed, "main.go", source)
	if err != nil || len(findings) != 1 || warning != "Some model opportunities were omitted because their source anchors were invalid." {
		t.Fatalf("mixed findings = %+v, %q, %v", findings, warning, err)
	}
}

func TestParsePerformanceFindingsWarnsTruthfullyForMixedInvalidFieldsAndEnums(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	validFinding := strings.TrimSuffix(strings.TrimPrefix(validPerformanceFindingsJSON(), `{"findings":[`), `]}`)
	missingField := strings.Replace(validFinding, `"title":"Repeated work"`, `"title":""`, 1)
	invalidEnum := strings.Replace(validFinding, `"category":"cpu"`, `"category":"network"`, 1)
	output := `{"findings":[` + validFinding + `,` + missingField + `,` + invalidEnum + `]}`
	findings, warning, err := ParsePerformanceFindings(output, "main.go", source)
	if err != nil || len(findings) != 1 || warning != "Some model opportunities were omitted because required fields or category, potential impact, or confidence values were invalid." {
		t.Fatalf("mixed findings = %+v, %q, %v", findings, warning, err)
	}
}

func TestParsePerformanceFindingsWarnsTruthfullyForOneInvalidReason(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	validFinding := strings.TrimSuffix(strings.TrimPrefix(validPerformanceFindingsJSON(), `{"findings":[`), `]}`)
	for _, test := range []struct {
		name    string
		invalid string
		warning string
	}{
		{
			name:    "required fields",
			invalid: strings.Replace(validFinding, `"title":"Repeated work"`, `"title":""`, 1),
			warning: "Some model opportunities were omitted because required fields were invalid.",
		},
		{
			name:    "enum values",
			invalid: strings.Replace(validFinding, `"category":"cpu"`, `"category":"network"`, 1),
			warning: "Some model opportunities were omitted because category, potential impact, or confidence values were invalid.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := `{"findings":[` + validFinding + `,` + test.invalid + `]}`
			findings, warning, err := ParsePerformanceFindings(output, "main.go", source)
			if err != nil || len(findings) != 1 || warning != test.warning {
				t.Fatalf("mixed findings = %+v, %q, %v", findings, warning, err)
			}
		})
	}
}

func TestParsePerformanceFindingsValidatesFieldsEnumsAndLineAnchors(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "missing required field", output: strings.Replace(validPerformanceFindingsJSON(), `"title":"Repeated work"`, `"title":""`, 1)},
		{name: "invalid category", output: strings.Replace(validPerformanceFindingsJSON(), `"category":"cpu"`, `"category":"network"`, 1)},
		{name: "reversed source range", output: strings.Replace(validPerformanceFindingsJSON(), `"start_line":3,"end_line":3`, `"start_line":4,"end_line":3`, 1)},
		{name: "outside file range", output: strings.Replace(validPerformanceFindingsJSON(), `"start_line":3,"end_line":3`, `"start_line":10,"end_line":10`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ParsePerformanceFindings(test.output, "main.go", source); err == nil {
				t.Fatal("invalid finding was accepted")
			}
		})
	}
}

func TestParsePerformanceFindingsOmitsMalformedOptionalInsight(t *testing.T) {
	source := "package main\nfunc Run() {\n for range []int{1} {}\n}\n"
	for _, insight := range []string{`"not an object"`, `{"mechanism":"m"}`, `{"mechanism":"m","why_it_matters_here":"w","unknown":true}`, `null`} {
		output := strings.Replace(validPerformanceFindingsJSON(), `"end_line":3`, `"end_line":3,"engineering_insight":`+insight, 1)
		findings, warning, err := ParsePerformanceFindings(output, "main.go", source)
		if err != nil || warning != "" || len(findings) != 1 || findings[0].EngineeringInsight != nil {
			t.Fatalf("findings with malformed insight %q = %+v, %q, %v", insight, findings, warning, err)
		}
	}
}

func validPerformanceFindingsJSON() string {
	return `{"findings":[{"category":"cpu","potential_impact":"medium","confidence":"low","title":"Repeated work","observed_pattern":"The loop repeats local work.","workload_conditions":"Only if the input collection grows.","recommendation":"Measure then cache safely.","tradeoff":"Caching retains memory.","verification_plan":"Benchmark representative input sizes.","start_line":3,"end_line":3}]}`
}

func TestPerformanceCacheIsSeparateSourceFreeAndFreshnessBound(t *testing.T) {
	root := t.TempDir()
	report := PerformanceFileReport{SchemaVersion: "1", ProjectID: "p", ProjectRevision: "r", Path: "main.go", ContentHash: "one", Status: "completed", Findings: []PerformanceFinding{}, Model: "model", Profile: "analyze", Scope: "analyze", PromptVersion: PerformancePromptVersion, ContextPolicyVersion: "policy", GeneratedAt: time.Now().UTC()}
	if err := StorePerformanceFileReport(root, report); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(performanceCachePath(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "SOURCE:") || filepath.Base(performanceCachePath(root, "main.go")) == "main.go" {
		t.Fatalf("unsafe cache = %s", stored)
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPerformanceFileReport(root, "main.go", "two", policy)
	if err != nil || loaded == nil || loaded.Status != "stale" {
		t.Fatalf("stale load = %+v, %v", loaded, err)
	}
}

func TestPerformanceCacheIsStaleWhenContextPolicyChanges(t *testing.T) {
	root := t.TempDir()
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	report := PerformanceFileReport{SchemaVersion: "1", ProjectID: "p", ProjectRevision: "r", Path: "main.go", ContentHash: "one", Status: "completed", Findings: []PerformanceFinding{}, Model: "model", Profile: "analyze", Scope: "analyze", PromptVersion: PerformancePromptVersion, ContextPolicyVersion: policy.Version(), GeneratedAt: time.Now().UTC()}
	if err := StorePerformanceFileReport(root, report); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".koginignore"), []byte("main.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	changedPolicy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPerformanceFileReport(root, "main.go", "one", changedPolicy)
	if err != nil || loaded == nil || loaded.Status != "stale" {
		t.Fatalf("policy-stale load = %+v, %v", loaded, err)
	}
}

func TestPerformanceCacheIsStaleWhenSourceIsUnavailable(t *testing.T) {
	root := t.TempDir()
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	report := PerformanceFileReport{SchemaVersion: "1", ProjectID: "p", ProjectRevision: "r", Path: "main.go", ContentHash: "one", Status: "completed", Findings: []PerformanceFinding{}, Model: "model", Profile: "analyze", Scope: "analyze", PromptVersion: PerformancePromptVersion, ContextPolicyVersion: policy.Version(), GeneratedAt: time.Now().UTC()}
	if err := StorePerformanceFileReport(root, report); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPerformanceFileReport(root, "main.go", "one", policy)
	if err != nil || loaded == nil || loaded.Status != "stale" {
		t.Fatalf("unavailable-source load = %+v, %v", loaded, err)
	}
}

func TestPerformanceCacheRecoversMalformedFile(t *testing.T) {
	root := t.TempDir()
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := performanceCachePath(root, "main.go")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPerformanceFileReport(root, "main.go", "hash", policy)
	if err != nil || loaded != nil {
		t.Fatalf("malformed report = %+v, %v", loaded, err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatalf("malformed cache still exists: %v", err)
	}
	corrupt, err := filepath.Glob(cachePath + ".corrupt-*")
	if err != nil || len(corrupt) != 1 {
		t.Fatalf("corrupt recovery = %v, %v", corrupt, err)
	}
}

func TestPerformanceCacheRemainsCurrentAtSourceLimitWithUnchangedPolicy(t *testing.T) {
	root := t.TempDir()
	source := []byte(strings.Repeat("a", PerformanceMaxSourceBytes))
	if err := os.WriteFile(filepath.Join(root, "main.go"), source, 0644); err != nil {
		t.Fatal(err)
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	report := PerformanceFileReport{
		SchemaVersion: "1", ProjectID: "p", ProjectRevision: "r", Path: "main.go", ContentHash: contentHash(source), Status: "completed",
		Findings: []PerformanceFinding{}, Model: "model", Profile: "analyze", Scope: "analyze", PromptVersion: PerformancePromptVersion,
		ContextPolicyVersion: policy.Version(), GeneratedAt: time.Now().UTC(),
	}
	if err := StorePerformanceFileReport(root, report); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPerformanceFileReport(root, report.Path, report.ContentHash, policy)
	if err != nil || loaded == nil || loaded.Status != "completed" {
		t.Fatalf("current load = %+v, %v", loaded, err)
	}
}

func performanceCachePath(root, path string) string {
	resolved, err := resolvePerformanceCachePath(root, path)
	if err != nil {
		panic(err)
	}
	return resolved
}
