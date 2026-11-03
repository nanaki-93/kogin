package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	value, err := gitRead(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(value)
}

func initializeChangeGit(t *testing.T, service *Service, root string) {
	t.Helper()
	testGit(t, root, "init", "-b", "main")
	testGit(t, root, "config", "user.name", "Kogin Test")
	testGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".kogin/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "--", ".")
	testGit(t, root, "commit", "-m", "Initial fixture")
	if _, err := service.Reindex(); err != nil {
		t.Fatal(err)
	}
}
