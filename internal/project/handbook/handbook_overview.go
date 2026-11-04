package handbook

import (
	"sort"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project/projectmap"
)

// buildDocsOverview is the docs' first page: the overview's purpose and
// architecture, and the project's size and languages from the index.
func buildDocsOverview(analysis Analysis) ProjectDocsOverview {
	overview := ProjectDocsOverview{
		Purpose:   strings.TrimSpace(analysis.Report.Purpose),
		Languages: codeLanguages(analysis.Languages),
		Files:     analysis.FileCount,
		Lines:     analysis.TotalLines,
	}
	// Overviews from older prompts drew the architecture as a Mermaid
	// diagram; the components diagram is drawn from the code instead.
	if architecture := strings.TrimSpace(analysis.Report.Architecture); !isMermaidDiagram(architecture) {
		overview.Architecture = architecture
	}
	return overview
}

func isMermaidDiagram(text string) bool {
	for _, prefix := range []string{"flowchart ", "graph ", "sequenceDiagram"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// codeLanguages are the project's programming languages, most files first.
func codeLanguages(counts map[string]int) []ProjectDocsLanguage {
	languages := []ProjectDocsLanguage{}
	for name, files := range counts {
		if projectmap.IsCode(name) && files > 0 {
			languages = append(languages, ProjectDocsLanguage{Name: name, Files: files})
		}
	}
	sort.Slice(languages, func(i, j int) bool {
		if languages[i].Files != languages[j].Files {
			return languages[i].Files > languages[j].Files
		}
		return languages[i].Name < languages[j].Name
	})
	return languages
}

// currentWalkthroughs are the overview's walkthroughs (or change guide)
// with only the steps whose files the project still has; a walkthrough left
// without steps is dropped.
func currentWalkthroughs(projectFiles []string, report []Walkthrough) []Walkthrough {
	files := make(map[string]bool, len(projectFiles))
	for _, file := range projectFiles {
		files[file] = true
	}
	walkthroughs := []Walkthrough{}
	for _, walkthrough := range report {
		steps := []WalkthroughStep{}
		for _, step := range walkthrough.Steps {
			if files[step.Path] {
				steps = append(steps, step)
			}
		}
		if len(steps) > 0 {
			walkthroughs = append(walkthroughs, Walkthrough{Title: walkthrough.Title, Summary: walkthrough.Summary, Steps: steps})
		}
	}
	return walkthroughs
}
