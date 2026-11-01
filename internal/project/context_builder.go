package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/project/symbols"
	"github.com/nanaki-93/kasuri/v2/internal/textutil"
)

const (
	maxContextBytes           = 512 * 1024
	maxSnippetBytes           = 24 * 1024
	maxTargetBytes            = 192 * 1024
	maxContextTokens          = 12000
	maxContextInventoryBytes  = maxContextBytes / 4
	maxContextInventoryTokens = maxContextTokens / 4
)

// ContextBuilder creates bounded project-wide context for analysis and generation.
type ContextBuilder struct{}

// ContextManifest describes the prompt material without returning source text.
type ContextManifest struct {
	Included        []ContextFile     `json:"included"`
	Excluded        []ContextDecision `json:"excluded"`
	EstimatedTokens int               `json:"estimated_tokens"`
	ByteLimit       int               `json:"byte_limit"`
	TokenLimit      int               `json:"token_limit"`
	Truncated       bool              `json:"truncated"`
	Scope           string            `json:"scope,omitempty"`
	Model           string            `json:"model,omitempty"`
	ProviderOrigin  string            `json:"provider_origin,omitempty"`
	RemoteProvider  bool              `json:"remote_provider,omitempty"`
}

type ContextFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Hash      string `json:"hash"`
	Tokens    int    `json:"estimated_tokens"`
	Truncated bool   `json:"truncated"`
}

func NewContextBuilder() *ContextBuilder { return &ContextBuilder{} }

func (b *ContextBuilder) Build(root, target string) (string, error) {
	contextText, _, err := b.BuildWithManifest(root, target)
	return contextText, err
}

// BuildWithManifest returns the exact prompt context plus safe inspection metadata.
func (b *ContextBuilder) BuildWithManifest(root, target string) (string, ContextManifest, error) {
	return b.BuildWithExcludedFiles(root, target, nil)
}

// BuildWithExcludedFiles also honors explicit analysis exclusions in project context.
func (b *ContextBuilder) BuildWithExcludedFiles(root, target string, excludedPaths []string) (string, ContextManifest, error) {
	manifest := ContextManifest{Included: []ContextFile{}, Excluded: []ContextDecision{}, ByteLimit: maxContextBytes, TokenLimit: maxContextTokens}
	canonical, files, excluded, err := contextFileInventory(root)
	if err != nil {
		return "", manifest, err
	}
	manifest.Excluded = excluded
	ignored := make(map[string]bool, len(excludedPaths))
	for _, file := range excludedPaths {
		ignored[file] = true
	}
	eligible := make([]string, 0, len(files))
	for _, file := range files {
		if ignored[file] {
			manifest.Excluded = append(manifest.Excluded, ContextDecision{Path: file, Reason: "excluded from project analysis by the user"})
		} else {
			eligible = append(eligible, file)
		}
	}
	files = eligible
	target, err = validateContextTarget(canonical, target, files)
	if err != nil {
		return "", manifest, err
	}
	var result strings.Builder
	guides, err := appendContextGuidance(&result, canonical, target, ignored)
	if err != nil {
		return "", manifest, err
	}
	emitted, omitted := writeContextInventory(&result, prioritize(files, target))
	if len(omitted) > 0 {
		manifest.Truncated = true
		manifest.Excluded = appendContextBudgetExclusions(manifest.Excluded, omitted)
	}
	var includedIndex map[string]int
	manifest.Included, includedIndex = contextManifestFiles(canonical, emitted)
	for _, file := range guides {
		if index, ok := includedIndex[file.Path]; ok {
			manifest.Included[index] = file
		} else {
			manifest.Included = append(manifest.Included, file)
		}
	}
	b.appendContextSnippets(&result, canonical, target, emitted, includedIndex, &manifest)
	manifest.EstimatedTokens = estimateTokens(result.String())
	return result.String(), manifest, nil
}

func contextFileInventory(root string) (string, []string, []ContextDecision, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return "", nil, nil, err
	}
	policy, err := NewContextPolicy(canonical)
	if err != nil {
		return "", nil, nil, err
	}
	allFiles, err := WalkProjectFiles(context.Background(), ProjectWalkOptions{Root: canonical, IgnoredDirectories: ignoredProjectDirectories, IncludeSymlinkFiles: true, MaxFiles: maxProjectFiles * 4})
	if err != nil {
		return "", nil, nil, err
	}
	files, excluded := make([]string, 0, len(allFiles)), make([]ContextDecision, 0)
	for _, file := range allFiles {
		decision := policy.Decide(file)
		if decision.Include {
			if len(files) >= maxProjectFiles {
				return "", nil, nil, fmt.Errorf("project contains more than %d files", maxProjectFiles)
			}
			files = append(files, file)
		} else {
			excluded = append(excluded, decision)
		}
	}
	return canonical, files, excluded, nil
}

func contextManifestFiles(root string, files []string) ([]ContextFile, map[string]int) {
	included, includedIndex := make([]ContextFile, 0, len(files)), make(map[string]int, len(files))
	for _, file := range files {
		includedIndex[file] = len(included)
		included = append(included, contextManifestFile(root, file))
	}
	return included, includedIndex
}

func contextManifestFile(root, file string) ContextFile {
	entry := ContextFile{Path: file, Tokens: estimateTokens(file)}
	if fullPath, resolveErr := ResolveFile(root, file); resolveErr == nil {
		if info, statErr := os.Stat(fullPath); statErr == nil {
			entry.SizeBytes = info.Size()
		}
	}
	return entry
}

func validateContextTarget(root, target string, eligibleFiles []string) (string, error) {
	if target == "" {
		return "", nil
	}
	canonicalTarget := filepath.ToSlash(filepath.Clean(target))
	if _, err := ResolveFile(root, canonicalTarget); err != nil {
		return "", fmt.Errorf("invalid target file: %w", err)
	}
	for _, file := range eligibleFiles {
		if file == canonicalTarget {
			return canonicalTarget, nil
		}
	}
	return "", fmt.Errorf("target file is not eligible for model context")
}

// writeContextInventory reserves a bounded portion of the prompt for names,
// so a selected target still has room for source context. It returns only
// paths whose names were actually emitted, plus eligible paths omitted by the
// inventory budget.
func writeContextInventory(result *strings.Builder, files []string) (emitted, omitted []string) {
	usedBytes, usedTokens := 0, 0
	if !appendBoundedInventoryText(result, "## Bounded project file inventory\n", &usedBytes, &usedTokens) {
		return nil, append([]string(nil), files...)
	}
	for index, file := range files {
		if !appendBoundedInventoryText(result, "- "+file+"\n", &usedBytes, &usedTokens) {
			return emitted, append([]string(nil), files[index:]...)
		}
		emitted = append(emitted, file)
	}
	_ = appendBoundedInventoryText(result, "\n", &usedBytes, &usedTokens)
	return emitted, nil
}

func appendBoundedInventoryText(result *strings.Builder, value string, usedBytes, usedTokens *int) bool {
	valueTokens := estimateTokens(value)
	if *usedBytes+len(value) > maxContextInventoryBytes || *usedTokens+valueTokens > maxContextInventoryTokens {
		return false
	}
	result.WriteString(value)
	*usedBytes += len(value)
	*usedTokens += valueTokens
	return true
}

func appendContextBudgetExclusions(excluded []ContextDecision, paths []string) []ContextDecision {
	for _, path := range paths {
		excluded = append(excluded, ContextDecision{Path: path, Reason: "omitted because the bounded context inventory reached its limit"})
	}
	return excluded
}

func (b *ContextBuilder) appendContextSnippets(result *strings.Builder, root, target string, files []string, includedIndex map[string]int, manifest *ContextManifest) {
	for _, relative := range files {
		if result.Len() >= maxContextBytes {
			manifest.Truncated = true
			return
		}
		if b.appendContextSnippet(result, root, target, relative, includedIndex, manifest) {
			return
		}
	}
}

// appendContextSnippet reports whether a context boundary requires traversal to stop.
func (b *ContextBuilder) appendContextSnippet(result *strings.Builder, root, target, relative string, includedIndex map[string]int, manifest *ContextManifest) bool {
	data, originalSize, ok := contextSnippet(root, target, relative)
	if !ok {
		return false
	}
	prefix := "## File: " + relative + "\n```\n"
	suffix := "\n```\n\n"
	remaining := maxContextBytes - result.Len() - len(prefix) - len(suffix)
	availableTokens := maxContextTokens - estimateTokens(result.String()) - estimateTokens(prefix+suffix)
	if remaining <= 0 || availableTokens <= 0 {
		manifest.Truncated = true
		return true
	}
	data = boundedContextData(data, remaining, availableTokens)
	if len(data) < originalSize {
		manifest.Truncated = true
	}
	result.WriteString(prefix)
	result.Write(data)
	result.WriteString(suffix)
	b.updateManifestFile(relative, data, originalSize, includedIndex, manifest)
	return false
}

func contextSnippet(root, target, relative string) ([]byte, int, bool) {
	// Applicable guides are emitted in full before source; other directory
	// guides must not become instructions for this target through a snippet.
	if filepath.Base(relative) == "AGENTS.md" {
		return nil, 0, false
	}
	fullPath, err := ResolveFile(root, relative)
	if err != nil || !contextCandidate(relative) {
		return nil, 0, false
	}
	limit := int64(maxSnippetBytes)
	if relative == filepath.ToSlash(target) {
		limit = maxTargetBytes
	}
	data, err := readLimited(fullPath, limit)
	if err != nil || isBinary(data) {
		return nil, 0, false
	}
	originalSize := len(data)
	if originalSize > int(limit) {
		data = []byte(textutil.Truncate(string(data), int(limit)))
	}
	return data, originalSize, true
}

func boundedContextData(data []byte, remaining, availableTokens int) []byte {
	if len(data) > remaining {
		data = []byte(textutil.Truncate(string(data), remaining))
	}
	if estimateTokens(string(data)) > availableTokens {
		data = []byte(textutil.Truncate(string(data), availableTokens*4))
	}
	return data
}

func (b *ContextBuilder) updateManifestFile(relative string, data []byte, originalSize int, includedIndex map[string]int, manifest *ContextManifest) {
	index, found := includedIndex[relative]
	if !found {
		return
	}
	hash := sha256.Sum256(data)
	manifest.Included[index] = ContextFile{Path: relative, SizeBytes: int64(len(data)), Hash: "sha256:" + hex.EncodeToString(hash[:]), Tokens: estimateTokens(string(data)), Truncated: len(data) < originalSize}
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len([]rune(text)) + 3) / 4
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func prioritize(files []string, target string) []string {
	ordered := make([]string, 0, len(files)+1)
	available := make(map[string]bool, len(files))
	for _, file := range files {
		available[file] = true
	}
	seen := make(map[string]bool, len(files))
	preferred := []string{target}
	for _, scope := range instructionScopes(target) {
		preferred = append(preferred, filepath.ToSlash(filepath.Join(scope, "AGENTS.md")))
	}
	preferred = append(preferred, "README.md", "go.mod", "build.gradle.kts", "build.gradle", "package.json", "Cargo.toml", "pyproject.toml")
	for _, file := range preferred {
		if file != "" && available[file] && !seen[file] {
			ordered = append(ordered, file)
			seen[file] = true
		}
	}
	for _, file := range files {
		if !seen[file] {
			ordered = append(ordered, file)
			seen[file] = true
		}
	}
	return ordered
}

func contextCandidate(path string) bool {
	if symbols.HasKnownExtension(path) {
		return true
	}
	switch filepath.Base(path) {
	case "Dockerfile", "Makefile", "go.mod", "go.sum", "Cargo.toml", "package.json", "pyproject.toml", "requirements.txt", "build.gradle", "build.gradle.kts", "settings.gradle.kts":
		return true
	default:
		return false
	}
}
