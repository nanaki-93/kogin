package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
)

// maxDocsBriefBytes bounds the project map a coding agent starts with.
const maxDocsBriefBytes = 6 << 10

// docsBrief gives an agent the docs' view of where a task's files sit: the
// components they are in, with their summaries, and the components those
// import or are used by. The docs belong to the main checkout, so a task in
// its own worktree reads them there. Empty without stored summaries.
func (s *Service) docsBrief(files []string) string {
	main := s
	if s.owner != nil {
		main = s.owner
	}
	analysis, err := main.manager.Analysis()
	if err != nil || len(files) == 0 {
		return ""
	}
	index, err := main.manager.Index()
	if err != nil {
		return ""
	}
	stored, err := project.LoadModuleSummaries(main.manager.Root(), analysis.ProjectID)
	if err != nil || len(stored.Entries) == 0 {
		return ""
	}
	components, edges := project.DocsComponents(main.projectMap(index))
	return writeDocsBrief(components, edges, stored.Summaries(), files)
}

func writeDocsBrief(components []projectmap.Component, edges []projectmap.Edge, summaries map[string]project.ModuleSummary, files []string) string {
	named := map[string]bool{}
	for _, file := range files {
		if component := componentOf(components, file); component != "" {
			named[component] = true
		}
	}
	if len(named) == 0 {
		return ""
	}
	links := newComponentLinks(edges)
	var brief strings.Builder
	write := func(path, role string) {
		summary, ok := summaries[path]
		if !ok {
			return
		}
		line := fmt.Sprintf("- %s (%s): %s", path, role, strings.TrimSpace(summary.Summary))
		if named[path] {
			line += links.describe(path)
		}
		if brief.Len()+len(line)+1 <= maxDocsBriefBytes {
			brief.WriteString(line + "\n")
		}
	}
	// The task's components first, then their neighbours.
	order := sortedKeys(named)
	for _, path := range order {
		write(path, "holds the task's files")
	}
	related := links.neighbours(order, named)
	for _, path := range sortedKeys(related) {
		write(path, related[path])
	}
	if brief.Len() == 0 {
		return ""
	}
	return "\nWhere these files sit (Kogin's docs; summaries are model-written from earlier file summaries and may be out of date, the code decides):\n" + brief.String()
}

// componentLinks are the imports between components, both ways.
type componentLinks struct {
	imports, usedBy map[string][]string
}

func newComponentLinks(edges []projectmap.Edge) componentLinks {
	links := componentLinks{imports: map[string][]string{}, usedBy: map[string][]string{}}
	for _, edge := range edges {
		links.imports[edge.From] = append(links.imports[edge.From], edge.To)
		links.usedBy[edge.To] = append(links.usedBy[edge.To], edge.From)
	}
	return links
}

// describe names what a component imports and what uses it.
func (links componentLinks) describe(path string) string {
	text := ""
	if list := links.imports[path]; len(list) > 0 {
		text += " Imports: " + strings.Join(list, ", ") + "."
	}
	if list := links.usedBy[path]; len(list) > 0 {
		text += " Used by: " + strings.Join(list, ", ") + "."
	}
	return text
}

// neighbours are the components next to the named ones, each with how it is
// related to the first named one that reaches it.
func (links componentLinks) neighbours(order []string, named map[string]bool) map[string]string {
	related := map[string]string{}
	add := func(path, role string) {
		if !named[path] && related[path] == "" {
			related[path] = role
		}
	}
	for _, path := range order {
		for _, next := range links.imports[path] {
			add(next, "imported by "+path)
		}
		for _, previous := range links.usedBy[path] {
			add(previous, "uses "+path)
		}
	}
	return related
}

// componentOf is the docs component holding a file: the one with its
// directory, else the deepest one around it.
func componentOf(components []projectmap.Component, file string) string {
	module := projectmap.ModuleOf(file)
	best := ""
	for _, component := range components {
		for _, member := range component.Modules {
			if member == module {
				return component.Path
			}
		}
		inside := component.Path == "." || module == component.Path || strings.HasPrefix(module+"/", component.Path+"/")
		if inside && (best == "" || best == "." || len(component.Path) > len(best) && component.Path != ".") {
			best = component.Path
		}
	}
	return best
}

// changeTargetPaths are the files a task names.
func changeTargetPaths(session *ChangeSession) []string {
	paths := make([]string, 0, len(session.Targets))
	for _, target := range session.Targets {
		paths = append(paths, target.Path)
	}
	return paths
}

func sortedKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
