package app

import (
	"errors"
	"fmt"
	"sort"

	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/projectmap"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

// maxFileOneLiner bounds the summary line the docs show for a file.
const maxFileOneLiner = 160

// ErrUnknownDocsComponent is returned for a path the docs show no component at.
var ErrUnknownDocsComponent = errors.New("the docs show no component at this path")

// DocsComponentFiles are the code files of one docs component.
type DocsComponentFiles struct {
	Path  string              `json:"path"`
	Files []DocsComponentFile `json:"files"`
}

// DocsComponentFile is one code file and the first sentence of its summary,
// when the last analysis summarized its current content.
type DocsComponentFile struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Lines    int    `json:"lines"`
	Summary  string `json:"summary,omitempty"`
}

// DocsComponentFiles lists a docs component's code files with a line on each.
// It reads saved summaries only and never calls a model.
func (s *Service) DocsComponentFiles(componentPath string) (*DocsComponentFiles, error) {
	_, index, _, err := s.workspaceState()
	if err != nil {
		return nil, err
	}
	components, _ := project.DocsComponents(s.projectMap(index))
	var modules map[string]bool
	for _, component := range components {
		if component.Path == componentPath {
			modules = make(map[string]bool, len(component.Modules))
			for _, module := range component.Modules {
				modules[module] = true
			}
		}
	}
	if modules == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDocsComponent, componentPath)
	}
	cache, err := project.NewFileAnalysisCache(s.manager.Root())
	if err != nil {
		return nil, err
	}
	result := &DocsComponentFiles{Path: componentPath, Files: []DocsComponentFile{}}
	for _, file := range index.Files {
		if file.Binary || !projectmap.IsCode(file.Language) || !modules[projectmap.ModuleOf(file.Path)] {
			continue
		}
		entry := DocsComponentFile{Path: file.Path, Language: file.Language, Lines: file.LineCount}
		summary, err := cache.Summary(file.Path, file.ContentHash)
		if err != nil {
			return nil, err
		}
		if summary != nil {
			entry.Summary = textutil.FirstSentence(summary.Purpose, maxFileOneLiner)
		}
		result.Files = append(result.Files, entry)
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}
