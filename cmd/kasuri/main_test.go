package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nanaki-93/mini-orca/v2/internal/launcher"
)

func TestProjectFolderDefaultsToTheCurrentFolder(t *testing.T) {
	folder := t.TempDir()
	t.Chdir(folder)
	canonical, _ := filepath.EvalSymlinks(folder)
	opts, err := parseOptions(nil, io.Discard)
	if err != nil || opts.project != canonical || !opts.open || opts.url != launcher.DefaultURL {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
	other := t.TempDir()
	if opts, err := parseOptions([]string{"--no-open", other}, io.Discard); err != nil || opts.open || filepath.Base(opts.project) != filepath.Base(other) {
		t.Fatalf("explicit folder: %+v %v", opts, err)
	}
	file := filepath.Join(folder, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{file}, {filepath.Join(folder, "missing")}, {folder, other}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
