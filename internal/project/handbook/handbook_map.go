package handbook

import (
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
	"github.com/nanaki-93/kogin/v2/internal/project/toolchain"
)

const (
	// docsComponentLimit is how many components the docs and the diagram show.
	docsComponentLimit  = 16
	maxDocsDependencies = 120
)

// DocsComponents are the components the docs show for a project map.
func DocsComponents(m projectmap.Map) ([]projectmap.Component, []projectmap.Edge) {
	return m.Components(docsComponentLimit)
}

// buildMapComponents turns the project map's components into the docs'
// components, with their resolved dependencies, entry points and summaries.
func buildMapComponents(in DocsInputs) ([]ProjectDocsComponent, []projectmap.Edge) {
	groups, edges := DocsComponents(in.Map)
	dependencies, usedBy := map[string][]string{}, map[string][]string{}
	for _, edge := range edges {
		dependencies[edge.From] = append(dependencies[edge.From], edge.To)
		usedBy[edge.To] = append(usedBy[edge.To], edge.From)
	}
	overview := overviewComponents(in.Analysis.Report.Components)
	components := make([]ProjectDocsComponent, 0, len(groups))
	for _, group := range groups {
		component := ProjectDocsComponent{
			Name: componentName(group.Path, in.Analysis.Name), Path: group.Path,
			Languages: group.Languages, Files: group.Files, Lines: group.Lines,
			Responsibilities: []string{}, EntryPoints: componentEntryPoints(group, in.Map.EntryPoints),
			Imports: orEmpty(dependencies[group.Path]), UsedBy: orEmpty(usedBy[group.Path]),
		}
		if summary, ok := in.Summaries[group.Path]; ok && strings.TrimSpace(summary.Summary) != "" {
			component.Summary, component.Responsibilities, component.SummarySource = summary.Summary, newResponsibilities(summary.Summary, summary.Responsibilities), "module"
		} else if model, ok := overviewFor(group.Path, overview); ok && model.Summary != "" {
			component.Summary, component.SummarySource = model.Summary, "overview"
		}
		components = append(components, component)
	}
	return components, edges
}

func componentName(dir, project string) string {
	if dir == "." {
		if project != "" {
			return project
		}
		return "Project root"
	}
	return humanizeComponentName(path.Base(dir))
}

// overviewComponents are the components the project overview's model named,
// by path.
func overviewComponents(raw []string) []ProjectDocsComponent {
	var components []ProjectDocsComponent
	for _, entry := range raw {
		component := parseReportComponent(entry)
		if component.Path != "" && !path.IsAbs(component.Path) && !strings.HasPrefix(path.Clean(component.Path), "..") {
			component.Path = strings.Trim(path.Clean(component.Path), "/")
			components = append(components, component)
		}
	}
	return components
}

// overviewFor finds the overview's line for a component's directory. Only
// the same path counts: a line on one of its subdirectories would describe
// the whole component by one part of it.
func overviewFor(dir string, overview []ProjectDocsComponent) (ProjectDocsComponent, bool) {
	for _, component := range overview {
		if component.Path == dir {
			return component, true
		}
	}
	return ProjectDocsComponent{}, false
}

// componentEntryPoints are the entry points in a component's modules.
func componentEntryPoints(group projectmap.Component, entries []projectmap.EntryPoint) []projectmap.EntryPoint {
	inside := []projectmap.EntryPoint{}
	for _, entry := range entries {
		if inModules(projectmap.ModuleOf(entry.Path), group) {
			inside = append(inside, entry)
		}
	}
	return inside
}

func inModules(module string, group projectmap.Component) bool {
	for _, member := range group.Modules {
		if member == module {
			return true
		}
	}
	return group.Path != "." && strings.HasPrefix(module+"/", group.Path+"/")
}

// ecosystemNames name the kind of a manifest's dependencies.
var ecosystemNames = map[string]string{
	"go": "Go module", "node": "npm package", "rust": "crate", "python": "Python package",
	"maven": "Java library", "gradle": "Java library", "dotnet": "NuGet package",
	"ruby": "gem", "php": "Composer package", "dart": "Dart package", "elixir": "Hex package",
}

// manifestDependencies lists what the manifests declare, runtime
// dependencies before development ones.
func manifestDependencies(manifests []projectmap.Manifest) []ProjectDocsDependency {
	var runtime, dev []ProjectDocsDependency
	seen := map[string]bool{}
	for _, manifest := range manifests {
		kind := ecosystemNames[manifest.Kind]
		if kind == "" {
			kind = manifest.Kind
		}
		for _, dependency := range manifest.Dependencies {
			key := kind + "\x00" + dependency.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			entry := ProjectDocsDependency{Name: dependency.Name, Kind: kind, Version: dependency.Version, Dev: dependency.Dev, Source: manifest.Path}
			if dependency.Dev {
				dev = append(dev, entry)
			} else {
				runtime = append(runtime, entry)
			}
		}
	}
	all := append(append([]ProjectDocsDependency{}, runtime...), dev...)
	if len(all) > maxDocsDependencies {
		all = all[:maxDocsDependencies]
	}
	return all
}

// buildCommands lists the commands the project declares: each subproject's
// test and lint commands, and the package scripts that build or start it.
func buildCommands(profiles []toolchain.Profile, manifests []projectmap.Manifest) []ProjectDocsCommand {
	commands := []ProjectDocsCommand{}
	for _, profile := range profiles {
		for _, check := range []struct {
			label   string
			command []string
		}{{"Test", profile.Test}, {"Lint", profile.Lint}} {
			if len(check.command) > 0 {
				commands = append(commands, ProjectDocsCommand{Label: check.label, Command: strings.Join(check.command, " "), Dir: profile.Dir, Source: profile.Source})
			}
		}
	}
	for _, manifest := range manifests {
		if manifest.Kind != "node" {
			continue
		}
		names := make([]string, 0, len(manifest.Scripts))
		for name := range manifest.Scripts {
			if name == "build" || name == "dev" || name == "start" || name == "serve" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		dir := strings.TrimPrefix(path.Dir(manifest.Path), ".")
		for _, name := range names {
			commands = append(commands, ProjectDocsCommand{Label: strings.ToUpper(name[:1]) + name[1:], Command: "npm run " + name, Dir: dir, Source: manifest.Path})
		}
	}
	return commands
}

func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}

// newResponsibilities are the responsibilities that say something the
// summary does not: one whose words the summary all has is left out.
func newResponsibilities(summary string, responsibilities []string) []string {
	said := map[string]bool{}
	for _, word := range contentWords(summary) {
		said[word] = true
	}
	kept := []string{}
	for _, responsibility := range responsibilities {
		words := contentWords(responsibility)
		if len(words) == 0 || slices.ContainsFunc(words, func(word string) bool { return !said[word] }) {
			kept = append(kept, responsibility)
		}
	}
	return kept
}

// contentWords are text's words in lower case without a plural or
// third-person "s", leaving out short ones ("a", "of", "the").
func contentWords(text string) []string {
	words := []string{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(word) > 3 {
			words = append(words, strings.TrimSuffix(word, "s"))
		}
	}
	return words
}
