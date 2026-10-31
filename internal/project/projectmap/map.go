// Package projectmap describes a project's structure as its code states it:
// the directories that hold code (modules), which modules import which,
// where the programs start, and the manifests with their dependencies. It
// reads files but calls no model, so it works for any project and is the
// same every time for the same files.
package projectmap

import (
	"path"
	"sort"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
)

// Map is a project's structure.
type Map struct {
	Modules     []Module     `json:"modules"`
	Edges       []Edge       `json:"edges"`
	EntryPoints []EntryPoint `json:"entry_points"`
	Manifests   []Manifest   `json:"manifests"`
	// Truncated says that a limit left modules, edges or entry points out.
	Truncated bool `json:"truncated,omitempty"`
}

// Module is a directory that holds code. "." is the project root.
type Module struct {
	Path      string   `json:"path"`
	Languages []string `json:"languages"`
	Files     int      `json:"files"`
	Lines     int      `json:"lines"`
}

// Edge says that code in From imports code in To, Imports times.
type Edge struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Imports int    `json:"imports"`
}

// EntryPoint is a place a program starts: a main function, a package's bin
// or main, a script.
type EntryPoint struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// Manifest is a build or package file and what it declares.
type Manifest struct {
	Path         string       `json:"path"`
	Kind         string       `json:"kind"`
	Name         string       `json:"name,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	// Scripts are the commands a package.json declares, by name.
	Scripts map[string]string `json:"scripts,omitempty"`
}

// Dependency is one dependency a manifest declares.
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Dev     bool   `json:"dev,omitempty"`
}

const (
	maxModules     = 2000
	maxEdges       = 8000
	maxEntryPoints = 64
	// maxReadBytes bounds a file read for entry points or manifests.
	maxReadBytes = 256 << 10
)

// nonCode are the languages of documents, data and styles: they hold no
// code to import, so their directories are not modules.
var nonCode = map[string]bool{
	"": true, "Text": true, "Markdown": true, "JSON": true, "YAML": true, "TOML": true, "INI": true, "Config": true,
	"XML": true, "HTML": true, "CSS": true, "SCSS": true, "Makefile": true, "Dockerfile": true, "CMake": true,
}

// IsCode reports whether a language holds code that modules are made of.
func IsCode(language string) bool { return !nonCode[language] }

// Build maps the project at root from its index. Only files in the index
// count; manifests are also looked for next to them.
func Build(root string, files []symbols.IndexFile) Map {
	known := map[string]bool{}
	var code []symbols.IndexFile
	for _, file := range files {
		known[file.Path] = true
		if !file.Binary && IsCode(file.Language) {
			code = append(code, file)
		}
	}
	sort.Slice(code, func(i, j int) bool { return code[i].Path < code[j].Path })
	m := Map{}
	m.Modules = modulesOf(code)
	if len(m.Modules) > maxModules {
		m.Modules, m.Truncated = m.Modules[:maxModules], true
	}
	moduleSet := map[string]bool{}
	for _, module := range m.Modules {
		moduleSet[module.Path] = true
	}
	m.Manifests = readManifests(root, manifestPaths(root, files, m.Modules))
	resolver := newResolver(root, known, moduleSet, m.Manifests)
	m.Edges = resolver.edges(code)
	if len(m.Edges) > maxEdges {
		m.Edges, m.Truncated = m.Edges[:maxEdges], true
	}
	var truncated bool
	m.EntryPoints, truncated = findEntryPoints(root, code, m.Manifests, known)
	m.Truncated = m.Truncated || truncated
	return m
}

// ModuleOf is the module a file belongs to: its directory.
func ModuleOf(file string) string {
	return path.Dir(file)
}

func modulesOf(code []symbols.IndexFile) []Module {
	byDir := map[string]*Module{}
	languages := map[string]map[string]int{}
	for _, file := range code {
		dir := ModuleOf(file.Path)
		module := byDir[dir]
		if module == nil {
			module = &Module{Path: dir}
			byDir[dir], languages[dir] = module, map[string]int{}
		}
		module.Files++
		module.Lines += file.LineCount
		languages[dir][file.Language]++
	}
	modules := make([]Module, 0, len(byDir))
	for dir, module := range byDir {
		module.Languages = rankedLanguages(languages[dir])
		modules = append(modules, *module)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules
}

// rankedLanguages lists languages by file count, most first.
func rankedLanguages(counts map[string]int) []string {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// Depth is how many directories deep a module is; the root is 0.
func Depth(module string) int {
	if module == "." || module == "" {
		return 0
	}
	return strings.Count(module, "/") + 1
}
