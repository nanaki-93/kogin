package app

import (
	"context"
	"path"
	"sort"

	"github.com/nanaki-93/kasuri/v2/internal/app/archgate"
	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/rules"
)

// ProjectRules is what the Web UI needs to apply the project's code rules on
// its own: which files are tests, and which AGENTS.md files declare rules.
type ProjectRules struct {
	ProjectID       string          `json:"project_id"`
	ProjectRevision string          `json:"project_revision"`
	TestPatterns    []string        `json:"test_patterns"`
	Guides          []string        `json:"guides"`
	Problems        []rules.Problem `json:"problems"`
}

// ProjectCodeRules reads every indexed AGENTS.md for its test file patterns.
func (s *Service) ProjectCodeRules() (*ProjectRules, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	guides := []string{}
	for _, file := range index.Files {
		if path.Base(file.Path) == "AGENTS.md" {
			guides = append(guides, file.Path)
		}
	}
	sort.Strings(guides)
	resolver := rules.NewResolver(s.manager.Root())
	patterns := resolver.TestPatterns(guides...)
	problems := resolver.Problems()
	if problems == nil {
		problems = []rules.Problem{}
	}
	return &ProjectRules{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, TestPatterns: patterns, Guides: guides, Problems: problems}, nil
}

// ChangeRulesFile is how one file of a task stands against its rules: the
// proposal when the task changed the file, the file as it is otherwise.
type ChangeRulesFile struct {
	archgate.BudgetReport
	Test     bool               `json:"test"`
	Proposed bool               `json:"proposed"`
	Imports  []rules.ImportRule `json:"imports,omitempty"`
	Sources  []string           `json:"sources,omitempty"`
}

// ChangeRules lists the rules of every file in a task.
type ChangeRules struct {
	SessionID string            `json:"session_id"`
	Revision  int64             `json:"revision"`
	Files     []ChangeRulesFile `json:"files"`
	Problems  []rules.Problem   `json:"problems"`
}

// ChangeCodeRules measures a task's files against their rules, live.
func (s *Service) ChangeCodeRules(ctx context.Context, id string) (*ChangeRules, error) {
	root := s.manager.Root()
	session, err := loadChangeSession(root, id)
	if err != nil {
		return nil, err
	}
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	if session.ProjectID != index.ProjectID {
		return nil, project.ErrRevisionConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	edits := make([]ChangeEdit, 0, len(session.Targets))
	for _, target := range session.Targets {
		if edit := proposedEdit(session, target.Path); edit != nil {
			edits = append(edits, *edit)
		} else {
			edits = append(edits, ChangeEdit{Path: target.Path, Content: target.Content})
		}
	}
	resolver := rules.NewResolver(root)
	result := &ChangeRules{SessionID: session.ID, Revision: session.Revision, Files: []ChangeRulesFile{}}
	for _, report := range archgate.BudgetReports(root, session.Targets, edits) {
		declared := resolver.For(report.Path)
		target := changeTarget(session, report.Path)
		result.Files = append(result.Files, ChangeRulesFile{
			BudgetReport: report, Test: target != nil && isTestTarget(*target), Proposed: proposedEdit(session, report.Path) != nil,
			Imports: declared.Imports, Sources: declared.Sources,
		})
	}
	result.Problems = resolver.Problems()
	if result.Problems == nil {
		result.Problems = []rules.Problem{}
	}
	return result, nil
}
