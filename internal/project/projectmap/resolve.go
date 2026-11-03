package projectmap

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

// resolver turns a file's import lines into the project's modules they name.
// Imports of anything outside the project are left out.
type resolver struct {
	known   map[string]bool
	modules map[string]bool
	// goModules maps a Go module path to its directory.
	goModules map[string]string
	// aliases are tsconfig/jsconfig path aliases: a prefix and its directory.
	aliases []alias
	// pythonRoots are where absolute Python imports start: the root, the
	// Python projects' directories, and their src/ directories.
	pythonRoots []string
	// bySuffix maps the lower-case end of a file path (with and without its
	// extension, at least the file name) to the file, "" when ambiguous.
	bySuffix map[string]string
	// dirBySuffix does the same for module directories.
	dirBySuffix map[string]string
}

type alias struct{ prefix, dir string }

var (
	jsSpecifier = regexp.MustCompile(`(?:from|import|require\()\s*\(?\s*['"]([^'"]+)['"]`)
	pyFrom      = regexp.MustCompile(`^from\s+(\.*)([\w.]*)\s+import\s+(.+)$`)
	pyImport    = regexp.MustCompile(`^import\s+(.+)$`)
	rustUse     = regexp.MustCompile(`^(?:pub\s+)?use\s+(crate|super|self)::([\w:]+)`)
	jsFamily    = map[string]bool{"TypeScript": true, "JavaScript": true, "Vue": true, "Svelte": true, "Astro": true}
	jsSuffixes  = []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", "/index.ts", "/index.tsx", "/index.js", "/index.jsx"}
)

func newResolver(root string, known, modules map[string]bool, manifests []Manifest) *resolver {
	r := &resolver{known: known, modules: modules, goModules: map[string]string{}, pythonRoots: []string{".", "src"}, bySuffix: map[string]string{}, dirBySuffix: map[string]string{}}
	for _, manifest := range manifests {
		dir := path.Dir(manifest.Path)
		switch manifest.Kind {
		case "go":
			if manifest.Name != "" {
				r.goModules[manifest.Name] = dir
			}
		case "node":
			r.aliases = append(r.aliases, readAliases(root, dir)...)
		case "python":
			if dir != "." {
				r.pythonRoots = append(r.pythonRoots, dir, path.Join(dir, "src"))
			}
		}
	}
	if len(r.aliases) == 0 {
		r.aliases = readAliases(root, ".")
	}
	for file := range known {
		addSuffixes(r.bySuffix, strings.ToLower(file), file)
		addSuffixes(r.bySuffix, strings.ToLower(strings.TrimSuffix(file, path.Ext(file))), file)
	}
	for module := range modules {
		addSuffixes(r.dirBySuffix, strings.ToLower(module), module)
	}
	return r
}

// addSuffixes indexes every tail of key that starts at a path segment as
// value; a tail two values share is ambiguous ("").
func addSuffixes(index map[string]string, key, value string) {
	for tail := key; ; {
		if previous, seen := index[tail]; seen && previous != value {
			index[tail] = ""
		} else if !seen {
			index[tail] = value
		}
		slash := strings.IndexByte(tail, '/')
		if slash < 0 {
			return
		}
		tail = tail[slash+1:]
	}
}

// edges resolves the imports of every file and counts them per module pair.
func (r *resolver) edges(code []symbols.IndexFile) []Edge {
	counts := map[[2]string]int{}
	for _, file := range code {
		from := ModuleOf(file.Path)
		for _, raw := range file.Imports {
			for _, to := range r.resolve(file, raw) {
				if to != from && r.modules[to] {
					counts[[2]string{from, to}]++
				}
			}
		}
	}
	edges := make([]Edge, 0, len(counts))
	for pair, count := range counts {
		edges = append(edges, Edge{From: pair[0], To: pair[1], Imports: count})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

// resolve returns the modules one import line names.
func (r *resolver) resolve(file symbols.IndexFile, raw string) []string {
	switch {
	case file.Language == "Go":
		return r.goImport(raw)
	case jsFamily[file.Language]:
		return r.jsImport(file.Path, raw)
	case file.Language == "Python":
		return r.pythonImport(file.Path, raw)
	case file.Language == "Rust":
		return r.rustImport(file.Path, raw)
	default:
		return r.genericImport(file.Path, raw)
	}
}

func (r *resolver) goImport(importPath string) []string {
	best := ""
	for module := range r.goModules {
		if (importPath == module || strings.HasPrefix(importPath, module+"/")) && len(module) > len(best) {
			best = module
		}
	}
	if best == "" {
		return nil
	}
	return []string{path.Join(r.goModules[best], strings.TrimPrefix(importPath, best))}
}

func (r *resolver) jsImport(file, raw string) []string {
	match := jsSpecifier.FindStringSubmatch(raw)
	if match == nil {
		return nil
	}
	spec := match[1]
	base := ""
	switch {
	case strings.HasPrefix(spec, "."):
		base = path.Join(path.Dir(file), spec)
	default:
		for _, a := range r.aliases {
			if strings.HasPrefix(spec, a.prefix) {
				base = path.Join(a.dir, strings.TrimPrefix(spec, a.prefix))
				break
			}
		}
	}
	if base == "" {
		return nil
	}
	for _, suffix := range jsSuffixes {
		if candidate := base + suffix; r.known[candidate] {
			return []string{ModuleOf(candidate)}
		}
	}
	return nil
}

func (r *resolver) pythonImport(file, raw string) []string {
	line := strings.TrimSpace(raw)
	if match := pyFrom.FindStringSubmatch(line); match != nil {
		base := strings.ReplaceAll(match[2], ".", "/")
		if dots := len(match[1]); dots > 0 {
			dir := path.Dir(file)
			for i := 1; i < dots; i++ {
				dir = path.Dir(dir)
			}
			base = path.Join(dir, base)
			return r.pythonModules(base, strings.Split(match[3], ","), true)
		}
		return r.pythonModules(base, strings.Split(match[3], ","), false)
	}
	if match := pyImport.FindStringSubmatch(line); match != nil {
		var modules []string
		for _, name := range strings.Split(match[1], ",") {
			fields := strings.Fields(name)
			if len(fields) > 0 {
				modules = append(modules, r.pythonModules(strings.ReplaceAll(fields[0], ".", "/"), nil, false)...)
			}
		}
		return modules
	}
	return nil
}

// pythonModules finds base (a package or module) or its imported names as
// files of the project, from the root or a src/ directory unless relative.
func (r *resolver) pythonModules(base string, names []string, relative bool) []string {
	roots := []string{""}
	if !relative {
		roots = r.pythonRoots
	}
	var found []string
	for _, root := range roots {
		dir := path.Join(root, base)
		for _, name := range names {
			if fields := strings.Fields(strings.Trim(name, "() ")); len(fields) > 0 {
				if module := r.pythonFile(path.Join(dir, fields[0])); module != "" {
					found = append(found, module)
				}
			}
		}
		if module := r.pythonFile(dir); module != "" {
			found = append(found, module)
		}
		if len(found) > 0 {
			return found
		}
	}
	return nil
}

func (r *resolver) pythonFile(base string) string {
	for _, candidate := range []string{base + ".py", base + "/__init__.py"} {
		if r.known[candidate] {
			return ModuleOf(candidate)
		}
	}
	return ""
}

func (r *resolver) rustImport(file, raw string) []string {
	match := rustUse.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return nil
	}
	dir := path.Dir(file)
	if match[1] == "crate" {
		dir = rustCrateSource(file)
	}
	segments := strings.Split(strings.Trim(match[2], ":"), "::")
	for n := len(segments); n > 0; n-- {
		base := path.Join(dir, path.Join(segments[:n]...))
		for _, candidate := range []string{base + ".rs", base + "/mod.rs"} {
			if r.known[candidate] {
				return []string{ModuleOf(candidate)}
			}
		}
	}
	return nil
}

// rustCrateSource is the src directory of the crate a file is in.
func rustCrateSource(file string) string {
	if index := strings.LastIndex(file, "/src/"); index >= 0 {
		return file[:index+len("/src")]
	}
	if strings.HasPrefix(file, "src/") {
		return "src"
	}
	return path.Dir(file)
}

// genericImport finds the file or package an import of another language
// names by the end of its path: com.acme.billing.Invoice is
// com/acme/billing/Invoice.java wherever the sources start.
func (r *resolver) genericImport(file, raw string) []string {
	spec := strings.TrimSuffix(symbols.NormalizeGenericImportPath(raw), "/*")
	if spec == "" {
		return nil
	}
	if relative := path.Join(path.Dir(file), spec); r.known[relative] {
		return []string{ModuleOf(relative)}
	}
	key := strings.ToLower(spec)
	if found := r.bySuffix[key]; found != "" {
		return []string{ModuleOf(found)}
	}
	if found := r.dirBySuffix[key]; found != "" {
		return []string{found}
	}
	return nil
}

// readAliases reads the path aliases of the tsconfig.json or jsconfig.json in
// dir: "@/*": ["./src/*"] makes "@/lib/x" the file src/lib/x.
func readAliases(root, dir string) []alias {
	for _, name := range []string{"tsconfig.json", "jsconfig.json", "tsconfig.app.json"} {
		data, err := readBounded(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil {
			continue
		}
		var config struct {
			CompilerOptions struct {
				BaseURL string              `json:"baseUrl"`
				Paths   map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}
		if json.Unmarshal(stripJSONComments(data), &config) != nil {
			continue
		}
		var aliases []alias
		base := path.Join(dir, config.CompilerOptions.BaseURL)
		for pattern, targets := range config.CompilerOptions.Paths {
			if len(targets) == 0 || !strings.HasSuffix(pattern, "*") || !strings.HasSuffix(targets[0], "*") {
				continue
			}
			aliases = append(aliases, alias{prefix: strings.TrimSuffix(pattern, "*"), dir: path.Join(base, strings.TrimSuffix(targets[0], "*")) + "/"})
		}
		sort.Slice(aliases, func(i, j int) bool { return len(aliases[i].prefix) > len(aliases[j].prefix) })
		if len(aliases) > 0 {
			return aliases
		}
	}
	return nil
}

var lineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

// stripJSONComments removes the whole-line comments tsconfig files allow.
func stripJSONComments(data []byte) []byte {
	return lineComment.ReplaceAll(data, nil)
}

func readBounded(file string) ([]byte, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	buffer := make([]byte, min(info.Size(), maxReadBytes))
	n, err := handle.Read(buffer)
	if err != nil && n == 0 {
		return nil, err
	}
	return buffer[:n], nil
}
