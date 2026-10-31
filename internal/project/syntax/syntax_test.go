package syntax

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type wantFunction struct {
	name       string
	complexity int
	start, end int
}

func assertFunctions(t *testing.T, file File, want []wantFunction) {
	t.Helper()
	if !file.Measured {
		t.Fatalf("functions were not measured: %s", file.Reason)
	}
	if len(file.Functions) != len(want) {
		t.Fatalf("functions = %+v, want %d", file.Functions, len(want))
	}
	for i, w := range want {
		got := file.Functions[i]
		if got.Name != w.name || got.Complexity != w.complexity || got.StartLine != w.start || got.EndLine != w.end {
			t.Errorf("function %d = %s complexity %d lines %d-%d, want %s complexity %d lines %d-%d",
				i, got.Name, got.Complexity, got.StartLine, got.EndLine, w.name, w.complexity, w.start, w.end)
		}
	}
}

func TestGoFunctionsMatchGocyclo(t *testing.T) {
	source := `package p

type T[K comparable] struct{}

func (t *T[K]) Add(a int) int {
	if a > 0 && a < 10 {
		return a
	}
	for i := range a {
		switch i {
		case 1:
		default:
		}
	}
	return 0
}

func F() { go func() {}() }
`
	file := Analyze("p.go", []byte(source))
	assertFunctions(t, file, []wantFunction{{"T.Add", 5, 5, 16}, {"F", 1, 18, 18}})
	if file.Functions[0].Kind != "method" || file.Grammar != "go" || file.SyntaxError != "" {
		t.Fatalf("file = %+v", file)
	}
}

func TestTypeScriptFunctionsIncludeMethodsAndBoundArrows(t *testing.T) {
	source := `export class Store {
  add(key: string, value: number): void {
    if (value < 0) { throw new Error("neg"); }
    for (const [k, v] of this.items) { if (k === key && v > 0) { return; } }
  }
  handle = (x: number) => x ?? 0;
}
export function total(xs: number[]): number {
  return xs.reduce((a, b) => a + b, 0) || (xs.length > 1 ? 1 : 0);
}
const pick = (x: number) => {
  switch (x) { case 1: return 1; case 2: return 2; default: return 0; }
};
`
	assertFunctions(t, Analyze("store.ts", []byte(source)), []wantFunction{
		{"Store.add", 5, 2, 5}, {"Store.handle", 2, 6, 6}, {"total", 3, 8, 10}, {"pick", 3, 11, 13},
	})
}

func TestPythonNestedFunctionsAreMeasuredOnTheirOwn(t *testing.T) {
	source := `class Store:
    def add(self, key, value):
        if value < 0 and key:
            raise ValueError("neg")
        def inner(x):
            return 1 if x else 2
        for k in self.items:
            if k == key:
                return
        match key:
            case "a":
                pass
            case _:
                pass
`
	assertFunctions(t, Analyze("store.py", []byte(source)), []wantFunction{
		{"Store.add", 6, 2, 14}, {"Store.add.inner", 2, 5, 6},
	})
}

func TestJavaRustAndKotlinFunctions(t *testing.T) {
	java := `public class A {
  public int add(int a, int b) {
    if (a > 0 && b > 0) { return a + b; }
    switch (a) { case 1: return 1; case 2: return 2; default: return 0; }
  }
  A() {}
}
`
	assertFunctions(t, Analyze("A.java", []byte(java)), []wantFunction{{"A.add", 5, 2, 5}, {"A.A", 1, 6, 6}})
	rust := `impl S {
    fn add(&self, a: i32) -> i32 { if a > 0 { a } else { match a { 0 => 1, 1 => 2, _ => 3 } } }
}
`
	assertFunctions(t, Analyze("lib.rs", []byte(rust)), []wantFunction{{"add", 4, 2, 2}})
	kotlin := `fun pick(x: Int): Int = when (x) {
    1 -> 1
    2 -> 2
    else -> 0
}
`
	assertFunctions(t, Analyze("Pick.kt", []byte(kotlin)), []wantFunction{{"pick", 3, 1, 5}})
}

func TestSyntaxErrorsNameTheirLine(t *testing.T) {
	broken := Analyze("broken.ts", []byte("export function broken(x: number {\n  return x +;\n}\n"))
	if !strings.HasPrefix(broken.SyntaxError, "line 1:") {
		t.Fatalf("syntax error = %q", broken.SyntaxError)
	}
	if clean := Analyze("ok.ts", []byte("export const a = 1;\n")); clean.SyntaxError != "" {
		t.Fatalf("clean file reported %q", clean.SyntaxError)
	}
	goBroken := Analyze("p.go", []byte("package p\n\nfunc F( {\n"))
	if !strings.HasPrefix(goBroken.SyntaxError, "line 3:") {
		t.Fatalf("go syntax error = %q", goBroken.SyntaxError)
	}
	yaml := Analyze("config.yaml", []byte("a: [1, 2\nb: c\n"))
	if yaml.SyntaxError == "" || yaml.Measured {
		t.Fatalf("yaml = %+v", yaml)
	}
}

func TestMovedFunctionsKeepTheirHash(t *testing.T) {
	original := Analyze("a.py", []byte("def f(x):\n    if x:\n        return 1\n    return 2\n"))
	moved := Analyze("b.py", []byte("import os\n\n\ndef f(x):\n    if x:\n\n        return 1\n    return 2\n"))
	edited := Analyze("c.py", []byte("def f(x):\n    if x:\n        return 3\n    return 2\n"))
	if original.Functions[0].Hash != moved.Functions[0].Hash {
		t.Fatal("a function moved with blank lines added changed its hash")
	}
	if original.Functions[0].Hash == edited.Functions[0].Hash {
		t.Fatal("an edited function kept its hash")
	}
}

func TestFilesWithoutAGrammarOrFunctionsAreNotMeasured(t *testing.T) {
	if file := Analyze("notes.unknownext", []byte("x")); file.Measured || file.Reason == "" || file.Grammar != "" {
		t.Fatalf("unknown = %+v", file)
	}
	if file := Analyze("data.json", []byte(`{"a": 1}`)); file.Measured || file.SyntaxError != "" || file.Grammar != "json" {
		t.Fatalf("json = %+v", file)
	}
}

func TestExtraDefinitionQueriesCompile(t *testing.T) {
	for name, extra := range extraDefinitions {
		entry := grammars.DetectLanguageByName(name)
		if entry == nil {
			t.Fatalf("no %s grammar", name)
		}
		if _, err := gotreesitter.NewOutliner(entry.Language(), grammars.ResolveTagsQuery(*entry)+extra); err != nil {
			t.Errorf("%s extra definitions: %v", name, err)
		}
	}
	js := Analyze("a.js", []byte("class A { run = () => 1; }\nfunction* gen() { yield 1; }\nconst f = function () { return a && b; };\n"))
	assertFunctions(t, js, []wantFunction{{"A.run", 1, 1, 1}, {"gen", 1, 2, 2}, {"f", 2, 3, 3}})
	cpp := Analyze("a.cpp", []byte("int Foo::bar(int x) {\n  return x > 0 ? 1 : 2;\n}\n"))
	assertFunctions(t, cpp, []wantFunction{{"Foo::bar", 2, 1, 3}})
}

func TestOutlinesListDeclarationsWithTheirFirstLine(t *testing.T) {
	ts := Outline("store.ts", []byte("export interface Item { id: string }\nexport class Store {\n  add(item: Item): void {\n    this.items.push(item);\n  }\n}\nexport function total(xs: number[]): number {\n  return 0;\n}\n"))
	want := []Symbol{
		{Kind: "interface", Name: "Item", Line: 1, Signature: "interface Item"},
		{Kind: "class", Name: "Store", Line: 2, Signature: "class Store"},
		{Kind: "method", Name: "Store.add", Line: 3, Signature: "add(item: Item): void"},
		{Kind: "function", Name: "total", Line: 7, Signature: "function total(xs: number[]): number"},
	}
	if len(ts) != len(want) {
		t.Fatalf("ts outline = %+v", ts)
	}
	for i := range want {
		if ts[i] != want[i] {
			t.Errorf("symbol %d = %+v, want %+v", i, ts[i], want[i])
		}
	}
	goSymbols := Outline("p.go", []byte("package p\n\n// Limit caps runs.\nconst Limit = 3\n\ntype Runner struct{ n int }\n\nfunc (r *Runner) Run(ctx context.Context) error {\n\treturn nil\n}\n"))
	if len(goSymbols) != 3 || goSymbols[0].Signature != "const Limit = 3" || goSymbols[1].Signature != "type Runner struct" || goSymbols[2].Name != "Runner.Run" || goSymbols[2].Signature != "func (r *Runner) Run(ctx context.Context) error" {
		t.Fatalf("go outline = %+v", goSymbols)
	}
	if none := Outline("notes.unknownext", []byte("x")); none != nil {
		t.Fatalf("unknown = %+v", none)
	}
}

func TestEnclosingNamesTheInnermostFunction(t *testing.T) {
	functions := []Function{{Name: "Store", StartLine: 1, EndLine: 20}, {Name: "Store.add", StartLine: 5, EndLine: 9}, {Name: "other", StartLine: 22, EndLine: 30}}
	for line, want := range map[int]string{3: "Store", 7: "Store.add", 21: "", 22: "other"} {
		if got := Enclosing(functions, line); got != want {
			t.Errorf("line %d: %q, want %q", line, got, want)
		}
	}
}
