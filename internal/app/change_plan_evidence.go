package app

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PlanEvidence contains an exact source quotation and a model interpretation.
// Valid provenance does not make the interpretation a verified finding.
type PlanEvidence struct {
	Path        string `json:"path"`
	Hash        string `json:"hash"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Quote       string `json:"quote"`
	Observation string `json:"observation"`
	// Step is the approach step (from 1) that changes the cited lines, and
	// Effect what it does to them: removed or changed. Zero and empty for
	// lines that only show the cause.
	Step   int    `json:"step,omitempty"`
	Effect string `json:"effect,omitempty"`
	// Excerpt is the cited lines as the file holds them, from ExcerptLine
	// (at most maxPlanExcerptLines), so the plan shows code beside its
	// prose. Empty for a plan captured before excerpts.
	Excerpt     string `json:"excerpt,omitempty"`
	ExcerptLine int    `json:"excerpt_line,omitempty"`
}

// What a plan step does to the lines a citation shows.
const (
	planEffectRemoved = "removed"
	planEffectChanged = "changed"
)

const (
	maxPlanExcerptLines = 8
	maxPlanExcerptBytes = 1600
	maxPlanSteps        = 3
)

// planExcerpt is the part of the cited lines a plan shows: all of a short
// citation, else the lines the quote starts on. It is empty when those
// lines are too long to show.
func planExcerpt(lines []string, start, end int, quote string) (string, int) {
	from, to := start, end
	if to-from+1 > maxPlanExcerptLines {
		first, _, _ := strings.Cut(quote, "\n")
		for line := start; line <= end; line++ {
			if strings.Contains(lines[line-1], first) {
				from = line
				break
			}
		}
		to = min(end, from+maxPlanExcerptLines-1)
	}
	excerpt := strings.Join(lines[from-1:to], "\n")
	if len(excerpt) > maxPlanExcerptBytes || !utf8.ValidString(excerpt) || strings.ContainsRune(excerpt, 0) {
		return "", 0
	}
	return excerpt, from
}

func validInvestigationText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validatePlanCitation(citation PlanEvidence, source string) error {
	if source == "" || citation.Hash != contentHash([]byte(source)) || !validInvestigationText(citation.Quote, 1024) || !validInvestigationText(citation.Observation, 512) {
		return fmt.Errorf("planning citation lacks valid captured provenance")
	}
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	if citation.StartLine < 1 || citation.EndLine < citation.StartLine || citation.EndLine > len(lines) {
		return fmt.Errorf("planning citation is outside the captured source")
	}
	if !strings.Contains(strings.Join(lines[citation.StartLine-1:citation.EndLine], "\n"), citation.Quote) {
		return fmt.Errorf("planning citation quote does not match captured source")
	}
	if citation.Step < 0 || citation.Step > maxPlanSteps || (citation.Effect != "" && (citation.Step == 0 || citation.Effect != planEffectRemoved && citation.Effect != planEffectChanged)) {
		return fmt.Errorf("planning citation names an approach step it does not have, or an unknown effect")
	}
	return validatePlanExcerpt(citation, lines)
}

// validatePlanExcerpt checks that a citation's excerpt is the source's own
// lines within the cited range.
func validatePlanExcerpt(citation PlanEvidence, lines []string) error {
	if citation.Excerpt == "" && citation.ExcerptLine == 0 {
		return nil
	}
	shown := strings.Split(citation.Excerpt, "\n")
	last := citation.ExcerptLine + len(shown) - 1
	if citation.ExcerptLine < citation.StartLine || last > citation.EndLine || len(shown) > maxPlanExcerptLines || len(citation.Excerpt) > maxPlanExcerptBytes {
		return fmt.Errorf("planning citation excerpt is outside its cited lines")
	}
	if strings.Join(lines[citation.ExcerptLine-1:last], "\n") != citation.Excerpt {
		return fmt.Errorf("planning citation excerpt does not match captured source")
	}
	return nil
}
