package handbook

import (
	"path/filepath"
	"strings"
)

func parseReportComponent(raw string) ProjectDocsComponent {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ProjectDocsComponent{}
	}
	pathPart, rest := splitComponentHeader(trimmed)
	name, cleanPath := deriveComponentIdentity(pathPart)
	layer := classifyComponentLayer(cleanPath, name)
	summary := rest
	if summary == "" {
		summary = "No responsibility explanation was supplied."
	}
	summary = "Model interpretation: " + summary
	return ProjectDocsComponent{
		Name:             name,
		Layer:            layer,
		Path:             cleanPath,
		Summary:          summary,
		Responsibilities: []string{summary},
		Contracts:        []ProjectDocsContract{},
		Dependencies:     []string{},
	}
}

func splitComponentHeader(trimmed string) (string, string) {
	colonIdx := strings.Index(trimmed, ":")
	if colonIdx <= 0 {
		return trimmed, ""
	}
	return strings.TrimSpace(trimmed[:colonIdx]), strings.TrimSpace(trimmed[colonIdx+1:])
}

func deriveComponentIdentity(header string) (string, string) {
	if openIdx := strings.Index(header, "("); openIdx > 0 && strings.HasSuffix(header, ")") {
		cleanPath := strings.TrimSpace(header[:openIdx])
		label := strings.TrimSpace(header[openIdx+1 : len(header)-1])
		if label != "" {
			return label, cleanPath
		}
	}
	cleanPath := strings.Trim(header, "` ")
	base := filepath.Base(cleanPath)
	if base == "." || base == "/" || base == "" {
		base = cleanPath
	}
	return humanizeComponentName(base), cleanPath
}

func humanizeComponentName(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.'
	})
	if len(parts) == 0 {
		return "Core Subsystem"
	}
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func classifyComponentLayer(path, name string) string {
	lower := strings.ToLower(path + " " + name)
	switch {
	case containsAnyToken(lower, "desktop", "frontend", "ui", "view", "web", "client"):
		return "Presentation Layer"
	case containsAnyToken(lower, "api", "handler", "route", "http", "transport", "cmd", "daemon", "cli"):
		return "API / Transport Boundary"
	case containsAnyToken(lower, "app", "service", "workflow", "orchestrat", "job", "usecase"):
		return "Application / Orchestration"
	case containsAnyToken(lower, "script", "test", "eval", "bench", "ci", "quality"):
		return "Tooling & Verification"
	default:
		return "Domain & Persistence"
	}
}

func containsAnyToken(haystack string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			return true
		}
	}
	return false
}
