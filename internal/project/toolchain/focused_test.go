package toolchain

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGoPackageTestRunsOnlyThePackagesOfTheFiles(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"go.mod", "main.go", "cmd/add.go", "internal/utils/utils.go", "tools/go.mod", "tools/gen.go", "web/go.mod", "web/api/api.go"} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command, packages := GoPackageTest(root, "", []string{"cmd/add.go", "cmd/add_test.go", "main.go", "README.md", "tools/gen.go", "gone/old.go"})
	if !reflect.DeepEqual(command, []string{"go", "test", ".", "./cmd"}) || !reflect.DeepEqual(packages, []string{".", "./cmd"}) {
		t.Fatalf("command %q, packages %q", command, packages)
	}
	if command, _ := GoPackageTest(root, "web", []string{"web/api/api.go", "cmd/add.go"}); !reflect.DeepEqual(command, []string{"go", "test", "./api"}) {
		t.Fatalf("in a subproject: %q", command)
	}
	if command, _ := GoPackageTest(root, "", []string{"README.md", "go.mod"}); command != nil {
		t.Fatalf("no Go code changed: %q", command)
	}
	if command, _ := GoPackageTest(filepath.Join(root, "cmd"), "", []string{"add.go"}); command != nil {
		t.Fatalf("not a module: %q", command)
	}
}
