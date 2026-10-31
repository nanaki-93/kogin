package projectmap

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
)

// manifestKinds maps a manifest's file name to its ecosystem.
var manifestKinds = map[string]string{
	"go.mod": "go", "package.json": "node", "Cargo.toml": "rust",
	"pyproject.toml": "python", "setup.py": "python", "requirements.txt": "python",
	"pom.xml": "maven", "build.gradle": "gradle", "build.gradle.kts": "gradle",
	"Gemfile": "ruby", "composer.json": "php", "pubspec.yaml": "dart", "mix.exs": "elixir",
}

const (
	maxDependencies = 80
	// maxManifestDepth bounds where manifests are looked for.
	maxManifestDepth = 4
)

// manifestPaths finds manifests: the indexed ones, and those in the root and
// in the directories that hold code (the index leaves some without an
// extension out).
func manifestPaths(root string, files []symbols.IndexFile, modules []Module) []string {
	found := map[string]bool{}
	for _, file := range files {
		if isManifest(file.Path) {
			found[file.Path] = true
		}
	}
	dirs := map[string]bool{".": true}
	for _, module := range modules {
		for dir := module.Path; dir != "." && Depth(dir) <= maxManifestDepth; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for dir := range dirs {
		for name := range manifestKinds {
			candidate := path.Join(dir, name)
			if found[candidate] {
				continue
			}
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil && info.Mode().IsRegular() {
				found[candidate] = true
			}
		}
	}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func isManifest(file string) bool {
	base := path.Base(file)
	return manifestKinds[base] != "" || strings.HasSuffix(base, ".csproj")
}

func readManifests(root string, paths []string) []Manifest {
	var manifests []Manifest
	for _, p := range paths {
		data, err := readBounded(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		manifest := parseManifest(p, string(data))
		if len(manifest.Dependencies) > maxDependencies {
			manifest.Dependencies = manifest.Dependencies[:maxDependencies]
		}
		manifests = append(manifests, manifest)
	}
	return manifests
}

func parseManifest(file, text string) Manifest {
	base := path.Base(file)
	kind := manifestKinds[base]
	if strings.HasSuffix(base, ".csproj") {
		kind = "dotnet"
	}
	manifest := Manifest{Path: file, Kind: kind}
	switch base {
	case "go.mod":
		manifest.Name, manifest.Dependencies = parseGoMod(text)
	case "package.json":
		parsePackageJSON(text, &manifest)
	case "Cargo.toml":
		manifest.Name = tomlValue(tomlSection(text, "package"), "name")
		manifest.Dependencies = tomlKeys(tomlSection(text, "dependencies"), false)
		manifest.Dependencies = append(manifest.Dependencies, tomlKeys(tomlSection(text, "dev-dependencies"), true)...)
	case "pyproject.toml":
		manifest.Name = tomlValue(tomlSection(text, "project"), "name")
		manifest.Dependencies = pythonRequirements(tomlArray(tomlSection(text, "project"), "dependencies"))
		manifest.Dependencies = append(manifest.Dependencies, tomlKeys(tomlSection(text, "tool.poetry.dependencies"), false)...)
	case "requirements.txt":
		manifest.Dependencies = pythonRequirements(strings.Split(text, "\n"))
	case "pom.xml":
		manifest.Name, manifest.Dependencies = parsePom(text)
	case "build.gradle", "build.gradle.kts":
		manifest.Dependencies = matchedDependencies(gradleDependency, text)
	default:
		if kind == "dotnet" {
			manifest.Dependencies = matchedDependencies(packageReference, text)
		}
	}
	return manifest
}

var goRequire = regexp.MustCompile(`^\s*(?:require\s+)?([\w.\-/~]+\.[\w.\-/~]+)\s+(v[\w.\-+]+)(\s*//\s*indirect)?`)

func parseGoMod(text string) (string, []Dependency) {
	name := ""
	var dependencies []Dependency
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fields := strings.Fields(trimmed); len(fields) >= 2 && fields[0] == "module" {
			name = strings.Trim(fields[1], `"`)
			continue
		}
		if match := goRequire.FindStringSubmatch(trimmed); match != nil && match[3] == "" && !strings.HasPrefix(trimmed, "module") && !strings.HasPrefix(trimmed, "go ") {
			dependencies = append(dependencies, Dependency{Name: match[1], Version: match[2]})
		}
	}
	return name, dependencies
}

func parsePackageJSON(text string, manifest *Manifest) {
	var pkg struct {
		Name            string            `json:"name"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(text), &pkg) != nil {
		return
	}
	manifest.Name, manifest.Scripts = pkg.Name, pkg.Scripts
	manifest.Dependencies = append(sortedDependencies(pkg.Dependencies, false), sortedDependencies(pkg.DevDependencies, true)...)
}

func sortedDependencies(versions map[string]string, dev bool) []Dependency {
	dependencies := make([]Dependency, 0, len(versions))
	for name, version := range versions {
		dependencies = append(dependencies, Dependency{Name: name, Version: version, Dev: dev})
	}
	sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].Name < dependencies[j].Name })
	return dependencies
}

// tomlSection returns the lines of one [section] of a TOML file.
func tomlSection(text, section string) []string {
	var lines []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inside = strings.Trim(trimmed, "[] ") == section
			continue
		}
		if inside && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func tomlValue(lines []string, key string) string {
	for _, line := range lines {
		if name, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(name) == key {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func tomlKeys(lines []string, dev bool) []Dependency {
	var dependencies []Dependency
	for _, line := range lines {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) == "python" {
			continue
		}
		version := strings.Trim(strings.TrimSpace(value), `"'`)
		if strings.HasPrefix(strings.TrimSpace(value), "{") {
			version = ""
			if match := inlineVersion.FindStringSubmatch(value); match != nil {
				version = match[1]
			}
		}
		dependencies = append(dependencies, Dependency{Name: strings.Trim(strings.TrimSpace(name), `"`), Version: version, Dev: dev})
	}
	return dependencies
}

// tomlArray reads a key = ["a", "b"] array, also across lines.
func tomlArray(lines []string, key string) []string {
	joined := strings.Join(lines, "\n")
	start := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*\[`).FindStringIndex(joined)
	if start == nil {
		return nil
	}
	end := strings.Index(joined[start[1]:], "]")
	if end < 0 {
		return nil
	}
	var items []string
	for _, item := range strings.Split(joined[start[1]:start[1]+end], ",") {
		if item = strings.Trim(strings.TrimSpace(item), `"'`); item != "" {
			items = append(items, item)
		}
	}
	return items
}

var inlineVersion = regexp.MustCompile(`version\s*=\s*["']([^"']+)["']`)

var requirementName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._\-\[\]]*)\s*(.*)$`)

func pythonRequirements(lines []string) []Dependency {
	var dependencies []Dependency
	for _, line := range lines {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		if match := requirementName.FindStringSubmatch(line); match != nil {
			dependencies = append(dependencies, Dependency{Name: match[1], Version: strings.TrimSpace(strings.SplitN(match[2], ";", 2)[0])})
		}
	}
	return dependencies
}

var (
	pomDependency    = regexp.MustCompile(`(?s)<dependency>.*?<artifactId>([^<]+)</artifactId>(?:\s*<version>([^<]+)</version>)?.*?</dependency>`)
	pomArtifact      = regexp.MustCompile(`<artifactId>([^<]+)</artifactId>`)
	gradleDependency = regexp.MustCompile(`(?m)^\s*(?:implementation|api|compileOnly|runtimeOnly|testImplementation)\s*\(?\s*["']([^:"']+:[^:"']+)(?::([^"']+))?["']`)
	packageReference = regexp.MustCompile(`<PackageReference\s+Include="([^"]+)"(?:\s+Version="([^"]+)")?`)
)

func parsePom(text string) (string, []Dependency) {
	body := text
	if index := strings.Index(text, "<dependencies>"); index >= 0 {
		body = text[:index]
	}
	name := ""
	if parent := strings.Index(body, "</parent>"); parent >= 0 {
		body = body[parent:]
	}
	if match := pomArtifact.FindStringSubmatch(body); match != nil {
		name = match[1]
	}
	return name, matchedDependencies(pomDependency, text)
}

func matchedDependencies(pattern *regexp.Regexp, text string) []Dependency {
	var dependencies []Dependency
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		dependency := Dependency{Name: match[1]}
		if len(match) > 2 {
			dependency.Version = match[2]
		}
		dependencies = append(dependencies, dependency)
	}
	return dependencies
}
