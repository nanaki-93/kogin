package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestManagerSetBuildsPolicyFilteredPersistentIndex(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc main() {}\n")
	writeIndexFixture(t, root, ".env", "TOKEN=must-not-index\n")
	writeIndexFixture(t, root, "ignored.go", "package ignored\n")
	writeIndexFixture(t, root, ".gitignore", "ignored.go\n")
	writeIndexFixture(t, root, "binary.dat", string([]byte{0, 1, 2}))

	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &Analysis{Name: "fixture"}); err != nil {
		t.Fatal(err)
	}
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	main := findIndexFile(index, "main.go")
	if main == nil || main.ContentHash == "" || main.LineCount != 2 || main.Binary {
		t.Fatalf("main index entry = %+v", main)
	}
	if findIndexFile(index, ".env") != nil || findIndexFile(index, "ignored.go") != nil {
		t.Fatalf("excluded content appeared in index: %+v", index.Files)
	}
	binary := findIndexFile(index, "binary.dat")
	if binary == nil || !binary.Binary || binary.LineCount != 0 || len(binary.Imports) != 0 {
		t.Fatalf("binary metadata entry = %+v", binary)
	}

	data, err := os.ReadFile(filepath.Join(root, indexRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || string(data) == "package main\nfunc main() {}\n" {
		t.Fatalf("index persistence is missing or contains source content: %s", data)
	}
	var persisted ProjectIndex
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ProjectRevision != index.ProjectRevision || persisted.ProjectID != index.ProjectID {
		t.Fatalf("persisted index state = %+v, want %+v", persisted, index)
	}
}

func TestReindexChangesOnlyModifiedEntryAndRevision(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc main() {}\n")
	writeIndexFixture(t, root, "helper.go", "package main\nfunc helper() {}\n")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &Analysis{}); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() { println(\"changed\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	after, err := manager.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if before.ProjectRevision == after.ProjectRevision {
		t.Fatal("revision did not change after source edit")
	}
	if reflect.DeepEqual(*findIndexFile(before, "main.go"), *findIndexFile(after, "main.go")) {
		t.Fatal("modified file index entry was reused")
	}
	if !reflect.DeepEqual(*findIndexFile(before, "helper.go"), *findIndexFile(after, "helper.go")) {
		t.Fatalf("unchanged entry changed:\n before=%+v\n after=%+v", findIndexFile(before, "helper.go"), findIndexFile(after, "helper.go"))
	}
}

func TestManagerPersistsFileAnalysisStatus(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc main() {}\n")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &Analysis{}); err != nil {
		t.Fatal(err)
	}
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.UpdateFileAnalysisStatus(index.ProjectID, index.ProjectRevision, "main.go", AnalysisStatusFresh); err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if file := findIndexFile(updated, "main.go"); file == nil || file.AnalysisStatus != AnalysisStatusFresh {
		t.Fatalf("updated index file = %+v", file)
	}

	data, err := os.ReadFile(filepath.Join(root, indexRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	var persisted ProjectIndex
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if file := findIndexFile(&persisted, "main.go"); file == nil || file.AnalysisStatus != AnalysisStatusFresh {
		t.Fatalf("persisted index file = %+v", file)
	}
}

func TestBuildIndexRebuildsCorruptCacheAtomically(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\n")
	if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, indexRelativePath), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := BuildIndex(root, "sha256:project", "sha256:revision")
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Files) != 1 || index.Files[0].Path != "main.go" {
		t.Fatalf("rebuilt index = %+v", index)
	}
	data, err := os.ReadFile(filepath.Join(root, indexRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	var persisted ProjectIndex
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("rebuilt index is invalid JSON: %v", err)
	}
	temps, err := filepath.Glob(filepath.Join(root, ".kogin", ".index-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("index temp files = %v, %v", temps, err)
	}
}

func TestManagerRestoreRefreshesIndexWithoutRewritingPersistedCache(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\n")
	manager, err := NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(root, &Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	persistedPath := filepath.Join(root, indexRelativePath)
	before, err := os.ReadFile(persistedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := manager.Restore(root, &Analysis{Name: "fixture", Path: root}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(persistedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("restore rewrote the persisted index")
	}
	index, err := manager.Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Files) != 1 || index.Files[0].LineCount != 2 {
		t.Fatalf("restored in-memory index = %+v", index.Files)
	}
}

func writeIndexFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func findIndexFile(index *ProjectIndex, path string) *IndexFile {
	for i := range index.Files {
		if index.Files[i].Path == path {
			return &index.Files[i]
		}
	}
	return nil
}

func TestBuildIndexRejectsSymlinkedMetadataWithoutOverwritingOutsideFiles(t *testing.T) {
	for _, mode := range []string{"directory", "file"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeIndexFixture(t, root, "main.go", "package main\nfunc main() {}\n")
			outside := t.TempDir()
			outsideIndex := filepath.Join(outside, "index.json")
			if err := os.WriteFile(outsideIndex, []byte("keep-outside-index"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "directory" {
				if err := os.Symlink(outside, filepath.Join(root, ".kogin")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideIndex, filepath.Join(root, indexRelativePath)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := BuildIndex(root, "sha256:project", "sha256:revision"); err == nil {
				t.Fatalf("BuildIndex succeeded with symlinked %s metadata", mode)
			}
			data, err := os.ReadFile(outsideIndex)
			if err != nil || string(data) != "keep-outside-index" {
				t.Fatalf("outside index.json was modified: %q, %v", data, err)
			}
		})
	}
}
