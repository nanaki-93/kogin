package project

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Model suggestions about one file come from several passes (the file's own
// analysis, Security, Performance), and each run words them afresh. Two
// rules keep the same issue from counting twice:
//
//   - Reports of one issue by different passes become one finding: the most
//     severe one, which lists the others under also_reported.
//   - A new finding that repeats one the user (or a task) dismissed in the
//     same file is recorded dismissed too, naming the one it repeats in
//     seen_before, so it does not open again as new.
//
// An issue is the same when its file matches and its words (title and
// message) overlap enough. Findings with a triage of their own are never
// merged away.

// FindingReport is another pass's report of an issue a finding merges.
type FindingReport struct {
	Analysis string `json:"analysis"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Line     int    `json:"line,omitempty"`
}

// FindingRef names an earlier finding: the dismissed one a new finding repeats.
type FindingRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

const (
	// sameIssueAcrossPasses is the word overlap at which two passes'
	// reports in one file are taken as one issue.
	sameIssueAcrossPasses = 0.2
	// sameIssueAsDismissed is the overlap at which a new finding repeats a
	// dismissed one; it is higher, since a match hides the finding.
	sameIssueAsDismissed = 0.25
	maxAlsoReported      = 4
)

var (
	findingWordPattern = regexp.MustCompile(`[a-z0-9]+`)
	findingStopWords   = map[string]bool{}
)

func init() {
	for _, word := range strings.Fields("the and for not are but can its this that with when then than from into only does was were has have had which while will would could should may might any all also each such there their they them") {
		findingStopWords[word] = true
	}
}

// findingWords are the words of a finding's title and message, cut to their
// first six letters so "leaks" and "leaked" match.
func findingWords(finding UnifiedFinding) map[string]bool {
	words := map[string]bool{}
	for _, word := range findingWordPattern.FindAllString(strings.ToLower(finding.Title+" "+finding.Message), -1) {
		if len(word) < 3 || findingStopWords[word] {
			continue
		}
		if len(word) > 6 {
			word = word[:6]
		}
		words[word] = true
	}
	return words
}

// findingOverlap is the share of words two findings have in common.
func findingOverlap(a, b map[string]bool) float64 {
	common := 0
	for word := range a {
		if b[word] {
			common++
		}
	}
	union := len(a) + len(b) - common
	if union == 0 {
		return 0
	}
	return float64(common) / float64(union)
}

// findingSeverityRank orders severities, most severe first.
func findingSeverityRank(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	}
	return 4
}

// mergeAcrossPasses folds reports of one issue by different passes into the
// most severe of them. protected findings (with a triage of their own) are
// kept as they are.
func mergeAcrossPasses(reported []UnifiedFinding, protected func(string) bool) []UnifiedFinding {
	order := slices.Clone(reported)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if rank := findingSeverityRank(a.Severity) - findingSeverityRank(b.Severity); rank != 0 {
			return rank < 0
		}
		return a.ID < b.ID
	})
	words := make(map[string]map[string]bool, len(order))
	var kept []UnifiedFinding
	for _, finding := range order {
		words[finding.ID] = findingWords(finding)
		if index := sameIssueAs(kept, finding, words, protected); index >= 0 {
			kept[index].AlsoReported = append(kept[index].AlsoReported, FindingReport{Analysis: finding.OriginatingAnalysis, Title: finding.Title, Severity: finding.Severity, Line: finding.Location.StartLine})
			continue
		}
		kept = append(kept, finding)
	}
	return kept
}

// sameIssueAs is the kept finding another pass's finding repeats, or -1.
func sameIssueAs(kept []UnifiedFinding, finding UnifiedFinding, words map[string]map[string]bool, protected func(string) bool) int {
	if finding.Location.Path == "" || finding.OriginatingAnalysis == "" || protected(finding.ID) {
		return -1
	}
	for index, primary := range kept {
		if primary.Location.Path != finding.Location.Path || primary.OriginatingAnalysis == finding.OriginatingAnalysis || len(primary.AlsoReported) >= maxAlsoReported {
			continue
		}
		if findingOverlap(words[primary.ID], words[finding.ID]) >= sameIssueAcrossPasses {
			return index
		}
	}
	return -1
}

// repeatsDismissed is the dismissed finding of the same file a new finding
// repeats, if any.
func repeatsDismissed(finding UnifiedFinding, dismissed []UnifiedFinding) *FindingRef {
	if finding.Location.Path == "" {
		return nil
	}
	words := findingWords(finding)
	var match *FindingRef
	best := sameIssueAsDismissed
	for _, earlier := range dismissed {
		if earlier.Location.Path != finding.Location.Path || earlier.ID == finding.ID {
			continue
		}
		if overlap := findingOverlap(words, findingWords(earlier)); overlap >= best {
			best, match = overlap, &FindingRef{ID: earlier.ID, Title: earlier.Title}
		}
	}
	return match
}
