package toolchain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocationsReadEveryCommonDiagnosticFormat(t *testing.T) {
	output := strings.Join([]string{
		"# example.com/app/store",
		"store/db.go:42:9: printf: Sprintf format %d has arg of wrong type",
		"./cmd/main.go:7: unreachable code",
		"app/models.py:12:5: F401 'os' imported but unused",
		"src/api.ts(31,7): error TS2322: Type 'string' is not assignable to type 'number'.",
		"",
		"/work/web/src/App.tsx",
		"  14:3   error    'x' is assigned a value but never used  no-unused-vars",
		"  20:10  warning  Unexpected console statement            no-console",
		"",
		"error[E0308]: mismatched types",
		"  --> src/main.rs:5:18",
		"warning: unused variable: `y`",
		"   --> src/lib.rs:9:9",
		"\x1b[31mlib/user.rb:3:1: C: Style/Documentation: Missing top-level documentation comment.\x1b[0m",
		"ok  \texample.com/app\t0.12s",
		"2026-10-31T17:39:54+08:00 INFO started",
	}, "\n")
	var got []string
	for _, location := range Locations(output) {
		got = append(got, fmt.Sprintf("%s:%d %s", location.Path, location.Line, location.Message))
	}
	want := []string{
		"store/db.go:42 printf: Sprintf format %d has arg of wrong type",
		"cmd/main.go:7 unreachable code",
		"app/models.py:12 F401 'os' imported but unused",
		"src/api.ts:31 error TS2322: Type 'string' is not assignable to type 'number'.",
		"/work/web/src/App.tsx:14 'x' is assigned a value but never used no-unused-vars",
		"/work/web/src/App.tsx:20 Unexpected console statement no-console",
		"src/main.rs:5 mismatched types",
		"src/lib.rs:9 unused variable: `y`",
		"lib/user.rb:3 C: Style/Documentation: Missing top-level documentation comment.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("locations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestFailureLocationsFollowTheTestsName(t *testing.T) {
	goOutput := "--- FAIL: TestSave (0.00s)\n    store_test.go:31: got 2, want 3\nFAIL\nFAIL\texample.com/app/store\t0.01s\n"
	if got := FailureLocations(goOutput, "example.com/app/store.TestSave"); len(got) == 0 || got[0].Path != "store_test.go" || got[0].Line != 31 {
		t.Fatalf("go = %+v", got)
	}
	pytest := "FAILED tests/test_api.py::test_login - AssertionError\n"
	if got := FailureLocations(pytest, "tests/test_api.py::test_login"); len(got) == 0 || got[0].Path != "tests/test_api.py" {
		t.Fatalf("pytest = %+v", got)
	}
	jest := "  ● Cart › adds an item\n\n    expect(received).toBe(expected)\n\n      at Object.<anonymous> (src/cart.test.ts:12:5)\n"
	if got := FailureLocations(jest, "Cart › adds an item"); len(got) == 0 || got[0].Path != "src/cart.test.ts" || got[0].Line != 12 {
		t.Fatalf("jest = %+v", got)
	}
	if got := FailureLocations("nothing here", "TestMissing"); len(got) != 0 {
		t.Fatalf("missing = %+v", got)
	}
}

func TestSemgrepReadsItsJSONAndTheProjectsOwnConfig(t *testing.T) {
	output := `{"results":[
		{"check_id":"rules.sql-format","path":"./app/db.py","start":{"line":4},"end":{"line":5},
		 "extra":{"message":"SQL  built\nwith format","severity":"ERROR","metadata":{"cwe":["CWE-89: SQL Injection"]}}},
		{"check_id":"rules.todo","path":"web/a.ts","start":{"line":2},"end":{"line":2},
		 "extra":{"message":"TODO left","severity":"INFO","metadata":{"category":"maintainability"}}}
	],"errors":[]}`
	results, err := ParseSemgrep([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Path != "app/db.py" || results[0].EndLine != 5 || results[0].Severity != "high" || !results[0].Security || results[0].CWE != "CWE-89" || results[0].Message != "SQL built with format" {
		t.Fatalf("first = %+v", results)
	}
	if results[1].Security || results[1].Severity != "low" {
		t.Fatalf("second = %+v", results[1])
	}
	if _, err := ParseSemgrep([]byte(`{"results": [`)); err == nil {
		t.Fatal("truncated output was read")
	}
	root := t.TempDir()
	if SemgrepConfig(root) != "" {
		t.Fatal("a project without rules has a config")
	}
	if err := os.WriteFile(filepath.Join(root, ".semgrep.yaml"), []byte("rules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if config := SemgrepConfig(root); config != ".semgrep.yaml" || SemgrepCommand(config)[3] != ".semgrep.yaml" {
		t.Fatalf("config = %q", config)
	}
}
