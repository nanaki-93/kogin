package handbook

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/projectmap"
	"github.com/nanaki-93/mini-orca/v2/internal/project/toolchain"
)

const (
	// docsComponentLimit is how many components the docs and the diagram show.
	docsComponentLimit  = 16
	maxDocsDependencies = 120
)

// buildMapComponents turns the project map's components into the docs'
// components, with their resolved dependencies, entry points and summaries.
func buildMapComponents(in DocsInputs) ([]ProjectDocsComponent, []projectmap.Edge) {
	groups, edges := in.Map.Components(docsComponentLimit)
	dependencies, usedBy := map[string][]string{}, map[string][]string{}
	for _, edge := range edges {
		dependencies[edge.From] = append(dependencies[edge.From], edge.To)
		usedBy[edge.To] = append(usedBy[edge.To], edge.From)
	}
	overview := overviewComponents(in.Analysis.Report.Components)
	components := make([]ProjectDocsComponent, 0, len(groups))
	for _, group := range groups {
		name := componentName(group.Path, in.Analysis.Name)
		component := ProjectDocsComponent{
			Name: name, Layer: classifyComponentLayer(group.Path, name), Path: group.Path,
			Languages: group.Languages, Files: group.Files, Lines: group.Lines,
			Responsibilities: []string{}, Contracts: entryContracts(group, in.Map.EntryPoints),
			Dependencies: orEmpty(dependencies[group.Path]), UsedBy: orEmpty(usedBy[group.Path]),
		}
		if summary, ok := in.Summaries[group.Path]; ok && strings.TrimSpace(summary.Summary) != "" {
			component.Summary, component.Responsibilities, component.SummarySource = summary.Summary, orEmpty(summary.Responsibilities), "module"
		} else if model, ok := overviewFor(group.Path, overview); ok {
			component.Summary, component.Responsibilities, component.SummarySource = model.Summary, model.Responsibilities, "overview"
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

// overviewFor finds the overview's component for a directory: the same
// path, else the most specific one inside it.
func overviewFor(dir string, overview []ProjectDocsComponent) (ProjectDocsComponent, bool) {
	best, found := ProjectDocsComponent{}, false
	for _, component := range overview {
		inside := component.Path == dir || dir == "." || strings.HasPrefix(component.Path, dir+"/")
		if inside && (!found || component.Path == dir || len(component.Path) > len(best.Path) && best.Path != dir) {
			best, found = component, true
		}
	}
	return best, found
}

// entryContracts lists the entry points in a component's modules.
func entryContracts(group projectmap.Component, entries []projectmap.EntryPoint) []ProjectDocsContract {
	contracts := []ProjectDocsContract{}
	for _, entry := range entries {
		if !inModules(projectmap.ModuleOf(entry.Path), group) {
			continue
		}
		summary := entry.Detail
		if summary == "" {
			summary = entry.Kind
		}
		contracts = append(contracts, ProjectDocsContract{Name: entry.Path, Kind: entry.Kind, Summary: summary})
	}
	return contracts
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
			entry := ProjectDocsDependency{Name: dependency.Name, Kind: kind, Version: dependency.Version, Source: manifest.Path, Summary: "Declared in " + manifest.Path}
			if dependency.Dev {
				entry.Kind += " (development)"
				dev = append(dev, entry)
			} else {
				runtime = append(runtime, entry)
			}
		}
	}
	all := append(runtime, dev...)
	if len(all) > maxDocsDependencies {
		all = all[:maxDocsDependencies]
	}
	return all
}

// buildRunbook lists the commands the project declares: each subproject's
// test and lint commands, and the package scripts that build or start it.
func buildRunbook(profiles []toolchain.Profile, manifests []projectmap.Manifest) []ProjectDocsRunbookItem {
	items := []ProjectDocsRunbookItem{}
	for _, profile := range profiles {
		where := "the project root"
		if profile.Dir != "" {
			where = profile.Dir + "/"
		}
		for _, check := range []struct {
			label   string
			command []string
		}{{"Test", profile.Test}, {"Lint", profile.Lint}} {
			if len(check.command) > 0 {
				items = append(items, ProjectDocsRunbookItem{Label: runbookLabel(check.label, profile.Dir), Command: strings.Join(check.command, " "), Description: fmt.Sprintf("From %s; runs in %s.", profile.Source, where)})
			}
		}
	}
	for _, manifest := range manifests {
		if manifest.Kind != "node" {
			continue
		}
		dir := path.Dir(manifest.Path)
		names := make([]string, 0, len(manifest.Scripts))
		for name := range manifest.Scripts {
			if name == "build" || name == "dev" || name == "start" || name == "serve" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			items = append(items, ProjectDocsRunbookItem{Label: runbookLabel("Run "+name, strings.TrimPrefix(dir, ".")), Command: "npm run " + name, Description: fmt.Sprintf("Script in %s: %s", manifest.Path, manifest.Scripts[name])})
		}
	}
	return items
}

func runbookLabel(label, dir string) string {
	if dir == "" {
		return label
	}
	return label + " (" + dir + ")"
}

func orEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
