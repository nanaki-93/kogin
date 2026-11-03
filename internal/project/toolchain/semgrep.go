package toolchain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// semgrepConfigs are where a project keeps its own semgrep rules.
var semgrepConfigs = []string{".semgrep.yml", ".semgrep.yaml", ".semgrep"}

// SemgrepConfig names the project's own semgrep rules under root: a
// .semgrep.yml or .semgrep.yaml file or a .semgrep directory; empty when it
// has none. Kogin never downloads rules from the registry.
func SemgrepConfig(root string) string {
	for _, name := range semgrepConfigs {
		info, err := os.Lstat(filepath.Join(root, name))
		if err == nil && (info.Mode().IsRegular() || info.IsDir() && name == ".semgrep") {
			return name
		}
	}
	return ""
}

// SemgrepCommand scans the project with its own rules, printing JSON, with
// metrics and the version check off.
func SemgrepCommand(config string) []string {
	return []string{"semgrep", "scan", "--config", config, "--json", "--metrics=off", "--disable-version-check", "--quiet"}
}

// SemgrepResult is one match of a semgrep rule.
type SemgrepResult struct {
	Rule      string
	Path      string
	StartLine int
	EndLine   int
	Message   string
	// Severity is high, medium or low.
	Severity string
	// Security reports a rule its metadata files under security or a CWE.
	Security bool
	CWE      string
}

type semgrepOutput struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
		Extra struct {
			Message  string          `json:"message"`
			Severity string          `json:"severity"`
			Metadata semgrepMetadata `json:"metadata"`
		} `json:"extra"`
	} `json:"results"`
}

type semgrepMetadata struct {
	Category string `json:"category"`
	CWE      any    `json:"cwe"`
}

// ParseSemgrep reads semgrep's JSON output.
func ParseSemgrep(output []byte) ([]SemgrepResult, error) {
	var parsed semgrepOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		return nil, fmt.Errorf("read semgrep output: %w", err)
	}
	results := make([]SemgrepResult, 0, len(parsed.Results))
	for _, item := range parsed.Results {
		cwe := firstCWE(item.Extra.Metadata.CWE)
		results = append(results, SemgrepResult{
			Rule: item.CheckID, Path: strings.TrimPrefix(filepath.ToSlash(item.Path), "./"),
			StartLine: item.Start.Line, EndLine: max(item.End.Line, item.Start.Line),
			Message:  strings.Join(strings.Fields(item.Extra.Message), " "),
			Severity: semgrepSeverity(item.Extra.Severity),
			Security: strings.EqualFold(item.Extra.Metadata.Category, "security") || cwe != "", CWE: cwe,
		})
	}
	return results, nil
}

func semgrepSeverity(value string) string {
	switch strings.ToUpper(value) {
	case "ERROR", "HIGH", "CRITICAL":
		return "high"
	case "WARNING", "MEDIUM":
		return "medium"
	default:
		return "low"
	}
}

// firstCWE reads "CWE-89: ..." from a string or the first of a list.
func firstCWE(value any) string {
	if list, ok := value.([]any); ok && len(list) > 0 {
		value = list[0]
	}
	text, _ := value.(string)
	if id, _, _ := strings.Cut(text, ":"); strings.HasPrefix(id, "CWE-") {
		return strings.TrimSpace(id)
	}
	return ""
}
