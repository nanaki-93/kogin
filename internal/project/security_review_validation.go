package project

import (
	"fmt"
	"net/url"
	"strings"
)

func validSecurityIndex(indexed IndexFile) bool {
	path, err := normalizedIndexedPath(indexed.Path)
	return err == nil && path == indexed.Path && indexed.ContentHash != "" && indexed.LineCount >= 0
}

func validSecurityFindingContent(f SecurityFinding) bool {
	return validSecurityIdentifier(f.Rule, 128) && validSecurityIdentifier(f.Category, 128) && validRequiredSecurityText(f.Title, maxSecurityTextBytes) && validRequiredSecurityText(f.ObservedCondition, maxSecurityTextBytes) && validRequiredSecurityText(f.Preconditions, maxSecurityTextBytes) && validRequiredSecurityText(f.Remediation, maxSecurityTextBytes) && validRequiredSecurityText(f.VerificationIdea, maxSecurityTextBytes) && validSecurityCWE(f.CWE) && validSecurityReference(f.Reference) && validSecurityTriage(f.Triage) && validSecurityVerification(f.VerificationState) && oneOf(f.Severity, "critical", "high", "medium", "low", "info") && oneOf(f.Confidence, "high", "medium", "low") && oneOf(f.EvidenceKind, "rule_match", "model_suspicion") && validStoredSecurityInsight(f.EngineeringInsight)
}

func validSecurityAnchor(anchor SecuritySourceAnchor, indexed IndexFile) bool {
	return validSecurityAnchorShape(anchor) && anchor.Path == indexed.Path && anchor.EndLine <= indexed.LineCount
}
func validSecurityAnchorShape(anchor SecuritySourceAnchor) bool {
	path, err := normalizedIndexedPath(anchor.Path)
	return err == nil && path == anchor.Path && anchor.StartLine >= 1 && anchor.EndLine >= anchor.StartLine
}
func validSecurityIdentifier(value string, limit int) bool {
	return value != "" && value == strings.ToLower(value) && validSecurityText(value, limit)
}
func validSecurityText(value string, limit int) bool {
	return value == strings.TrimSpace(value) && len(value) <= limit && redactSecurityText(value) == value
}
func validRequiredSecurityText(value string, limit int) bool {
	return value != "" && validSecurityText(value, limit)
}
func validSecurityCWE(value string) bool {
	return value == "" || len(value) <= 32 && securityCWE.MatchString(value)
}
func validSecurityReference(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return validSecurityText(value, 512) && err == nil && parsed.IsAbs() && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func validStoredSecurityInsight(insight *EngineeringInsight) bool {
	if insight == nil {
		return true
	}
	redacted := redactSecurityInsight(insight)
	if redacted == nil || *redacted != *insight {
		return false
	}
	canonical := *insight
	return ValidEngineeringInsight(&canonical) && canonical == *insight
}

func validSecurityInput(input SecurityReportInput) error {
	if !validSecurityInputIdentity(input) {
		return fmt.Errorf("security report identity is incomplete")
	}
	switch input.Source {
	case SecuritySourceAI:
		if !validAISecurityInput(input) {
			return fmt.Errorf("AI security report provenance is incomplete")
		}
	case SecuritySourceDeterministic:
		if !validDeterministicSecurityInput(input) {
			return fmt.Errorf("deterministic security report provenance is invalid")
		}
	default:
		return fmt.Errorf("security report source is invalid")
	}
	return nil
}

func validSecurityInputIdentity(input SecurityReportInput) bool {
	path, err := normalizedIndexedPath(input.Path)
	return err == nil &&
		path == input.Path &&
		validRequiredSecurityProvenance(input.ProjectID, 128) &&
		validRequiredSecurityProvenance(input.ProjectRevision, 128) &&
		validRequiredSecurityProvenance(input.ContentHash, 128) &&
		validRequiredSecurityProvenance(input.ContextPolicyVersion, 128)
}

func validAISecurityInput(input SecurityReportInput) bool {
	if input.RuleSetVersion != "" {
		return false
	}
	fields := []struct {
		value string
		limit int
	}{
		{input.Model, 512},
		{input.ConfiguredModel, 512},
		{input.Profile, 128},
		{input.Scope, 128},
		{input.ProviderOrigin, 512},
		{input.ReasoningEffort, 128},
		{input.PromptVersion, 128},
	}
	for _, field := range fields {
		if !validRequiredSecurityProvenance(field.value, field.limit) {
			return false
		}
	}
	return true
}

func validDeterministicSecurityInput(input SecurityReportInput) bool {
	return validRequiredSecurityProvenance(input.RuleSetVersion, 128) &&
		input.Model == "" &&
		input.ConfiguredModel == "" &&
		input.Profile == "" &&
		input.Scope == "" &&
		input.ProviderOrigin == "" &&
		input.ReasoningEffort == "" &&
		input.PromptVersion == ""
}
func validRequiredSecurityProvenance(value string, limit int) bool {
	return value != "" && validSecurityText(value, limit)
}

func validStoredSecurityReport(report SecurityFileReport, indexed *IndexFile) bool {
	input := SecurityReportInput{ProjectID: report.ProjectID, ProjectRevision: report.ProjectRevision, Path: report.Path, ContentHash: report.ContentHash, Source: report.Source, RuleSetVersion: report.RuleSetVersion, Model: report.Model, ConfiguredModel: report.ConfiguredModel, Profile: report.Profile, Scope: report.Scope, ProviderOrigin: report.ProviderOrigin, ReasoningEffort: report.ReasoningEffort, PromptVersion: report.PromptVersion, ContextPolicyVersion: report.ContextPolicyVersion}
	if !validSecurityReportHeader(report, input, indexed) || !validSecurityReportLifecycle(report) {
		return false
	}
	return validSecurityReportFindings(report, indexed)
}

func validSecurityReportHeader(report SecurityFileReport, input SecurityReportInput, indexed *IndexFile) bool {
	return report.SchemaVersion == securityReportSchemaVersion &&
		validSecurityInput(input) == nil &&
		report.Findings != nil &&
		!report.GeneratedAt.IsZero() &&
		validSecurityStatus(report.Status) &&
		validSecurityText(report.Reason, maxSecurityReasonBytes) &&
		len(report.Findings) <= maxSecurityFindings &&
		(indexed == nil || validSecurityIndex(*indexed) && report.Path == indexed.Path && report.ContentHash == indexed.ContentHash)
}

func validSecurityReportLifecycle(report SecurityFileReport) bool {
	switch report.Status {
	case SecurityStatusNotRun, SecurityStatusRunning:
		return len(report.Findings) == 0
	case SecurityStatusCompletedEmpty:
		return len(report.Findings) == 0 && report.Reason == ""
	case SecurityStatusCompleted:
		return len(report.Findings) > 0 && report.Reason == ""
	case SecurityStatusPartial:
		return len(report.Findings) > 0 && report.Reason != ""
	case SecurityStatusFailed, SecurityStatusCanceled:
		return len(report.Findings) == 0 && report.Reason != ""
	case SecurityStatusStale:
		return true
	default:
		return false
	}
}

func validSecurityReportFindings(report SecurityFileReport, indexed *IndexFile) bool {
	seen := make(map[string]struct{}, len(report.Findings))
	for _, finding := range report.Findings {
		if finding.Anchor.Path != report.Path || !validStoredSecurityFinding(finding, indexed) {
			return false
		}
		if !validSecurityEvidenceSource(report.Source, finding.EvidenceKind) {
			return false
		}
		if _, duplicate := seen[finding.ID]; duplicate {
			return false
		}
		seen[finding.ID] = struct{}{}
	}
	return true
}

func validSecurityEvidenceSource(source, evidenceKind string) bool {
	return source == SecuritySourceAI && evidenceKind == "model_suspicion" ||
		source == SecuritySourceDeterministic && evidenceKind == "rule_match"
}
func validStoredSecurityFinding(f SecurityFinding, indexed *IndexFile) bool {
	return f.ID != "" && f.ID == SecurityFindingID(f) && validSecurityFindingContent(f) && validSecurityAnchorShape(f.Anchor) && (indexed == nil || validSecurityAnchor(f.Anchor, *indexed))
}
func validSecurityStatus(value string) bool {
	return oneOf(value, SecurityStatusNotRun, SecurityStatusRunning, SecurityStatusCompleted, SecurityStatusCompletedEmpty, SecurityStatusPartial, SecurityStatusStale, SecurityStatusFailed, SecurityStatusCanceled)
}
func validSecurityTriage(value string) bool {
	return oneOf(value, SecurityTriageOpen, SecurityTriageDismissed, SecurityTriageAccepted)
}
func validSecurityVerification(value string) bool {
	return oneOf(value, SecurityVerificationUnverified, SecurityVerificationVerified, SecurityVerificationNotReproducible)
}
func securityReportMatchesInput(report SecurityFileReport, input SecurityReportInput) bool {
	// The provider may resolve the selected model to a different reported name.
	// ConfiguredModel pins the request; Model remains response provenance.
	return report.ProjectID == input.ProjectID && report.Path == input.Path && report.ContentHash == input.ContentHash && report.Source == input.Source && report.RuleSetVersion == input.RuleSetVersion && report.ConfiguredModel == input.ConfiguredModel && report.Profile == input.Profile && report.Scope == input.Scope && report.ProviderOrigin == input.ProviderOrigin && report.ReasoningEffort == input.ReasoningEffort && report.PromptVersion == input.PromptVersion && report.ContextPolicyVersion == input.ContextPolicyVersion
}
