package projectmap

import (
	"path"
	"sort"
)

// Component is a group of modules shown as one part of the project: a
// directory and the code below it, when the project has too many modules
// to show one by one.
type Component struct {
	Path      string   `json:"path"`
	Modules   []string `json:"modules"`
	Languages []string `json:"languages"`
	Files     int      `json:"files"`
	Lines     int      `json:"lines"`
}

// Components groups the modules into at most limit components: while there
// are too many, the deepest directories are folded into their parent. Edges
// between modules become edges between their components.
func (m Map) Components(limit int) ([]Component, []Edge) {
	groupOf := map[string]string{}
	members := map[string][]string{}
	for _, module := range m.Modules {
		groupOf[module.Path] = module.Path
		members[module.Path] = []string{module.Path}
	}
	for len(members) > max(limit, 1) {
		parent, ok := deepestParent(members)
		if !ok {
			break
		}
		for group := range members {
			if group != parent && group != "." && path.Dir(group) == parent {
				members[parent] = append(members[parent], members[group]...)
				for _, module := range members[group] {
					groupOf[module] = parent
				}
				delete(members, group)
			}
		}
	}
	return m.components(members, groupOf), m.componentEdges(groupOf)
}

// deepestParent picks the parent to fold its children into: the deepest
// one whose fold leaves fewer groups (smallest first, so small folders fold
// before big ones), else the deepest, which moves its child up a level.
func deepestParent(members map[string][]string) (string, bool) {
	type candidate struct{ children, size int }
	parents := map[string]*candidate{}
	for group, modules := range members {
		if group == "." {
			continue
		}
		parent := path.Dir(group)
		if parents[parent] == nil {
			parents[parent] = &candidate{}
		}
		parents[parent].children++
		parents[parent].size += len(modules)
	}
	best, bestDepth, bestSize, bestReduces := "", -1, 0, false
	for parent, c := range parents {
		_, exists := members[parent]
		reduces := c.children > 1 || exists
		depth := Depth(parent)
		better := reduces && !bestReduces ||
			reduces == bestReduces && (depth > bestDepth || depth == bestDepth && (c.size < bestSize || c.size == bestSize && parent < best))
		if better {
			best, bestDepth, bestSize, bestReduces = parent, depth, c.size, reduces
		}
	}
	return best, bestDepth >= 0
}

func (m Map) components(members map[string][]string, groupOf map[string]string) []Component {
	byPath := map[string]*Component{}
	languages := map[string]map[string]int{}
	for _, module := range m.Modules {
		group := groupOf[module.Path]
		component := byPath[group]
		if component == nil {
			component = &Component{Path: group}
			byPath[group], languages[group] = component, map[string]int{}
		}
		component.Files += module.Files
		component.Lines += module.Lines
		for rank, language := range module.Languages {
			languages[group][language] += len(module.Languages) - rank
		}
	}
	components := make([]Component, 0, len(byPath))
	for group, component := range byPath {
		component.Modules = append([]string(nil), members[group]...)
		sort.Strings(component.Modules)
		component.Languages = rankedLanguages(languages[group])
		components = append(components, *component)
	}
	sort.Slice(components, func(i, j int) bool { return components[i].Path < components[j].Path })
	return components
}

func (m Map) componentEdges(groupOf map[string]string) []Edge {
	counts := map[[2]string]int{}
	for _, edge := range m.Edges {
		from, to := groupOf[edge.From], groupOf[edge.To]
		if from != "" && to != "" && from != to {
			counts[[2]string{from, to}] += edge.Imports
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
