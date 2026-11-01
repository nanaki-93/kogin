package securityrules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/insight"
	"github.com/nanaki-93/kasuri/v2/internal/project/symbols"
)

const (
	MaxSecurityFindings            = 5
	maxSecurityFindings            = MaxSecurityFindings
	SecurityTriageOpen             = "open"
	SecurityVerificationUnverified = "unverified"
)

type IndexFile = symbols.IndexFile
type EngineeringInsight = insight.EngineeringInsight

// SecuritySourceAnchor identifies the source file and line span of a security finding.
type SecuritySourceAnchor struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// SecurityFinding keeps the user's triage separate from verification evidence.
type SecurityFinding struct {
	ID                 string               `json:"id"`
	Rule               string               `json:"rule"`
	Category           string               `json:"category"`
	Title              string               `json:"title"`
	Anchor             SecuritySourceAnchor `json:"source_anchor"`
	Severity           string               `json:"severity"`
	Confidence         string               `json:"confidence"`
	EvidenceKind       string               `json:"evidence_kind"`
	ObservedCondition  string               `json:"observed_condition"`
	Preconditions      string               `json:"preconditions_or_unknowns"`
	Remediation        string               `json:"remediation"`
	VerificationIdea   string               `json:"verification_idea"`
	CWE                string               `json:"cwe,omitempty"`
	Reference          string               `json:"reference,omitempty"`
	Triage             string               `json:"triage"`
	VerificationState  string               `json:"verification_state"`
	EngineeringInsight *EngineeringInsight  `json:"engineering_insight,omitempty"`
}

// SecurityFindingID excludes model wording, insights, CWE, and references.
func SecurityFindingID(f SecurityFinding) string {
	parts := []string{f.Rule, f.Category, f.Anchor.Path, fmt.Sprint(f.Anchor.StartLine), fmt.Sprint(f.Anchor.EndLine), f.EvidenceKind}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "security:" + hex.EncodeToString(sum[:])
}

// SecurityRuleScanResult is the bounded deterministic output for one file.
type SecurityRuleScanResult struct {
	Findings  []SecurityFinding
	Truncated bool
}

type securityRuleCandidate struct {
	position token.Pos
	finding  SecurityFinding
}

// scanGoCandidates parses Go source and evaluates the AST rules, which
// resolve imports and shadowing that a tree-sitter query cannot.
func scanGoCandidates(indexed IndexFile, source string) ([]ruleCandidate, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, indexed.Path, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse Go security rules: %w", err)
	}
	visitor := newSecurityRuleVisitor(fset, indexed)
	visitor.walkFile(file)
	sort.SliceStable(visitor.candidates, func(i, j int) bool { return visitor.candidates[i].position < visitor.candidates[j].position })
	candidates := make([]ruleCandidate, 0, len(visitor.candidates))
	for _, candidate := range visitor.candidates {
		candidates = append(candidates, ruleCandidate{line: candidate.finding.Anchor.StartLine, finding: candidate.finding})
	}
	return candidates, nil
}
