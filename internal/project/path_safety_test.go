package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritePathContainmentAndNewFiles(t *testing.T) {
	root := t.TempDir()
	canonical, err := CanonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.go", "/outside.go", "a/../b.go", "a//b.go", "./b.go", " a.go", "a\\b.go", ".git/config", ".kasuri/history.json", "a\nb.go"} {
		if _, err := ResolveWritePath(root, path); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	resolved, err := ResolveWritePath(root, "new/package/main.go")
	if err != nil || resolved != filepath.Join(canonical, "new/package/main.go") {
		t.Fatalf("new path: %s, %v", resolved, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("resolution created a directory")
	}
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("package test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWritePath(root, "existing.go"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWritePath(root, "alias/existing.go"); err == nil {
		t.Fatal("accepted symlink alias")
	}
}
