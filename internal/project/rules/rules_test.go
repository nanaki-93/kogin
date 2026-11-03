package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeGuide(t *testing.T, root, dir, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(dir))
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "AGENTS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const rootGuide = "# Guide\n\nSome prose.\n\n```kogin\nbudgets:\n  file_lines: 800\n  function_lines: 80\nimports:\n  - from: internal/app\n    never: [internal/api]\ntest_files:\n  - \"e2e/**/*.ts\"\n```\n"

func TestTheNearestGuideWinsForEachBudget(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, "", rootGuide)
	writeGuide(t, root, "web", "```kogin\nbudgets:\n  file_lines: 400\nimports:\n  - from: src/api\n    never: [src/db]\n```\n")
	resolver := NewResolver(root)

	rootRules := resolver.For("internal/app/service.go")
	want := Budgets{FileLines: 800, FunctionLines: 80, FunctionComplexity: DefaultFunctionComplexity}
	if rootRules.Budgets != want || !reflect.DeepEqual(rootRules.Sources, []string{"AGENTS.md"}) {
		t.Fatalf("root rules = %+v", rootRules)
	}
	webRules := resolver.For("web/src/api/client.ts")
	want.FileLines = 400
	if webRules.Budgets != want {
		t.Fatalf("web budgets = %+v, want %+v", webRules.Budgets, want)
	}
	wantImports := []ImportRule{{From: "internal/app", Never: []string{"internal/api"}}, {From: "web/src/api", Never: []string{"src/db"}}}
	if !reflect.DeepEqual(webRules.Imports, wantImports) || !reflect.DeepEqual(webRules.Sources, []string{"AGENTS.md", "web/AGENTS.md"}) {
		t.Fatalf("web rules = %+v", webRules)
	}
	if problems := resolver.Problems(); len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
}

func TestPlainLanguageGuidesStillDeclareRules(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, "", "- Max file lines: 800\n- max function complexity 12\n- `internal/app` must never import `internal/api` or `internal/storage`.\n")
	rules := NewResolver(root).For("main.go")
	if rules.Budgets != (Budgets{FileLines: 800, FunctionComplexity: 12}) {
		t.Fatalf("budgets = %+v", rules.Budgets)
	}
	if !reflect.DeepEqual(rules.Imports, []ImportRule{{From: "internal/app", Never: []string{"internal/api", "internal/storage"}}}) {
		t.Fatalf("imports = %+v", rules.Imports)
	}
}

func TestABlockReplacesThePlainLanguageLinesOfItsGuide(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, "", "Max file lines: 300\n\n```kogin\nbudgets:\n  function_complexity: 0\n```\n")
	rules := NewResolver(root).For("a.py")
	if rules.Budgets != (Budgets{}) {
		t.Fatalf("budgets = %+v, want none", rules.Budgets)
	}
}

func TestBlocksUnderEarlierNamesStillDeclareRules(t *testing.T) {
	for _, info := range []string{"kasuri", "mini-orca"} {
		root := t.TempDir()
		writeGuide(t, root, "", "```"+info+"\nbudgets:\n  file_lines: 500\n```\n")
		rules := NewResolver(root).For("main.go")
		if rules.Budgets.FileLines != 500 {
			t.Fatalf("a %s block: budgets = %+v", info, rules.Budgets)
		}
	}
}

func TestBrokenBlocksAreReportedAndDeclareNothing(t *testing.T) {
	for name, guide := range map[string]string{
		"unknown field": "```kogin\nbudget:\n  file_lines: 10\n```\n",
		"not closed":    "```kogin\nbudgets:\n  file_lines: 10\n",
		"no never":      "```kogin\nimports:\n  - from: a\n```\n",
		"negative":      "```kogin\nbudgets:\n  file_lines: -1\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeGuide(t, root, "", guide)
			resolver := NewResolver(root)
			if rules := resolver.For("a.go"); rules.Budgets != (Budgets{FunctionComplexity: DefaultFunctionComplexity}) || len(rules.Imports) != 0 {
				t.Fatalf("rules = %+v", rules)
			}
			problems := resolver.Problems()
			if len(problems) != 1 || problems[0].Path != "AGENTS.md" || !strings.Contains(problems[0].Message, "block") && !strings.Contains(problems[0].Message, "never") {
				t.Fatalf("problems = %+v", problems)
			}
		})
	}
}

func TestTestFilesFollowConventionsAndDeclaredPatterns(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, "", rootGuide)
	writeGuide(t, root, "svc", "```kogin\ntest_files: [\"*.it.ts\"]\n```\n")
	resolver := NewResolver(root)
	for file, want := range map[string]bool{
		"internal/app/service_test.go":    true,
		"web/src/lib/util.test.ts":        true,
		"pkg/tests/helpers.py":            true,
		"src/test/java/a/FooTest.java":    true,
		"app/test_models.py":              true,
		"e2e/review.ts":                   true,
		"svc/deep/orders.it.ts":           true,
		"other/orders.it.ts":              false,
		"internal/app/service.go":         false,
		"web/src/lib/testing-library.tsx": false,
		"test":                            false,
	} {
		if got := resolver.IsTestFile(file); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", file, got, want)
		}
	}
	patterns := resolver.TestPatterns("AGENTS.md", "svc/AGENTS.md", "missing/AGENTS.md")
	if got := patterns[len(patterns)-2:]; !reflect.DeepEqual(got, []string{"e2e/**/*.ts", "svc/**/*.it.ts"}) {
		t.Fatalf("declared patterns = %v", got)
	}
}

func TestGlobs(t *testing.T) {
	for _, tc := range []struct {
		glob, file string
		want       bool
	}{
		{"*_test.go", "a/b/c_test.go", true},
		{"*_test.go", "c_test.go", true},
		{"src/*.ts", "src/a.ts", true},
		{"src/*.ts", "src/x/a.ts", false},
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/x/y/a.ts", true},
		{"**/test/**", "test/a.go", true},
		{"a?.py", "ab.py", true},
		{"*Test.java", "FooTEST.java", true},
	} {
		re, ok := compileGlob(tc.glob)
		if !ok || re.MatchString(tc.file) != tc.want {
			t.Errorf("%q vs %q = %v, want %v", tc.glob, tc.file, ok && re.MatchString(tc.file), tc.want)
		}
	}
	if _, ok := compileGlob("/abs/*.go"); ok {
		t.Fatal("absolute glob accepted")
	}
}

func TestGuidesDeclareCheckCommandsForTheirSubproject(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, "", "```kogin\nchecks:\n  test: go test ./...\n  lint: \"\"\n```\n")
	writeGuide(t, root, "web", "```kogin\nchecks:\n  test: npx vitest run --reporter 'dot'\n```\n")
	resolver := NewResolver(root)
	rootChecks := resolver.ChecksIn("")
	if !reflect.DeepEqual(rootChecks.Test, []string{"go", "test", "./..."}) || !rootChecks.LintDeclared || rootChecks.Lint != nil {
		t.Fatalf("root checks = %+v", rootChecks)
	}
	web := resolver.ChecksIn("web")
	if !reflect.DeepEqual(web.Test, []string{"npx", "vitest", "run", "--reporter", "dot"}) || web.LintDeclared {
		t.Fatalf("web checks = %+v", web)
	}
	if none := resolver.ChecksIn("docs"); none.TestDeclared || none.LintDeclared {
		t.Fatalf("docs checks = %+v", none)
	}
	for _, command := range []string{"go test ./... && rm -rf /", "echo $HOME", "make 'test"} {
		if args := SplitCommand(command); args != nil {
			t.Errorf("SplitCommand(%q) = %q, want refusal", command, args)
		}
	}
	writeGuide(t, root, "bad", "```kogin\nchecks:\n  test: go test ./... | tee log\n```\n")
	if bad := resolver.ChecksIn("bad"); bad.TestDeclared || len(resolver.Problems()) != 1 {
		t.Fatalf("shell syntax accepted: %+v, %+v", bad, resolver.Problems())
	}
}

func TestCompanionTestsFollowEachLanguageAndPreferExistingFiles(t *testing.T) {
	root := t.TempDir()
	for _, existing := range []string{"web/src/lib/util.spec.ts", "tests/test_models.py"} {
		full := filepath.Join(root, filepath.FromSlash(existing))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for file, want := range map[string]string{
		"internal/app/service.go":       "internal/app/service_test.go",
		"web/src/lib/util.ts":           "web/src/lib/util.spec.ts",
		"web/src/App.tsx":               "web/src/App.test.tsx",
		"app/models.py":                 "tests/test_models.py",
		"app/views.py":                  "app/test_views.py",
		"core/src/main/java/a/Foo.java": "core/src/test/java/a/FooTest.java",
		"lib/parser.rb":                 "spec/parser_spec.rb",
		"src/lib.rs":                    "",
		"internal/app/service_test.go":  "",
		"README.md":                     "",
	} {
		if got := CompanionTest(root, file); got != want {
			t.Errorf("CompanionTest(%q) = %q, want %q", file, got, want)
		}
	}
}
