package app

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/app/changes"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
	"github.com/nanaki-93/kasuri/v2/internal/project/syntax"
)

const (
	maxNeighborOutlineBytes = 8 * 1024
	maxNeighborFileBytes    = 64 * 1024
	maxSiblingsPerDir       = 12
)

// neighborOutlines gives the writing agents the declarations of the files
// the task's files import and of the files next to them, in any language the
// parser knows, so they reuse what exists instead of redefining it.
func neighborOutlines(root string, session *ChangeSession) string {
	policy, err := project.NewContextPolicy(root)
	if err != nil {
		return ""
	}
	var out strings.Builder
	for _, file := range neighborFiles(root, session, policy) {
		data, err := readNeighbor(root, file)
		if err != nil {
			continue
		}
		symbols := syntax.Outline(file, data)
		if len(symbols) == 0 {
			continue
		}
		var block strings.Builder
		block.WriteString("// " + file + "\n")
		for _, symbol := range symbols {
			block.WriteString("  " + symbol.Signature + "\n")
		}
		if out.Len()+block.Len() > maxNeighborOutlineBytes {
			break
		}
		out.WriteString(block.String())
	}
	if out.Len() == 0 {
		return ""
	}
	return "\nOutlines of neighboring files (read-only context; reuse these declarations instead of redefining them):\n" + out.String()
}

func readNeighbor(root, file string) ([]byte, error) {
	full, err := project.ResolveWritePath(root, file)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxNeighborFileBytes {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(full)
}

// neighborFiles lists, without repeats, the local files the task's files
// import, then the files in their directories with the same kind of source.
// Files the task holds and files the context policy leaves out are skipped.
func neighborFiles(root string, session *ChangeSession, policy *project.ContextPolicy) []string {
	held := map[string]bool{}
	for _, target := range session.Targets {
		held[target.Path] = true
	}
	sources := neighborSources(session.Targets)
	var files []string
	seen := map[string]bool{}
	add := func(file string) {
		file = path.Clean(filepath.ToSlash(file))
		if !seen[file] && !held[file] && !worktreeMetadata(file) && policy.Decide(file).Include {
			seen[file] = true
			files = append(files, file)
		}
	}
	for _, file := range localImports(root, sources) {
		add(file)
	}
	for _, target := range sources {
		for _, file := range siblingSources(root, target.Path) {
			add(file)
		}
	}
	return files
}

// neighborSources are the targets whose surroundings matter: the
// implementation files, or the tests when the task holds only tests.
func neighborSources(targets []ChangeTarget) []ChangeTarget {
	var sources []ChangeTarget
	for _, target := range targets {
		if !isTestTarget(target) {
			sources = append(sources, target)
		}
	}
	if len(sources) == 0 {
		return targets
	}
	return sources
}

// sourceFamily groups extensions that import each other.
func sourceFamily(file string) string {
	switch ext := strings.ToLower(path.Ext(file)); ext {
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return "js"
	default:
		return ext
	}
}

func siblingSources(root, file string) []string {
	dir := path.Dir(filepath.ToSlash(file))
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil
	}
	family := sourceFamily(file)
	var siblings []string
	for _, entry := range entries {
		name := entry.Name()
		candidate := path.Join(dir, name)
		if entry.IsDir() || sourceFamily(name) != family || family == "" || rules.IsConventionalTestFile(candidate) {
			continue
		}
		siblings = append(siblings, candidate)
		if len(siblings) == maxSiblingsPerDir {
			break
		}
	}
	sort.Strings(siblings)
	return siblings
}

var (
	jsImportPattern       = regexp.MustCompile(`(?:from\s+|import\s+|require\(\s*)['"](\.{1,2}/[^'"]+)['"]`)
	pythonRelativePattern = regexp.MustCompile(`(?m)^\s*from\s+(\.+)([\w.]*)\s+import\s+([\w, ]+)`)
	pythonAbsolutePattern = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
)

// localImports resolves the imports of the given files to project files: Go
// packages of the module, relative JavaScript and TypeScript imports, and
// Python modules of the project.
func localImports(root string, sources []ChangeTarget) []string {
	var files []string
	var goSources []string
	for _, source := range sources {
		switch sourceFamily(source.Path) {
		case ".go":
			goSources = append(goSources, source.Content)
		case "js":
			files = append(files, jsImports(root, source)...)
		case ".py":
			files = append(files, pythonImports(root, source)...)
		}
	}
	if module := changes.ReadGoModulePath(root); module != "" && len(goSources) > 0 {
		for _, dir := range changes.LocalImportedGoDirs(root, module, goSources) {
			files = append(files, siblingSources(root, path.Join(dir, "x.go"))...)
		}
	}
	return files
}

func jsImports(root string, source ChangeTarget) []string {
	var files []string
	dir := path.Dir(source.Path)
	for _, match := range jsImportPattern.FindAllStringSubmatch(source.Content, -1) {
		base := path.Join(dir, match[1])
		for _, candidate := range []string{base, base + ".ts", base + ".tsx", base + ".js", base + ".jsx", base + ".mjs", base + "/index.ts", base + "/index.tsx", base + "/index.js"} {
			if regularFile(root, candidate) {
				files = append(files, candidate)
				break
			}
		}
	}
	return files
}

func pythonImports(root string, source ChangeTarget) []string {
	var files []string
	for _, match := range pythonRelativePattern.FindAllStringSubmatch(source.Content, -1) {
		dir := path.Dir(source.Path)
		for i := 1; i < len(match[1]); i++ {
			dir = path.Dir(dir)
		}
		if match[2] != "" {
			files = append(files, pythonModule(root, path.Join(dir, strings.ReplaceAll(match[2], ".", "/")))...)
			continue
		}
		for _, name := range strings.Split(match[3], ",") {
			files = append(files, pythonModule(root, path.Join(dir, strings.TrimSpace(name)))...)
		}
	}
	for _, match := range pythonAbsolutePattern.FindAllStringSubmatch(source.Content, -1) {
		module := match[1] + match[2]
		files = append(files, pythonModule(root, strings.ReplaceAll(module, ".", "/"))...)
	}
	return files
}

func pythonModule(root, module string) []string {
	for _, candidate := range []string{module + ".py", module + "/__init__.py"} {
		if regularFile(root, candidate) {
			return []string{candidate}
		}
	}
	return nil
}

func regularFile(root, file string) bool {
	if strings.HasPrefix(file, "../") || file == ".." {
		return false
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(file)))
	return err == nil && info.Mode().IsRegular()
}
