package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyCheckWorkspaceExcludesCachesAndIgnoredOutputAndKeepsTrackedOutput(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "workspace")
	for path, content := range map[string]string{
		"main.go":                           "package fixture\n",
		".git":                              "gitdir: /outside/worktree\n",
		"node_modules/dependency/index.js":  "cache",
		"desktop/build/generated.txt":       "ignored output",
		".gitignore":                        "build/\n",
		"internal/webui/dist/index.js":      "embedded bundle",
		"target/classes/App.class":          "tracked output",
		".worktrees/fix/main.go":            "package ignored\n",
		".kogin/check.json":                 "metadata",
		"testdata/node_modules/fixture.txt": "required fixture",
		"testdata/nested/.git":              "gitdir: /outside/fixture-worktree\n",
		"testdata/nested/.hg/store":         "metadata fixture",
		"testdata/nested/.svn/entries":      "metadata fixture",
		"testdata/nested/.kogin/check.json": "metadata fixture",
	} {
		full := filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(source, "main.go"), filepath.Join(source, "linked.go")); err != nil {
		t.Fatal(err)
	}
	if err := CopyWorkspace(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"main.go", "testdata/node_modules/fixture.txt", "internal/webui/dist/index.js", "target/classes/App.class"} {
		if _, err := os.Stat(filepath.Join(destination, path)); err != nil {
			t.Fatalf("required copied path %q: %v", path, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(destination, "node_modules")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("installed dependencies are linked, not copied: %v, %v", info, err)
	}
	for _, path := range []string{".git", ".worktrees", "desktop/build", ".kogin", "linked.go", "testdata/nested/.git", "testdata/nested/.hg", "testdata/nested/.svn", "testdata/nested/.kogin"} {
		if _, err := os.Lstat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded path %q has error %v", path, err)
		}
	}
}

func TestValidateCheckWorkspaceFileRejectsNonRegularAndChangedFiles(t *testing.T) {
	directory := t.TempDir()
	regularPath := filepath.Join(directory, "regular.txt")
	otherPath := filepath.Join(directory, "other.txt")
	if err := os.WriteFile(regularPath, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Lstat(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Open(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := validateCheckWorkspaceFile(regularPath, regular, other); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := validateCheckWorkspaceFile(directory, directoryInfo, input); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("nonregular source error = %v", err)
	}
}

func TestCopyCheckWorkspaceRejectsResourceLimitAndCleansWorkspace(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, source string)
		limit string
	}{
		{
			name: "bytes",
			setup: func(t *testing.T, source string) {
				t.Helper()
				path := filepath.Join(source, "large.bin")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, checkLimits.maxWorkspaceBytes+1); err != nil {
					t.Fatal(err)
				}
			},
			limit: "byte limit",
		},
		{
			name: "files",
			setup: func(t *testing.T, source string) {
				t.Helper()
				for index := 0; index <= checkLimits.maxWorkspaceFiles; index++ {
					path := filepath.Join(source, fmt.Sprintf("file-%05d", index))
					if err := os.WriteFile(path, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			},
			limit: "file limit",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := t.TempDir()
			destination := filepath.Join(t.TempDir(), "workspace")
			test.setup(t, source)
			err := CopyWorkspace(context.Background(), source, destination)
			if err == nil || !strings.Contains(err.Error(), test.limit) {
				t.Fatalf("copy error = %v", err)
			}
			if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
				t.Fatalf("failed copy left workspace behind: %v", statErr)
			}
		})
	}
}

func TestCopyCheckWorkspaceHonorsCancellation(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "workspace")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := CopyWorkspace(ctx, source, destination)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("canceled copy left workspace behind: %v", statErr)
	}
}

func TestCopyCheckWorkspaceFileStopsAfterMidCopyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := cancelAfterFirstRead{data: []byte("first-second"), cancel: cancel}
	var output bytes.Buffer

	copied, err := copyCheckWorkspaceFile(ctx, &output, &input, make([]byte, len("first")), int64(len(input.data)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v", err)
	}
	if copied != int64(len("first")) || output.String() != "first" {
		t.Fatalf("partial copy = %d, %q", copied, output.String())
	}
}

func TestCheckWorkspaceCopierCopyFileRemovesOutputOnCancellation(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "workspace")
	sourcePath := filepath.Join(source, "main.go")
	if err := os.WriteFile(sourcePath, []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	copier := checkWorkspaceCopier{
		ctx: ctx, source: source, destination: destination,
		buffer: make([]byte, checkCopyBufferSize),
	}

	err = copier.copyFile(sourcePath, "main.go", info)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "main.go")); !os.IsNotExist(err) {
		t.Fatalf("canceled file copy left output behind: %v", err)
	}
}

type cancelAfterFirstRead struct {
	data   []byte
	cancel context.CancelFunc
	read   bool
}

func (reader *cancelAfterFirstRead) Read(destination []byte) (int, error) {
	if reader.read {
		return 0, io.EOF
	}
	reader.read = true
	read := copy(destination, reader.data)
	reader.cancel()
	return read, nil
}

func TestFingerprintCheckWorkspaceCancelsDuringRead(t *testing.T) {
	root := t.TempDir()
	writeBenchmarkFile(t, root, "large.go", strings.Repeat("x", 2*checkCopyBufferSize))
	ctx := &cancelAfterBenchmarkContext{cancelAt: 4}
	if _, err := FingerprintWorkspace(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("fingerprint error = %v, want context cancellation", err)
	}
}

func TestWorkspaceFingerprintMatchesCopiedCheckWorkspace(t *testing.T) {
	root := t.TempDir()
	writeBenchmarkFile(t, root, "main.go", "package main\n")
	if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0700); err != nil {
		t.Fatal(err)
	}
	writeBenchmarkFile(t, root, filepath.Join(".kogin", "state.json"), "{}")
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	writeBenchmarkFile(t, root, filepath.Join("node_modules", "pkg", "index.js"), "export default 1;\n")

	direct, err := WorkspaceFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	copiedDir := t.TempDir()
	if err := CopyWorkspace(context.Background(), root, copiedDir); err != nil {
		t.Fatal(err)
	}
	copied, err := FingerprintWorkspace(context.Background(), copiedDir)
	if err != nil {
		t.Fatal(err)
	}
	if direct != copied {
		t.Fatalf("in-place fingerprint %s != copied fingerprint %s", direct, copied)
	}
}

func TestWorkspaceFingerprintFollowsTrackedOutputAndIgnoresIgnoredOutput(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		".gitignore":        "build/\n",
		"main.go":           "package main\n",
		"build/app.bin":     "first build",
		"webui/dist/app.js": "bundle v1",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		writeBenchmarkFile(t, root, path, content)
	}
	before, err := WorkspaceFingerprint(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeBenchmarkFile(t, root, filepath.Join("build", "app.bin"), "second build")
	if after, err := WorkspaceFingerprint(context.Background(), root); err != nil || after != before {
		t.Fatalf("ignored build output changed the fingerprint: %s -> %s, %v", before, after, err)
	}
	writeBenchmarkFile(t, root, filepath.Join("webui", "dist", "app.js"), "bundle v2")
	if after, err := WorkspaceFingerprint(context.Background(), root); err != nil || after == before {
		t.Fatalf("tracked bundle did not change the fingerprint: %s -> %s, %v", before, after, err)
	}
}

type cancelAfterBenchmarkContext struct {
	checks   int
	cancelAt int
}

func (ctx *cancelAfterBenchmarkContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterBenchmarkContext) Done() <-chan struct{}       { return nil }
func (ctx *cancelAfterBenchmarkContext) Value(any) any               { return nil }
func (ctx *cancelAfterBenchmarkContext) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func writeBenchmarkFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkCopyCheckWorkspace(b *testing.B) {
	for _, files := range []int{50, 2000} {
		b.Run(fmt.Sprintf("files_%d", files), func(b *testing.B) {
			root := writeCheckWorkspaceBenchmarkFixture(b, files, 4096)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dest, err := os.MkdirTemp("", "perf01-copy-")
				if err != nil {
					b.Fatal(err)
				}
				err = CopyWorkspace(context.Background(), root, dest)
				removeErr := os.RemoveAll(dest)
				if err != nil {
					b.Fatal(err)
				}
				if removeErr != nil {
					b.Fatal(removeErr)
				}
			}
		})
	}
}

func writeCheckWorkspaceBenchmarkFixture(b *testing.B, files, bytes int) string {
	b.Helper()
	root := b.TempDir()
	content := make([]byte, bytes)
	for i := range content {
		content[i] = 'x'
	}
	for i := 0; i < files; i++ {
		path := filepath.Join(root, fmt.Sprintf("pkg%03d", i/100), fmt.Sprintf("file%05d.dat", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			b.Fatal(err)
		}
	}
	return root
}
