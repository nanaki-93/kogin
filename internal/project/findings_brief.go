package project

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/textutil"
)

// maxBriefLine bounds each line of an issue's brief, in characters.
const maxBriefLine = 200

// FindingBrief is an issue in four short lines: what is wrong, what it
// costs, the change that fixes it and how to see that it is fixed. It is
// read from the finding's message, labeled evidence, own insight and task
// specification, so findings stored before it existed have one too.
type FindingBrief struct {
	Problem string `json:"problem"`
	Impact  string `json:"impact,omitempty"`
	Fix     string `json:"fix,omitempty"`
	Verify  string `json:"verify,omitempty"`
}

// Labels of the evidence lines a brief is read from; file analysis writes
// Impact and Fix, Security and Performance the others.
var (
	briefImpactLabels = []string{"Impact", "Workload", "Preconditions"}
	briefFixLabels    = []string{"Fix", "Remediation", "Recommendation"}
	briefVerifyLabels = []string{"How to verify"}
)

// BriefOf reads a finding's brief.
func BriefOf(finding UnifiedFinding) FindingBrief {
	labeled := evidenceLabels(finding.Evidence)
	own := ownInsight(finding)
	brief := FindingBrief{Problem: briefLine(finding.Message)}
	brief.Impact = briefLine(firstLabeled(labeled, briefImpactLabels))
	if brief.Impact == "" && own != nil {
		brief.Impact = briefLine(firstNonEmpty(own.WhyItMattersHere, own.TradeoffOrFailureMode))
	}
	brief.Fix = briefLine(firstLabeled(labeled, briefFixLabels))
	if brief.Fix == "" && own != nil {
		brief.Fix = briefLine(own.TransferableLesson)
	}
	if brief.Fix == "" && finding.TaskSpec != nil && len(finding.TaskSpec.AcceptanceCriteria) > 0 {
		brief.Fix = briefLine(finding.TaskSpec.AcceptanceCriteria[0])
	}
	brief.Verify = briefLine(firstLabeled(labeled, briefVerifyLabels))
	if brief.Verify == "" && finding.TaskSpec != nil && finding.TaskSpec.TestCandidate != nil {
		candidate := finding.TaskSpec.TestCandidate
		brief.Verify = fmt.Sprintf("`%s` in %s fails before the fix and passes after it.", candidate.Name, candidate.Path)
	}
	if brief.Impact == brief.Problem {
		brief.Impact = ""
	}
	return brief
}

// evidenceLabels splits "Label: text" lines; nil when any line is not one.
func evidenceLabels(evidence string) map[string]string {
	labeled := map[string]string{}
	for _, line := range strings.Split(evidence, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		label, text, found := strings.Cut(line, ": ")
		if !found || !knownEvidenceLabel(label) {
			return nil
		}
		labeled[label] = text
	}
	return labeled
}

func knownEvidenceLabel(label string) bool {
	switch label {
	case "Impact", "Fix", "Preconditions", "Remediation", "How to verify", "Reference", "Workload", "Recommendation", "Tradeoff":
		return true
	}
	return false
}

func firstLabeled(labeled map[string]string, labels []string) string {
	for _, label := range labels {
		if text := strings.TrimSpace(labeled[label]); text != "" {
			return text
		}
	}
	return ""
}

// ownInsight is the finding's insight when it was written for this finding:
// an older file analysis lent the file's general insight to risks without
// one, and that one explains something else.
func ownInsight(finding UnifiedFinding) *EngineeringInsight {
	insight := finding.EngineeringInsight
	if insight == nil || briefLine(insight.Mechanism) != briefLine(finding.Message) {
		return nil
	}
	return insight
}

func briefLine(text string) string {
	return textutil.FirstSentence(text, maxBriefLine)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// EvidenceIsLabeled reports whether evidence is only labeled lines, which
// the brief already shows; other evidence is a tool's output.
func EvidenceIsLabeled(evidence string) bool {
	return evidenceLabels(evidence) != nil
}
