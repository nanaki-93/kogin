package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nanaki-93/kogin/v2/internal/brand"
	"github.com/nanaki-93/kogin/v2/internal/storage"
)

const contextPolicyVersion = "3"

// ContextDecision explains whether a project file may enter an LLM prompt.
type ContextDecision struct {
	Path    string `json:"path"`
	Include bool   `json:"include"`
	Reason  string `json:"reason"`
}

// ContextPolicy is the single inclusion policy for imports and model prompts.
type ContextPolicy struct {
	root    string
	ignores []ignoreRule
	include []contextRule
	exclude []contextRule
	version string
}

type contextRule struct {
	pattern string
	regexp  *regexp.Regexp
}

type policyConfig struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

func NewContextPolicy(root string) (*ContextPolicy, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	p := &ContextPolicy{root: canonical}
	p.ignores, err = projectIgnoreRules(canonical)
	if err != nil {
		return nil, err
	}
	configPath, err := storage.MetadataPath(canonical, ".kogin", "context-policy.json")
	if err != nil {
		return nil, fmt.Errorf("read context policy: %w", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read context policy: %w", err)
	}
	if err == nil {
		var cfg policyConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse context policy: %w", err)
		}
		p.include, err = compileRules(cfg.Include)
		if err != nil {
			return nil, err
		}
		p.exclude, err = compileRules(cfg.Exclude)
		if err != nil {
			return nil, err
		}
	}
	p.version = policyVersion(p.ignores, p.include, p.exclude)
	return p, nil
}

func (p *ContextPolicy) Version() string { return p.version }

// ReadSource returns bounded, hash-bound source through the same exclusion
// policy as prompt composition. Rooted opening also contains path replacements.
func (p *ContextPolicy) ReadSource(ctx context.Context, path, expectedHash string) ([]byte, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if decision := p.Decide(path); !decision.Include {
		return nil, fmt.Errorf("%w: %s", ErrExcludedFile, decision.Reason)
	}
	if _, err := ResolveWritePath(p.root, path); err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, fmt.Errorf("open source root: %w", err)
	}
	defer root.Close()
	info, err := root.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileViewBytes {
		return nil, fmt.Errorf("source must be a regular file of at most %d bytes", maxFileViewBytes)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%w: source identity changed", ErrRevisionConflict)
	}
	data, err := readAgentSource(ctx, file)
	if err != nil {
		return nil, err
	}
	if expectedHash == "" || contentHash(data) != expectedHash {
		return nil, ErrRevisionConflict
	}
	return data, nil
}

func readAgentSource(ctx context.Context, reader io.Reader) ([]byte, error) {
	var data []byte
	buffer := make([]byte, 32<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		n, err := reader.Read(buffer)
		data = append(data, buffer[:n]...)
		if len(data) > maxFileViewBytes {
			return nil, fmt.Errorf("source exceeds %d bytes", maxFileViewBytes)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read source: %w", err)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if isBinary(data) || !utf8.Valid(data) {
		return nil, fmt.Errorf("source must be UTF-8 text")
	}
	return data, nil
}

func policyVersion(ignores []ignoreRule, groups ...[]contextRule) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(contextPolicyVersion + "\n"))
	for _, rule := range ignores {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\n", rule.base, rule.pattern)
	}
	_, _ = hash.Write([]byte("\n"))
	for _, group := range groups {
		for _, rule := range group {
			_, _ = hash.Write([]byte(rule.pattern + "\n"))
		}
		_, _ = hash.Write([]byte("\n"))
	}
	return contextPolicyVersion + ":" + hex.EncodeToString(hash.Sum(nil))
}

func (p *ContextPolicy) Decide(relative string) ContextDecision {
	path := filepath.ToSlash(filepath.Clean(relative))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") || filepath.IsAbs(relative) {
		return ContextDecision{Path: path, Reason: "unsafe path"}
	}
	if first := strings.SplitN(path, "/", 2)[0]; brand.IsMetadataDir(first) {
		return ContextDecision{Path: path, Reason: "Kogin metadata"}
	}
	if slices.Contains(strings.Split(path, "/"), ".worktrees") {
		return ContextDecision{Path: path, Reason: "isolated worktrees"}
	}
	if hasSymlinkComponent(p.root, path) {
		return ContextDecision{Path: path, Reason: "symlink is excluded from model context"}
	}
	name := strings.ToLower(filepath.Base(path))
	if secretFile(name) {
		return ContextDecision{Path: path, Reason: "secret or local configuration"}
	}
	if generatedOrLockFile(name) {
		return ContextDecision{Path: path, Reason: "generated, dependency, or lock file"}
	}
	if matches(p.exclude, path) {
		return ContextDecision{Path: path, Reason: "project context-policy exclude"}
	}
	if matches(p.include, path) {
		return ContextDecision{Path: path, Include: true, Reason: "project context-policy include"}
	}
	if ignoredPath(p.ignores, path, false) {
		return ContextDecision{Path: path, Reason: "ignored by .gitignore or .koginignore"}
	}
	return ContextDecision{Path: path, Include: true, Reason: "eligible source or text file"}
}

// Ignored reports whether .gitignore or .koginignore rules exclude the
// path. Unlike Decide it ignores the model-context rules for secrets, lock
// files and generated files.
func (p *ContextPolicy) Ignored(relative string) bool {
	return ignoredPath(p.ignores, filepath.ToSlash(filepath.Clean(relative)), false)
}

// IgnoredDirectory reports whether .gitignore or .koginignore rules
// exclude the directory, including rules that only match directories.
func (p *ContextPolicy) IgnoredDirectory(relative string) bool {
	return ignoredPath(p.ignores, filepath.ToSlash(filepath.Clean(relative)), true)
}

func hasSymlinkComponent(root, relative string) bool {
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

func secretFile(name string) bool {
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}
	if name == "config.yaml" || name == "config.yml" || strings.Contains(name, ".local.") {
		return true
	}
	for _, fragment := range []string{"credential", "secret", "token", "apikey", "api_key", "password"} {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	for _, extension := range []string{".pem", ".key", ".crt", ".cer", ".der", ".p12", ".pfx"} {
		if strings.HasSuffix(name, extension) {
			return true
		}
	}
	return false
}

func generatedOrLockFile(name string) bool {
	if strings.Contains(name, ".min.") || strings.Contains(name, ".generated.") || strings.HasSuffix(name, "_generated.go") {
		return true
	}
	switch name {
	case "go.sum", "cargo.lock", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "composer.lock", "gemfile.lock":
		return true
	}
	return false
}

func compileRules(patterns []string) ([]contextRule, error) {
	rules := make([]contextRule, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pattern)), "/")
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "/") {
			pattern += "**"
		}
		expr := regexp.QuoteMeta(pattern)
		expr = strings.ReplaceAll(expr, "\\*\\*", ".*")
		expr = strings.ReplaceAll(expr, "\\*", "[^/]*")
		expr = strings.ReplaceAll(expr, "\\?", "[^/]")
		re, err := regexp.Compile("^(?:.*/)?" + expr + "$")
		if err != nil {
			return nil, fmt.Errorf("invalid context-policy pattern %q: %w", pattern, err)
		}
		rules = append(rules, contextRule{pattern: pattern, regexp: re})
	}
	return rules, nil
}

func matches(rules []contextRule, path string) bool {
	for _, rule := range rules {
		if rule.regexp.MatchString(path) {
			return true
		}
	}
	return false
}
