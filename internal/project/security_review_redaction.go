package project

import (
	"fmt"
	"strings"
	"unicode"
)

func redactSecurityText(value string) string {
	value = securityAuthorization.ReplaceAllString(value, "${1}Authorization: [redacted]")
	return securityKeyValueSecret.ReplaceAllString(value, "${1}[redacted]")
}
func redactSecurityInsight(insight *EngineeringInsight) *EngineeringInsight {
	if insight == nil {
		return nil
	}
	copy := *insight
	copy.Mechanism, copy.WhyItMattersHere = redactSecurityText(copy.Mechanism), redactSecurityText(copy.WhyItMattersHere)
	copy.TradeoffOrFailureMode, copy.TransferableLesson = redactSecurityText(copy.TradeoffOrFailureMode), redactSecurityText(copy.TransferableLesson)
	return &copy
}

func redactSecurityInput(input *SecurityReportInput) {
	input.Model, input.ConfiguredModel = redactSecurityText(input.Model), redactSecurityText(input.ConfiguredModel)
	input.Profile, input.Scope = redactSecurityText(input.Profile), redactSecurityText(input.Scope)
	input.ProviderOrigin, input.ReasoningEffort = redactSecurityText(input.ProviderOrigin), redactSecurityText(input.ReasoningEffort)
	input.PromptVersion, input.ContextPolicyVersion = redactSecurityText(input.PromptVersion), redactSecurityText(input.ContextPolicyVersion)
}
func redactSecurityReport(report *SecurityFileReport) {
	report.Reason, report.Model, report.ConfiguredModel = redactSecurityText(report.Reason), redactSecurityText(report.Model), redactSecurityText(report.ConfiguredModel)
	report.Profile, report.Scope, report.ProviderOrigin = redactSecurityText(report.Profile), redactSecurityText(report.Scope), redactSecurityText(report.ProviderOrigin)
	report.ReasoningEffort, report.PromptVersion, report.ContextPolicyVersion = redactSecurityText(report.ReasoningEffort), redactSecurityText(report.PromptVersion), redactSecurityText(report.ContextPolicyVersion)
	for index := range report.Findings {
		f := &report.Findings[index]
		f.Title, f.ObservedCondition, f.Preconditions = redactSecurityText(f.Title), redactSecurityText(f.ObservedCondition), redactSecurityText(f.Preconditions)
		f.Remediation, f.VerificationIdea, f.Reference = redactSecurityText(f.Remediation), redactSecurityText(f.VerificationIdea), redactSecurityText(f.Reference)
		f.EngineeringInsight = redactSecurityInsight(f.EngineeringInsight)
	}
}

// SanitizeSecurityFileReport returns a source-free report with every prose and
// provenance field redacted using the same rules enforced by cache storage.
func SanitizeSecurityFileReport(report SecurityFileReport) SecurityFileReport {
	report = cloneSecurityFileReportValue(report)
	redactSecurityReport(&report)
	return report
}

// ValidateSecurityFileReportSourceFree rejects report prose that repeats a
// meaningful source-line token sequence. Redaction removes credentials; this
// guard keeps formatting variations of source out of durable and returned
// review evidence.
func ValidateSecurityFileReportSourceFree(report SecurityFileReport, source string) error {
	reportSignatures := make([]string, 0, len(report.Findings)*9+9)
	for _, text := range securityReportText(report) {
		if signature, _, _ := securityEchoSignature(text); signature != "" {
			reportSignatures = append(reportSignatures, signature)
		}
	}
	for _, line := range strings.Split(source, "\n") {
		signature, words, wordCharacters := securityEchoSignature(line)
		if !meaningfulSecurityEcho(words, wordCharacters) {
			continue
		}
		for _, reportSignature := range reportSignatures {
			if strings.Contains(reportSignature, signature) {
				return fmt.Errorf("security report repeats source text")
			}
		}
	}
	return nil
}

func securityEchoSignature(value string) (string, int, int) {
	words := make([]string, 0, 8)
	var word strings.Builder
	wordCharacters := 0
	flush := func() {
		if word.Len() == 0 {
			return
		}
		words = append(words, word.String())
		word.Reset()
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			word.WriteRune(character)
			wordCharacters++
			continue
		}
		flush()
	}
	flush()
	if len(words) == 0 {
		return "", 0, 0
	}
	return "\x00" + strings.Join(words, "\x00") + "\x00", len(words), wordCharacters
}

func meaningfulSecurityEcho(words, wordCharacters int) bool {
	return wordCharacters >= minSecurityEchoWordCharacters &&
		(words > 1 || wordCharacters >= minSecurityEchoSingleWordCharacters)
}

func securityReportText(report SecurityFileReport) []string {
	texts := []string{report.Reason, report.Model, report.ConfiguredModel, report.Profile, report.Scope, report.ProviderOrigin, report.ReasoningEffort, report.PromptVersion, report.ContextPolicyVersion}
	for _, finding := range report.Findings {
		texts = append(texts, finding.Rule, finding.Category, finding.Title, finding.ObservedCondition, finding.Preconditions, finding.Remediation, finding.VerificationIdea, finding.CWE, finding.Reference)
		if finding.EngineeringInsight != nil {
			texts = append(texts, finding.EngineeringInsight.Mechanism, finding.EngineeringInsight.WhyItMattersHere, finding.EngineeringInsight.TradeoffOrFailureMode, finding.EngineeringInsight.TransferableLesson)
		}
	}
	return texts
}
