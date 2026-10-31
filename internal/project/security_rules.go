package project

import (
	"github.com/nanaki-93/mini-orca/v2/internal/project/securityrules"
)

const SecurityRuleSetVersion = securityrules.SecurityRuleSetVersion

type SecurityRuleScanResult = securityrules.SecurityRuleScanResult

// SecurityRulesCover reports that a deterministic rule pack reads the file.
func SecurityRulesCover(path string) bool {
	return securityrules.Covers(path)
}

// ScanSecurityRules applies the source-only rule pack of the file's language.
// It does not resolve packages or run code.
func ScanSecurityRules(indexed IndexFile, source string) (SecurityRuleScanResult, error) {
	if !validSecurityIndex(indexed) || indexed.Binary || !securityrules.Covers(indexed.Path) {
		return SecurityRuleScanResult{}, ErrSecurityRulesUnavailable
	}
	if len(source) > SecurityMaxSourceBytes || contentHash([]byte(source)) != indexed.ContentHash {
		return SecurityRuleScanResult{}, ErrRevisionConflict
	}
	return securityrules.ScanFile(indexed, source)
}
