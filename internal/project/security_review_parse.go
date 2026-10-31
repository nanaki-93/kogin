package project

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nanaki-93/mini-orca/v2/internal/project/insight"
)

type securityWire struct {
	Findings []securityFindingWire `json:"findings"`
}
type securityFindingWire struct {
	Rule              string               `json:"rule"`
	Category          string               `json:"category"`
	Title             string               `json:"title"`
	Anchor            SecuritySourceAnchor `json:"source_anchor"`
	Severity          string               `json:"severity"`
	Confidence        string               `json:"confidence"`
	EvidenceKind      string               `json:"evidence_kind"`
	ObservedCondition string               `json:"observed_condition"`
	Preconditions     string               `json:"preconditions_or_unknowns"`
	Remediation       string               `json:"remediation"`
	VerificationIdea  string               `json:"verification_idea"`
	CWE               string               `json:"cwe"`
	Reference         string               `json:"reference"`
	Insight           json.RawMessage      `json:"engineering_insight"`
}

// ParseSecurityFindings accepts an empty section or one bounded findings object.
// Invalid non-empty results are rejected as a whole.
func ParseSecurityFindings(output string, indexed IndexFile, source string) ([]SecurityFinding, error) {
	if !validSecurityReviewInput(output, indexed, source) {
		return nil, fmt.Errorf("security review input is invalid or too large")
	}
	wire, err := decodeSecurityWire(insight.NormalizeEmptyFindingsOutput(output))
	if err != nil {
		return nil, err
	}
	if wire.Findings == nil || len(wire.Findings) > maxSecurityFindings || securitySourceLineCount(source) != indexed.LineCount {
		return nil, fmt.Errorf("security review findings or indexed facts are invalid")
	}
	return securityFindingsFromWire(wire.Findings, indexed)
}

func securitySourceLineCount(source string) int {
	if source == "" {
		return 0
	}
	lines := strings.Count(source, "\n")
	if !strings.HasSuffix(source, "\n") {
		lines++
	}
	return lines
}

func validSecurityReviewInput(output string, indexed IndexFile, source string) bool {
	return len(output) <= maxSecurityOutputBytes &&
		validSecurityIndex(indexed) &&
		len(source) <= SecurityMaxSourceBytes &&
		contentHash([]byte(source)) == indexed.ContentHash
}

func decodeSecurityWire(output string) (securityWire, error) {
	var wire securityWire
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return securityWire{}, fmt.Errorf("parse security review JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return securityWire{}, fmt.Errorf("security review JSON must contain one object")
	}
	return wire, nil
}

func securityFindingsFromWire(items []securityFindingWire, indexed IndexFile) ([]SecurityFinding, error) {
	findings := make([]SecurityFinding, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validSecurityWireLengths(item) {
			return nil, fmt.Errorf("security review contains an overlong finding")
		}
		finding := SecurityFinding{Rule: item.Rule, Category: item.Category, Title: redactSecurityText(item.Title), Anchor: item.Anchor, Severity: item.Severity, Confidence: item.Confidence, EvidenceKind: item.EvidenceKind, ObservedCondition: redactSecurityText(item.ObservedCondition), Preconditions: redactSecurityText(item.Preconditions), Remediation: redactSecurityText(item.Remediation), VerificationIdea: redactSecurityText(item.VerificationIdea), CWE: item.CWE, Reference: redactSecurityText(item.Reference), Triage: SecurityTriageOpen, VerificationState: SecurityVerificationUnverified}
		finding.EngineeringInsight, _ = ParseOptionalEngineeringInsight(item.Insight)
		finding.EngineeringInsight = redactSecurityInsight(finding.EngineeringInsight)
		if !validSecurityFindingContent(finding) || finding.EvidenceKind != "model_suspicion" || !validSecurityAnchor(finding.Anchor, indexed) {
			return nil, fmt.Errorf("security review contains an invalid finding")
		}
		finding.ID = SecurityFindingID(finding)
		if _, duplicate := seen[finding.ID]; duplicate {
			return nil, fmt.Errorf("security review contains duplicate findings")
		}
		seen[finding.ID] = struct{}{}
		findings = append(findings, finding)
	}
	return findings, nil
}

func validSecurityWireLengths(item securityFindingWire) bool {
	values := []struct {
		value string
		limit int
	}{{item.Rule, 128}, {item.Category, 128}, {item.Title, maxSecurityTextBytes}, {item.ObservedCondition, maxSecurityTextBytes}, {item.Preconditions, maxSecurityTextBytes}, {item.Remediation, maxSecurityTextBytes}, {item.VerificationIdea, maxSecurityTextBytes}, {item.CWE, 32}, {item.Reference, 512}, {item.Anchor.Path, 1024}}
	for _, value := range values {
		if len(value.value) > value.limit {
			return false
		}
	}
	return true
}
