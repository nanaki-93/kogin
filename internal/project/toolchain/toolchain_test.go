package toolchain

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

func writeFiles(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var paths []string
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, name)
	}
	return root, paths
}

func TestEachSubprojectGetsItsOwnProfile(t *testing.T) {
	root, files := writeFiles(t, map[string]string{
		"go.mod":               "module example.test/m\n",
		"main.go":              "package main\n",
		"web/package.json":     `{"scripts":{"test":"vitest run","typecheck":"tsc -b","build":"vite build"}}`,
		"web/pnpm-lock.yaml":   "lockfileVersion: 9\n",
		"svc/Cargo.toml":       "[package]\nname = \"svc\"\n",
		"py/pyproject.toml":    "[tool.pytest.ini_options]\n",
		"java/pom.xml":         "<project/>",
		"java/mvnw":            "#!/bin/sh\n",
		"app/build.gradle.kts": "plugins {}\n",
		"tools/Makefile":       ".PHONY: test\nGO := go\ntest: build\n\t$(GO) test ./...\ncheck: test lint\nlint:\n\tgolangci-lint run\n",
		"docs/AGENTS.md":       "# Docs guide\n",
		"empty/package.json":   `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`,
		"dotnet/App.csproj":    "<Project/>",
		"site/justfile":        "test arg='x':\n  echo {{arg}}\n",
	})
	got := Detect(root, files, rules.NewResolver(root))
	want := []Profile{
		{Dir: "", Kind: "go", Source: "go.mod", Test: []string{"go", "test", "./..."}, Lint: []string{"go", "vet", "./..."}},
		{Dir: "app", Kind: "gradle", Source: "app/build.gradle.kts", Test: []string{"gradle", "test"}},
		{Dir: "dotnet", Kind: "dotnet", Source: "dotnet/App.csproj", Test: []string{"dotnet", "test"}},
		{Dir: "java", Kind: "maven", Source: "java/pom.xml", Test: []string{"./mvnw", "test"}},
		{Dir: "py", Kind: "python", Source: "py/pyproject.toml", Test: []string{"python", "-m", "pytest", "-q"}},
		{Dir: "site", Kind: "just", Source: "site/justfile", Test: []string{"just", "test"}},
		{Dir: "svc", Kind: "rust", Source: "svc/Cargo.toml", Test: []string{"cargo", "test"}, Lint: []string{"cargo", "check", "--all-targets"}},
		{Dir: "tools", Kind: "make", Source: "tools/Makefile", Test: []string{"make", "test"}},
		{Dir: "web", Kind: "node", Source: "web/package.json", Test: []string{"pnpm", "test"}, Lint: []string{"pnpm", "typecheck"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles:\n got %+v\nwant %+v", got, want)
	}
}

func TestAGENTSChecksOverrideAndTaskRunnersBeatDefaults(t *testing.T) {
	root, files := writeFiles(t, map[string]string{
		"go.mod":           "module example.test/m\n",
		"Makefile":         "test:\n\tgo test -v ./...\ncheck: test\n",
		"AGENTS.md":        "```kasuri\nchecks:\n  lint: go vet ./...\n```\n",
		"web/package.json": `{"scripts":{"test":"vitest run"}}`,
		"web/AGENTS.md":    "```kasuri\nchecks:\n  test: \"\"\n```\n",
	})
	got := Detect(root, files, rules.NewResolver(root))
	want := []Profile{{Dir: "", Kind: "make", Source: "AGENTS.md", Test: []string{"make", "test"}, Lint: []string{"go", "vet", "./..."}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles:\n got %+v\nwant %+v", got, want)
	}
}

func TestLintIsTheEcosystemsOwnNeverATaskRunnerTarget(t *testing.T) {
	root, files := writeFiles(t, map[string]string{
		"go.mod":             "module example.test/m\n",
		"justfile":           "test:\n    go test -race ./...\n\ncheck:\n    go vet ./...\n    go test -race ./...\n\nlint:\n    golangci-lint run\n",
		"web/package.json":   `{"scripts":{"test":"vitest run","check":"tsc -b && vitest run"}}`,
		"tools/Taskfile.yml": "version: '3'\ntasks:\n  lint:\n    cmds: [ruff check .]\n",
	})
	got := Detect(root, files, rules.NewResolver(root))
	want := []Profile{
		{Dir: "", Kind: "just", Source: "justfile", Test: []string{"just", "test"}, Lint: []string{"go", "vet", "./..."}},
		{Dir: "web", Kind: "node", Source: "web/package.json", Test: []string{"npm", "test"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles:\n got %+v\nwant %+v", got, want)
	}
}

func TestChangedFilesPickTheDeepestSubproject(t *testing.T) {
	profiles := []Profile{{Dir: ""}, {Dir: "web"}, {Dir: "web/e2e"}, {Dir: "svc"}}
	got := Affected(profiles, []string{"web/src/a.ts", "web/src/b.ts", "internal/app/x.go"})
	if len(got) != 2 || got[0].Dir != "" || got[1].Dir != "web" {
		t.Fatalf("affected = %+v", got)
	}
	if got := Affected(profiles[1:], []string{"README.md"}); len(got) != 0 {
		t.Fatalf("uncovered file affected %+v", got)
	}
}

func TestFailuresAreReadFromCommonTestRunners(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		want   []string
	}{
		"go": {"=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    a_test.go:9: boom\n    --- FAIL: TestA/sub (0.00s)\nFAIL\nFAIL\texample.test/m/a\t0.01s\nok  \texample.test/m/b\t0.02s\nFAIL\texample.test/m/c [build failed]\nFAIL\n",
			[]string{"example.test/m/a.TestA", "example.test/m/c"}},
		"pytest": {"=== short test summary info ===\nFAILED tests/test_x.py::test_one - assert 1 == 2\nERROR tests/test_y.py::test_two\n", []string{"tests/test_x.py::test_one", "tests/test_y.py::test_two"}},
		"cargo":  {"test util::tests::adds ... ok\ntest util::tests::subtracts ... FAILED\n", []string{"util::tests::subtracts"}},
		"vitest": {"\x1b[31m FAIL \x1b[39m src/lib/a.test.ts > math > adds\n × adds 3ms\n", []string{"adds", "src/lib/a.test.ts > math > adds"}},
		"gradle": {"CalcTest > adds() FAILED\n", []string{"CalcTest > adds()"}},
		"maven":  {"[ERROR] Failures: \n[ERROR]   CalcTest.adds:12 expected:<3> but was:<4>\n", []string{"CalcTest.adds"}},
		"none":   {"all good\nok  \texample.test/m\t0.1s\n", []string{}},
	} {
		if got := Failures(tc.output); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Failures = %q, want %q", name, got, tc.want)
		}
	}
	if added := NewFailures([]string{"a", "b"}, []string{"b", "c"}); !reflect.DeepEqual(added, []string{"c"}) {
		t.Fatalf("NewFailures = %v", added)
	}
}

func TestLintDiagnosticsIgnoreLineNumbers(t *testing.T) {
	before := Diagnostics("# example.test/m\n./a.go:10:2: unreachable code\nb.ts(3,1): not this format\n")
	after := Diagnostics("# example.test/m\n./a.go:14:2: unreachable code\nsrc/b.go:3: x declared and not used\n")
	if added := NewFailures(before, after); !reflect.DeepEqual(added, []string{"src/b.go: x declared and not used"}) {
		t.Fatalf("new diagnostics = %q (before %q)", added, before)
	}
}
