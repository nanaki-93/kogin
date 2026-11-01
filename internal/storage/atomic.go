// Package storage provides small durable primitives for metadata owned by the
// application. Domain stores keep their own validation and serialization.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrSymlink reports that a metadata path or one of its directory components is a symlink.
var ErrSymlink = errors.New("metadata path contains a symlink")

// MetadataPath resolves a project-contained path under .kasuri and rejects
// symlink components before reads, writes, or removals.
func MetadataPath(root string, parts ...string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("metadata project root is required")
	}
	cleanRoot := filepath.Clean(root)
	segments, err := normalizeMetadataSegments(parts)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(append([]string{cleanRoot}, segments...)...)
	rel, err := filepath.Rel(cleanRoot, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("metadata path escapes project directory")
	}
	if err := inspectMetadataSegments(cleanRoot, segments); err != nil {
		return "", err
	}
	return candidate, nil
}

func normalizeMetadataSegments(parts []string) ([]string, error) {
	var segments []string
	for index, part := range parts {
		if part == "" && index == len(parts)-1 && len(segments) > 0 {
			continue
		}
		if part == "" || strings.Contains(part, "\\") || filepath.IsAbs(part) {
			return nil, fmt.Errorf("metadata path escapes project directory")
		}
		for _, segment := range strings.Split(filepath.ToSlash(part), "/") {
			if segment == "" || segment == "." || segment == ".." {
				return nil, fmt.Errorf("metadata path escapes project directory")
			}
			segments = append(segments, segment)
		}
	}
	if len(segments) == 0 || segments[0] != ".kasuri" {
		return nil, fmt.Errorf("metadata path must stay under .kasuri")
	}
	return segments, nil
}

func inspectMetadataSegments(cleanRoot string, segments []string) error {
	current := cleanRoot
	for index, segment := range segments {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
		if index < len(segments)-1 && !info.IsDir() {
			return fmt.Errorf("metadata parent is not a directory")
		}
	}
	return nil
}

func validateMetadataFileTarget(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("metadata path is required")
	}
	clean := filepath.Clean(path)
	if root, segments, ok := splitKasuriPath(clean); ok {
		if _, err := MetadataPath(root, segments...); err != nil {
			return err
		}
	} else if err := rejectDirectSymlinkTarget(clean); err != nil {
		return err
	}
	return nil
}

func splitKasuriPath(clean string) (string, []string, bool) {
	var tail []string
	current := clean
	for {
		base := filepath.Base(current)
		parent := filepath.Dir(current)
		if base == ".kasuri" && parent != current {
			segments := make([]string, 0, len(tail)+1)
			segments = append(segments, ".kasuri")
			for i := len(tail) - 1; i >= 0; i-- {
				segments = append(segments, tail[i])
			}
			return parent, segments, true
		}
		if parent == current {
			return "", nil, false
		}
		tail = append(tail, base)
		current = parent
	}
}

func rejectDirectSymlinkTarget(clean string) error {
	for _, candidate := range []string{filepath.Dir(clean), clean} {
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
	}
	return nil
}

// WriteFile replaces path with data through a same-directory temporary file.
// Metadata directories and files are private to the current user. The source
// file is synced and closed before it replaces an existing destination.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return writeFile(path, data, mode, defaultAtomicFileOperations())
}

// WriteFileAuthorized writes and syncs a same-directory temporary file, then
// calls authorize immediately before atomically replacing path. A rejected
// authorization leaves the destination unchanged and removes the temporary.
func WriteFileAuthorized(path string, data []byte, mode os.FileMode, authorize func() error) error {
	return writeFileAuthorized(path, data, mode, authorize, defaultAtomicFileOperations())
}

type atomicFileOperations struct {
	write   func(*os.File, []byte) (int, error)
	chmod   func(*os.File, os.FileMode) error
	sync    func(*os.File) error
	close   func(*os.File) error
	replace func(string, string) error
}

func defaultAtomicFileOperations() atomicFileOperations {
	return atomicFileOperations{
		write:   func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		chmod:   func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:    func(file *os.File) error { return file.Sync() },
		close:   func(file *os.File) error { return file.Close() },
		replace: os.Rename,
	}
}

func writeFile(path string, data []byte, mode os.FileMode, operations atomicFileOperations) error {
	return writeFileAuthorized(path, data, mode, nil, operations)
}

func writeFileAuthorized(path string, data []byte, mode os.FileMode, authorize func() error, operations atomicFileOperations) error {
	if err := validateMetadataFileTarget(path); err != nil {
		return fmt.Errorf("validate metadata path: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".metadata-*.tmp")
	if err != nil {
		return fmt.Errorf("create metadata temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := writeAndSyncTemp(temporary, data, mode, operations); err != nil {
		return err
	}
	if err := operations.close(temporary); err != nil {
		closed = true
		return fmt.Errorf("close metadata: %w", err)
	}
	closed = true
	if authorize != nil {
		if err := authorize(); err != nil {
			return fmt.Errorf("authorize metadata replacement: %w", err)
		}
	}
	if err := validateMetadataFileTarget(path); err != nil {
		return fmt.Errorf("validate metadata path: %w", err)
	}
	if err := operations.replace(temporaryPath, path); err != nil {
		return fmt.Errorf("replace metadata: %w", err)
	}
	// Persist the rename itself: without a directory sync a crash can keep the
	// source edits a journal describes while losing the journal.
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync metadata directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeAndSyncTemp(temporary *os.File, data []byte, mode os.FileMode, operations atomicFileOperations) error {
	if _, err := operations.write(temporary, data); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	if err := operations.chmod(temporary, mode); err != nil {
		return fmt.Errorf("set metadata permissions: %w", err)
	}
	if err := operations.sync(temporary); err != nil {
		return fmt.Errorf("sync metadata: %w", err)
	}
	return nil
}

// RecoverCorrupt preserves an unreadable metadata file using the established
// .corrupt-<UTC timestamp> suffix. Stores opt into recovery explicitly.
func RecoverCorrupt(path string) error {
	if err := validateMetadataFileTarget(path); err != nil {
		return fmt.Errorf("recover corrupt metadata: %w", err)
	}
	corrupt := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.Rename(path, corrupt); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("recover corrupt metadata: %w", err)
	}
	return nil
}
