package changes

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/storage"
)

var SessionIDPattern = regexp.MustCompile(`^change-[a-f0-9]{32}$`)

// NewOpaqueID generates a random hex identifier with the given prefix.
func NewOpaqueID(prefix string) string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return prefix + "-" + hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
}

// MetadataPath disallows metadata-directory aliases before either reads or writes.
func MetadataPath(root, name string) (string, error) {
	return storage.MetadataPath(root, ".kasuri", "changes", name)
}

// AtomicWriteSource writes content to a temporary file in the same directory,
// preserves file permissions, fsyncs both the file and parent directory, and renames.
func AtomicWriteSource(path string, content []byte) error {
	info, err := os.Stat(path)
	mode := os.FileMode(0600)
	if err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".kasuri-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err == nil {
		defer directory.Close()
		if err := directory.Sync(); err != nil {
			return &SourceWriteError{Cause: err}
		}
	} else {
		return &SourceWriteError{Cause: err}
	}
	return nil
}

// SourceWriteError indicates the target file was replaced on disk, but the
// subsequent parent directory fsync failed.
type SourceWriteError struct{ Cause error }

func (err *SourceWriteError) Error() string {
	return "source replaced; directory sync failed: " + err.Cause.Error()
}
func (err *SourceWriteError) Unwrap() error { return err.Cause }

// ContentHash computes the canonical sha256:<hex> digest of source bytes.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
