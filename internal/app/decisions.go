package app

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/project"
	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// The decision log: a project's design decisions as numbered Markdown files
// in the repository (architecture decision records), each saying what was
// decided between which options and why. Kogin only reads them: they are
// written and changed in ordinary commits, so they are versioned and
// reviewed with the code. The Docs list them, and an investigation is told
// they exist so a plan does not quietly go against one.

// decisionDirs are where a project keeps its decision records; the first
// that holds any is the log.
var decisionDirs = []string{"docs/decisions", "docs/adr", "doc/adr", "adr", "decisions"}

// decisionFile is a record's name: its number, then its title in words.
var decisionFile = regexp.MustCompile(`^(\d{1,4})-[^/]+\.md$`)

const (
	maxDecisions        = 200
	maxDecisionBytes    = 32 << 10
	maxDecisionSection  = 4000
	maxDecisionsInBrief = 20
)

// Decision statuses as the log reads them; anything else is shown as written.
const (
	DecisionAccepted   = "accepted"
	DecisionProposed   = "proposed"
	DecisionSuperseded = "superseded"
)

// Decision is one record: its number and title, its status and date when
// the file states them, and its sections in the file's order.
type Decision struct {
	Number   int               `json:"number"`
	Title    string            `json:"title"`
	Status   string            `json:"status,omitempty"`
	Date     string            `json:"date,omitempty"`
	Path     string            `json:"path"`
	Sections []DecisionSection `json:"sections,omitempty"`
}

// DecisionSection is one "## Heading" of a record and its text.
type DecisionSection struct {
	Heading string `json:"heading"`
	Text    string `json:"text"`
}

// DecisionsReport is the project's decision log, newest first.
type DecisionsReport struct {
	ProjectID       string `json:"project_id"`
	ProjectRevision string `json:"project_revision"`
	// Dir is where the records are, or where Kogin looks first when the
	// project has none.
	Dir       string     `json:"dir"`
	Decisions []Decision `json:"decisions"`
}

// Decisions reads the project's decision log from its indexed files.
func (s *Service) Decisions(ctx context.Context) (*DecisionsReport, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	policy, err := project.NewContextPolicy(s.manager.Root())
	if err != nil {
		return nil, err
	}
	report := &DecisionsReport{ProjectID: index.ProjectID, ProjectRevision: index.ProjectRevision, Dir: decisionDirs[0], Decisions: []Decision{}}
	dir, files := decisionRecords(index.Files)
	if dir == "" {
		return report, nil
	}
	report.Dir = dir
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := policy.ReadSource(ctx, file.Path, file.ContentHash)
		if err != nil {
			// A record that changed since indexing, or that the policy leaves out, is not listed.
			continue
		}
		report.Decisions = append(report.Decisions, parseDecision(file.Path, string(data)))
	}
	slices.SortFunc(report.Decisions, func(a, b Decision) int { return b.Number - a.Number })
	return report, nil
}

// decisionRecords are the decision files of the first directory that has
// any, within the bounds of the log.
func decisionRecords(files []project.IndexFile) (string, []project.IndexFile) {
	for _, dir := range decisionDirs {
		var records []project.IndexFile
		for _, file := range files {
			if path.Dir(file.Path) == dir && decisionFile.MatchString(path.Base(file.Path)) && !file.Binary && file.SizeBytes <= maxDecisionBytes && len(records) < maxDecisions {
				records = append(records, file)
			}
		}
		if len(records) > 0 {
			return dir, records
		}
	}
	return "", nil
}

var (
	decisionTitleNumber = regexp.MustCompile(`^(?i)(adr[- ]?)?\d+[.:)]?\s+`)
	decisionField       = regexp.MustCompile(`^(?i)[-*]?\s*\**(status|date)\**\s*:\**\s*(.+)$`)
)

// parseDecision reads a record leniently: the first "# " line is its title
// (without a leading number), "Status:" and "Date:" lines or sections state
// those, and each "## " heading starts a section.
func parseDecision(file, text string) Decision {
	decision := Decision{Path: file}
	if match := decisionFile.FindStringSubmatch(path.Base(file)); match != nil {
		decision.Number, _ = strconv.Atoi(match[1])
	}
	var section *DecisionSection
	flush := func() {
		if section == nil {
			return
		}
		section.Text = textutil.Truncate(strings.TrimSpace(section.Text), maxDecisionSection)
		switch strings.ToLower(section.Heading) {
		case "status":
			decision.Status = firstDecisionLine(section.Text)
		case "date":
			decision.Date = firstDecisionLine(section.Text)
		default:
			if section.Text != "" {
				decision.Sections = append(decision.Sections, *section)
			}
		}
		section = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "## "):
			flush()
			section = &DecisionSection{Heading: strings.TrimSpace(trimmed[3:])}
		case strings.HasPrefix(trimmed, "# ") && decision.Title == "":
			decision.Title = strings.TrimSpace(decisionTitleNumber.ReplaceAllString(strings.TrimSpace(trimmed[2:]), ""))
		case section != nil:
			section.Text += line + "\n"
		default:
			if field := decisionField.FindStringSubmatch(trimmed); field != nil {
				setDecisionField(&decision, field[1], field[2])
			}
		}
	}
	flush()
	if decision.Title == "" {
		decision.Title = strings.TrimSuffix(path.Base(file), ".md")
	}
	decision.Status = strings.ToLower(textutil.Truncate(decision.Status, 80))
	decision.Date = textutil.Truncate(decision.Date, 32)
	return decision
}

func setDecisionField(decision *Decision, name, value string) {
	value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "*"))
	if strings.EqualFold(name, "status") {
		decision.Status = value
	} else {
		decision.Date = value
	}
}

func firstDecisionLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

// decisionsBrief tells an investigation which design decisions the project
// has recorded, so its plan reads the ones that bear on the task and says so
// when it goes against one. Superseded records are left out. Empty when the
// project has none.
func (s *Service) decisionsBrief(ctx context.Context) string {
	report, err := s.Decisions(ctx)
	if err != nil || len(report.Decisions) == 0 {
		return ""
	}
	var brief strings.Builder
	listed := 0
	for _, decision := range report.Decisions {
		if strings.HasPrefix(decision.Status, DecisionSuperseded) || listed == maxDecisionsInBrief {
			continue
		}
		status := decision.Status
		if status == "" {
			status = "no status"
		}
		fmt.Fprintf(&brief, "- %04d %s (%s): %s\n", decision.Number, decision.Title, status, decision.Path)
		listed++
	}
	if listed == 0 {
		return ""
	}
	return "\nDesign decisions recorded for this project. Read with read_file the ones that bear on this task; a plan that goes against an accepted decision says which and why in risks:\n" + brief.String()
}
