package projectmap

import (
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

// mainPatterns recognize a program's start in a source file, by language.
var mainPatterns = map[string]*regexp.Regexp{
	"Go":         regexp.MustCompile(`(?m)^package main\b[\s\S]*^func main\(\)`),
	"Python":     regexp.MustCompile(`(?m)^if __name__ == ['"]__main__['"]`),
	"Java":       regexp.MustCompile(`public\s+static\s+void\s+main\s*\(`),
	"Kotlin":     regexp.MustCompile(`(?m)^\s*fun\s+main\s*\(`),
	"C#":         regexp.MustCompile(`static\s+(?:async\s+)?(?:void|int|Task(?:<int>)?)\s+Main\s*\(`),
	"C":          regexp.MustCompile(`(?m)^\s*int\s+main\s*\(`),
	"C++":        regexp.MustCompile(`(?m)^\s*int\s+main\s*\(`),
	"Swift":      regexp.MustCompile(`(?m)^@main\b`),
	"Dart":       regexp.MustCompile(`(?m)^\s*(?:void|Future<void>)\s+main\s*\(`),
	"Rust":       regexp.MustCompile(`(?m)^\s*(?:async\s+)?fn\s+main\s*\(`),
	"JavaScript": nil, "TypeScript": nil,
}

// findEntryPoints finds where the project's programs start: main functions,
// the bin and main of package.json files, and their start scripts.
func findEntryPoints(root string, code []symbols.IndexFile, manifests []Manifest, known map[string]bool) ([]EntryPoint, bool) {
	var entries []EntryPoint
	for _, file := range code {
		pattern := mainPatterns[file.Language]
		if pattern == nil || isTestPath(file.Path) || file.SizeBytes > maxReadBytes {
			continue
		}
		if file.Language == "Rust" && !strings.HasSuffix(file.Path, "src/main.rs") && !strings.Contains(file.Path, "src/bin/") {
			continue
		}
		data, err := readBounded(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err == nil && pattern.Match(data) {
			entries = append(entries, EntryPoint{Path: file.Path, Kind: "main", Detail: file.Language})
		}
	}
	for _, manifest := range manifests {
		entries = append(entries, packageEntries(root, manifest, known)...)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) > maxEntryPoints {
		return entries[:maxEntryPoints], true
	}
	return entries, false
}

// packageEntries are a package.json's bin, main and start scripts.
func packageEntries(root string, manifest Manifest, known map[string]bool) []EntryPoint {
	if manifest.Kind != "node" {
		return nil
	}
	dir := path.Dir(manifest.Path)
	data, err := readBounded(filepath.Join(root, filepath.FromSlash(manifest.Path)))
	if err != nil {
		return nil
	}
	var entries []EntryPoint
	for _, target := range packageTargets(data) {
		file := path.Join(dir, target.file)
		if known[file] {
			entries = append(entries, EntryPoint{Path: file, Kind: target.kind, Detail: target.name})
		}
	}
	for _, name := range []string{"start", "dev", "serve"} {
		if script := manifest.Scripts[name]; script != "" {
			entries = append(entries, EntryPoint{Path: manifest.Path, Kind: "script", Detail: name + ": " + script})
		}
	}
	return entries
}

type packageTarget struct{ kind, name, file string }

var (
	packageMain = regexp.MustCompile(`"main"\s*:\s*"([^"]+)"`)
	packageBin  = regexp.MustCompile(`"bin"\s*:\s*(?:"([^"]+)"|\{([^}]*)\})`)
	binEntry    = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)
)

func packageTargets(data []byte) []packageTarget {
	var targets []packageTarget
	if match := packageMain.FindSubmatch(data); match != nil {
		targets = append(targets, packageTarget{kind: "package main", file: cleanTarget(string(match[1]))})
	}
	if match := packageBin.FindSubmatch(data); match != nil {
		if len(match[1]) > 0 {
			targets = append(targets, packageTarget{kind: "bin", file: cleanTarget(string(match[1]))})
		}
		for _, entry := range binEntry.FindAllSubmatch(match[2], -1) {
			targets = append(targets, packageTarget{kind: "bin", name: string(entry[1]), file: cleanTarget(string(entry[2]))})
		}
	}
	return targets
}

func cleanTarget(target string) string {
	return strings.TrimPrefix(path.Clean(target), "./")
}

// isTestPath tells test files apart in the common layouts, without the
// project's own rules: they never start a program.
func isTestPath(file string) bool {
	base := path.Base(file)
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.Contains("/"+file, "/test/") || strings.Contains("/"+file, "/tests/")
}
