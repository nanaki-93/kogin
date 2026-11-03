// Package project contains active-project state, analysis, and safe file access.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nanaki-93/kogin/v2/internal/brand"
)

// CanonicalRoot validates a project directory and resolves symlinks.
func CanonicalRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("project path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory")
	}
	return filepath.Clean(resolved), nil
}

// ResolveFile safely resolves a relative path inside root, including symlinks.
func ResolveFile(root, relative string) (string, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return "", err
	}
	return resolveFile(canonical, relative)
}

// Batch readers canonicalize their root once, then retain the same containment
// and symlink checks for every file in that snapshot.
func resolveFile(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", fmt.Errorf("file path is required")
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("file path must be relative to the project")
	}
	candidate := filepath.Join(root, filepath.Clean(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file path escapes project directory")
	}
	return resolved, nil
}

// ResolveWritePath admits a canonical project-relative spelling and rejects
// symlink components even when their destination is inside the project. Missing
// components are allowed for a new file; this function never creates them.
func ResolveWritePath(root, relative string) (string, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return "", err
	}
	if err := validateWritePath(relative); err != nil {
		return "", err
	}
	current := canonical
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect write path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("write path contains a symlink")
		}
		if index < len(parts)-1 && !info.IsDir() || index == len(parts)-1 && !info.Mode().IsRegular() {
			return "", fmt.Errorf("write path is not a regular file path")
		}
	}
	return current, nil
}

func validateWritePath(relative string) error {
	if relative == "" || len(relative) > 512 || strings.TrimSpace(relative) != relative || filepath.IsAbs(relative) || strings.Contains(relative, "\\") || filepath.ToSlash(filepath.Clean(relative)) != relative || relative == "." {
		return fmt.Errorf("provide a canonical project-relative file path")
	}
	if strings.IndexFunc(relative, unicode.IsControl) >= 0 {
		return fmt.Errorf("file path contains control characters")
	}
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case "..", ".git", ".aws", ".codex", ".agents":
			return fmt.Errorf("file path targets protected metadata")
		}
		if brand.IsMetadataDir(part) {
			return fmt.Errorf("file path targets protected metadata")
		}
	}
	return nil
}
