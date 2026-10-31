package syntax

import (
	"errors"
	"testing"
)

func TestQueriesTellFixedLiteralsFromBuiltStrings(t *testing.T) {
	for _, test := range []struct {
		path, source, query string
		literal             []bool
	}{
		{"a.py", "a = \"x\"\nb = f\"x {y}\"\nc = 'x' + y\n", `(assignment right: (_) @value)`, []bool{true, false, false}},
		{"a.js", "a = 'x'; b = `x`; c = `x ${y}`;", `(assignment_expression right: (_) @value)`, []bool{true, true, false}},
		{"a.rb", "a = \"x\"\nb = \"x #{y}\"\n", `(assignment right: (_) @value)`, []bool{true, false}},
		{"a.php", "<?php $a = 'x'; $b = \"x\"; $c = \"x $y\";", `(assignment_expression right: (_) @value)`, []bool{true, true, false}},
		{"a.cs", "class A { void F() { a = \"x\"; b = @\"x\"; c = $\"x {y}\"; } }", `(assignment_expression right: (_) @value)`, []bool{true, true, false}},
		{"a.kt", "fun f() { a = \"x\"\n b = \"x $y\" }", `(assignment (_) (_) @value)`, []bool{true, false}},
		{"a.go", "package a\nfunc f() { a = \"x\"; b = `x`; c = x }", `(assignment_statement right: (expression_list (_) @value))`, []bool{true, true, false}},
	} {
		results, err := Queries(test.path, []byte(test.source), []string{test.query})
		if err != nil {
			t.Fatalf("%s: %v", test.path, err)
		}
		if len(results) != 1 || len(results[0]) != len(test.literal) {
			t.Fatalf("%s matches = %+v", test.path, results)
		}
		for i, match := range results[0] {
			value := match.Captures["value"]
			if value.Literal != test.literal[i] || value.StartLine < 1 || value.Kind == "" {
				t.Fatalf("%s value %d = %+v, want literal %v", test.path, i, value, test.literal[i])
			}
		}
	}
	if _, err := Queries("data.bin", []byte("x"), []string{"(x)"}); !errors.Is(err, ErrNoGrammar) {
		t.Fatalf("a file without a grammar: %v", err)
	}
	if _, err := Queries("a.py", []byte("x"), []string{"(no_such_node)"}); err == nil {
		t.Fatal("a query naming an unknown node compiled")
	}
}
