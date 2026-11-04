// Package reach finds code nothing calls: functions no path from a
// program's entry points reaches. It reads Go source today.
//
// Reachability goes by name, which over-approximates calls: a function is
// reached when its name appears in code that runs, whatever that name
// resolves to there. So a reported function is unused for sure (barring
// reflection by a computed name), while some unused ones are missed. Code
// runs from each main package's main, every init, and every package-level
// declaration (variable initializers, command tables, interface method
// sets). A module without a main function is a library: its exported
// functions and methods are its API and count as reached. Exported methods
// always count, since interfaces of other packages call them by name.
package reach

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strings"
)

// File is one source file of the project.
type File struct {
	Path    string
	Content []byte
	// Test marks a test file: what it calls does not make code used.
	Test bool
}

// Unused is a function nothing reachable calls.
type Unused struct {
	Path      string `json:"path"`
	Function  string `json:"function"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	// TestOnly marks a function only tests name.
	TestOnly bool `json:"test_only,omitempty"`
}

// function is one declared function and the names its body uses.
type function struct {
	path, display, name string
	start, end          int
	root, report        bool
	// module is the directory of the function's go.mod.
	module   string
	exported bool
	// mainPackage marks a function of a main package; such a package
	// without a main function has no entry point Find knows of.
	mainPackage bool
	uses        []string
}

// Find reports the unused functions of the Go files, ordered by path and line.
// Files that do not parse are left out, and so are their functions.
func Find(files []File) []Unused {
	modules := moduleDirs(files)
	reached := map[string]bool{}
	tested := map[string]bool{}
	var functions []function
	for _, file := range files {
		if path.Ext(file.Path) != ".go" || skipped(file.Path) {
			continue
		}
		parsed, fset, ok := parse(file)
		if !ok {
			continue
		}
		if file.Test {
			markNames(tested, parsed)
			continue
		}
		functions = append(functions, declared(file.Path, nearestModule(file.Path, modules), parsed, fset, reached)...)
	}
	rootLibraries(functions)
	rootPackagesWithoutMain(functions)
	return unreached(functions, reached, tested)
}

// rootLibraries counts the exported functions of a module without a main
// function as reached: such a module is a library, and they are its API.
func rootLibraries(functions []function) {
	programs := map[string]bool{}
	for _, fn := range functions {
		if fn.mainPackage && fn.display == "main" {
			programs[fn.module] = true
		}
	}
	for i, fn := range functions {
		if fn.exported && !programs[fn.module] {
			functions[i].root = true
		}
	}
}

// rootPackagesWithoutMain counts every function of a main package that has
// no main function as reached: its entry point is elsewhere (a build tag, a
// plugin), so nothing in it can be called unused for sure.
func rootPackagesWithoutMain(functions []function) {
	entry := map[string]bool{}
	for _, fn := range functions {
		if fn.mainPackage && fn.display == "main" {
			entry[path.Dir(fn.path)] = true
		}
	}
	for i, fn := range functions {
		if fn.mainPackage && !entry[path.Dir(fn.path)] {
			functions[i].root = true
		}
	}
}

// unreached spreads reachability from the roots and the names package-level
// code uses, until no new name is reached.
func unreached(functions []function, reached, tested map[string]bool) []Unused {
	done := make([]bool, len(functions))
	for changed := true; changed; {
		changed = false
		for i, fn := range functions {
			if done[i] || !fn.root && !reached[fn.name] {
				continue
			}
			done[i], changed = true, true
			reached[fn.name] = true
			for _, name := range fn.uses {
				reached[name] = true
			}
		}
	}
	var unused []Unused
	for i, fn := range functions {
		if !done[i] && fn.report {
			unused = append(unused, Unused{Path: fn.path, Function: fn.display, StartLine: fn.start, EndLine: fn.end, TestOnly: tested[fn.name]})
		}
	}
	slices.SortFunc(unused, func(a, b Unused) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return a.StartLine - b.StartLine
	})
	return unused
}

// declared lists a file's functions and marks the names its package-level
// declarations and directives use as reached.
func declared(file, module string, parsed *ast.File, fset *token.FileSet, reached map[string]bool) []function {
	generated := ast.IsGenerated(parsed)
	mainPackage := parsed.Name.Name == "main"
	var functions []function
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			for _, name := range names(decl) {
				reached[name] = true
			}
			continue
		}
		name := fn.Name.Name
		method := fn.Recv != nil
		exported := ast.IsExported(name)
		entry := name == "init" || mainPackage && name == "main" && !method
		functions = append(functions, function{
			path: file, display: displayName(fn), name: name,
			start: fset.Position(fn.Pos()).Line, end: fset.Position(fn.End()).Line,
			module: module, exported: exported,
			root:        generated || entry || method && exported || name == "_",
			report:      !generated,
			mainPackage: mainPackage,
			uses:        names(fn.Body),
		})
	}
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if name := directiveName(comment.Text); name != "" {
				reached[name] = true
			}
		}
	}
	return functions
}

// directives name functions the toolchain calls: cgo exports and linknames.
var directive = regexp.MustCompile(`^//(?:export|go:linkname)\s+(\w+)`)

func directiveName(comment string) string {
	if match := directive.FindStringSubmatch(comment); match != nil {
		return match[1]
	}
	return ""
}

// identifier words inside string literals count as uses, so code looked up
// by a fixed name (a method named in a string) stays reached.
var word = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// names are the identifiers a node uses, with the words of its string
// literals.
func names(node ast.Node) []string {
	if node == nil {
		return nil
	}
	seen := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		switch value := n.(type) {
		case *ast.Ident:
			seen[value.Name] = true
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				for _, w := range word.FindAllString(value.Value, -1) {
					seen[w] = true
				}
			}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func markNames(into map[string]bool, parsed *ast.File) {
	for _, name := range names(parsed) {
		into[name] = true
	}
}

// displayName is a function as people name it: "generateKey", or "Store.add"
// for a method.
func displayName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	for {
		switch t := receiver.(type) {
		case *ast.StarExpr:
			receiver = t.X
			continue
		case *ast.IndexExpr:
			receiver = t.X
			continue
		case *ast.IndexListExpr:
			receiver = t.X
			continue
		case *ast.Ident:
			return t.Name + "." + fn.Name.Name
		}
		return fn.Name.Name
	}
}

func parse(file File) (*ast.File, *token.FileSet, bool) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file.Path, file.Content, parser.ParseComments|parser.SkipObjectResolution)
	return parsed, fset, err == nil
}

// skipped are paths whose code is not the project's own: vendored and
// test data.
func skipped(file string) bool {
	for _, part := range strings.Split(path.Dir(file), "/") {
		if part == "vendor" || part == "testdata" {
			return true
		}
	}
	return false
}
