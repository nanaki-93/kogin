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
	return nil
}
