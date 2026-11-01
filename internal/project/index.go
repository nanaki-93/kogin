package project

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/project/symbols"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

const (
	indexSchemaVersion = "4"
	indexRelativePath  = ".kasuri/index.json"
)

type ProjectIndex = symbols.ProjectIndex
type IndexFile = symbols.IndexFile
type Diagnostic = symbols.Diagnostic

// BuildIndex scans only context-policy-eligible project files and writes the
// complete index atomically. Existing unchanged entries are reused by hash.
func BuildIndex(root, id, revision string) (*ProjectIndex, error) {
	return buildIndex(root, id, revision, true)
}

// VerifyProjectSources compares live source contents with an index snapshot.
// It ignores policy-excluded files and never rewrites the index.
func VerifyProjectSources(ctx context.Context, root string, index *ProjectIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		return err
	}
	paths, err := WalkProjectFiles(ctx, ProjectWalkOptions{Root: root, Policy: policy, IncludeSymlinkFiles: true, MaxFiles: maxProjectFiles})
	if err != nil {
		return err
	}
	indexed := make(map[string]IndexFile, len(index.Files))
	for _, file := range index.Files {
		if policy.Decide(file.Path).Include {
			indexed[file.Path] = file
		}
	}
	if len(paths) != len(indexed) {
		return ErrRevisionConflict
	}
	buffer := make([]byte, 32*1024)
	for _, relative := range paths {
		file, ok := indexed[relative]
		if !ok {
			return ErrRevisionConflict
		}
		path, err := ResolveFile(root, relative)
		if err != nil {
			return err
		}
		hash, err := hashFileContext(ctx, path, buffer, 0)
		if err != nil {
			return err
		}
		if hash != file.ContentHash {
			return ErrRevisionConflict
		}
	}
	return ctx.Err()
}

// buildIndex can refresh an in-memory index for startup restoration without
// rewriting the persisted cache.
func buildIndex(root, id, revision string, persist bool) (*ProjectIndex, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	if _, err := storage.MetadataPath(canonical, indexRelativePath); err != nil {
		return nil, err
	}
	policy, err := NewContextPolicy(canonical)
	if err != nil {
		return nil, err
	}
	previous, _ := loadIndex(canonical)
	previousFiles := make(map[string]IndexFile)
	if previous != nil && previous.SchemaVersion == indexSchemaVersion {
		for _, file := range previous.Files {
			previousFiles[file.Path] = file
		}
	}
	paths, err := WalkProjectFiles(context.Background(), ProjectWalkOptions{
		Root: canonical, Policy: policy, IgnoredDirectories: ignoredProjectDirectories, IncludeSymlinkFiles: true, MaxFiles: maxProjectFiles,
	})
	if err != nil {
		return nil, err
	}
	index := &ProjectIndex{
		SchemaVersion: indexSchemaVersion, ProjectID: id, ProjectRevision: revision,
		GeneratedAt: time.Now().UTC(), Files: make([]IndexFile, 0, len(paths)),
	}
	for _, relative := range paths {
		if !policy.Decide(relative).Include {
			continue
		}
		entry, err := buildIndexFile(canonical, relative, previousFiles[relative])
		if err != nil {
			return nil, err
		}
		index.Files = append(index.Files, entry)
	}
	if persist {
		if err := writeIndex(canonical, index); err != nil {
			return nil, err
		}
	}
	return index, nil
}

func buildIndexFile(root, relative string, previous IndexFile) (IndexFile, error) {
	fullPath, err := ResolveFile(root, relative)
	if err != nil {
		return IndexFile{}, fmt.Errorf("resolve index file %s: %w", relative, err)
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return IndexFile{}, fmt.Errorf("stat index file %s: %w", relative, err)
	}
	content, err := readIndexContent(fullPath, info)
	if err != nil {
		return IndexFile{}, fmt.Errorf("read index file %s: %w", relative, err)
	}
	entry := IndexFile{
		Path: relative, ContentHash: content.hash, Language: detectLanguage(relative), SizeBytes: content.size,
		ModifiedAt: info.ModTime().UTC(), Binary: content.binary, Imports: []string{}, AnalysisStatus: "missing",
	}
	if !entry.Binary {
		entry.LineCount = content.lines
	}
	if previous.Path == relative && previous.ContentHash == content.hash {
		entry.Imports = append([]string(nil), previous.Imports...)
		entry.Diagnostics = append([]Diagnostic(nil), previous.Diagnostics...)
		entry.AnalysisStatus = previous.AnalysisStatus
	} else if !entry.Binary && content.size > maxFileViewBytes {
		entry.Diagnostics = []Diagnostic{{Message: fmt.Sprintf("File exceeds %d bytes; import extraction is unavailable.", maxFileViewBytes)}}
	} else if entry.Language == "Go" && !entry.Binary {
		entry.Imports, entry.Diagnostics = symbols.ExtractGoFacts(relative, content.source)
	} else if !entry.Binary {
		entry.Imports = symbols.ExtractGenericFacts(entry.Language, content.source)
	}
	return entry, nil
}

func loadIndex(root string) (*ProjectIndex, error) {
	path, err := storage.MetadataPath(root, indexRelativePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var index ProjectIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != indexSchemaVersion {
		return nil, fmt.Errorf("unsupported index schema %q", index.SchemaVersion)
	}
	return &index, nil
}

func writeIndex(root string, index *ProjectIndex) error {
	path, err := storage.MetadataPath(root, indexRelativePath)
	if err != nil {
		return fmt.Errorf("store index: %w", err)
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encode index: %w", err)
	}
	if err := storage.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("store index: %w", err)
	}
	return nil
}
