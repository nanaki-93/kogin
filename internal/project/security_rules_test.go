package project

import (
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/project/symbols"
)

func TestScanSecurityRulesRejectsUnsupportedAndStaleInput(t *testing.T) {
	source := "package fixture\nfunc Run() {}\n"
	indexed := securityRuleIndexedFile("notes.txt", source)
	if _, err := ScanSecurityRules(indexed, source); err != ErrSecurityRulesUnavailable {
		t.Fatalf("unsupported language = %v", err)
	}
	indexed = securityRuleIndexedFile("main.go", source)
	if _, err := ScanSecurityRules(indexed, source+"// changed\n"); err != ErrRevisionConflict {
		t.Fatalf("stale source = %v", err)
	}
}

func securityRuleIndexedFile(path, source string) IndexFile {
	imports, _ := symbols.ExtractGoFacts(path, []byte(source))
	return IndexFile{Path: path, ContentHash: contentHash([]byte(source)), Language: "Go", LineCount: strings.Count(source, "\n"), Imports: imports}
}
