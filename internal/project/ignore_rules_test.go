package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIgnoredSourceNeverEntersContext(t *testing.T) {
	for _, test := range []struct{ name, ignore, pattern, target string }{
		{"directory", ".gitignore", "private\n", "private/client.go"},
		{"nested", "sub/.gitignore", "internal.go\n", "sub/internal.go"},
		{"recursive root", ".gitignore", "**/internal.go\n", "internal.go"},
		{"nested kogin", "sub/.koginignore", "private/\n", "sub/private/client.go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeIndexFixture(t, root, test.ignore, test.pattern)
			writeIndexFixture(t, root, test.target, "package sample\n// PRIVATE_REVIEW_MARKER\n")
			client := &recordingChatClient{}
			if _, err := analyzeProjectForTest(context.Background(), NewAnalyzerWithProvenance(client, "", "analysis", "", ""), root); err != nil {
				t.Fatal(err)
			}
			for _, message := range client.messages {
				if strings.Contains(message.Content, "PRIVATE_REVIEW_MARKER") {
					t.Fatal("ignored source entered provider context")
				}
			}
		})
	}
}

func TestIgnoreDecisionsRespectGitPatternSemantics(t *testing.T) {
	for _, test := range []struct {
		pattern, path string
		ignored       bool
	}{
		{"/root.go", "root.go", true},
		{"/root.go", "sub/root.go", false},
		{"sub/file.go", "nested/sub/file.go", false},
		{"cache/", "cache", false},
		{"cache/", "cache/file.go", true},
		{"cache", "sub/cache/file.go", true},
		{"*.go\n!keep.go", "keep.go", false},
		{"private/\n!private/keep.go", "private/keep.go", true},
		{"private/**\n!private/keep.go", "private/keep.go", false},
		{"private/**\n!private/nested/keep.go", "private/nested/keep.go", true},
		{"a/**/b.go", "a/b.go", true},
		{"a/**/b.go", "a/one/two/b.go", true},
		{"a/*/b.go", "a/one/two/b.go", false},
		{"file[0-9].go", "file2.go", true},
		{"file[0-9].go", "filex.go", false},
		{"file[!0-9].go", "filex.go", true},
		{"file[!0-9].go", "file2.go", false},
		{"file[[:digit:]].go", "file2.go", true},
		{"file[[:digit:]].go", "filex.go", false},
		{"unclosed[.go", "unclosed[.go", true},
		{"\\#file.go", "#file.go", true},
		{"\\!file.go", "!file.go", true},
		{"space\\ ", "space /file.go", true},
		{" leading.go", " leading.go", true},
	} {
		t.Run(test.pattern+":"+test.path, func(t *testing.T) {
			root := t.TempDir()
			writeIndexFixture(t, root, ".gitignore", test.pattern+"\n")
			writeIndexFixture(t, root, test.path, "package sample\n")
			policy, err := NewContextPolicy(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := policy.Decide(test.path); got.Include == test.ignored {
				t.Fatalf("decision = %+v, want ignored=%v", got, test.ignored)
			}
		})
	}
}

func TestNestedIgnoreChangesInvalidatePolicyAndRespectParentExclusions(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, ".gitignore", "*.go\nprivate/\n")
	writeIndexFixture(t, root, "sub/.gitignore", "!keep.go\n")
	writeIndexFixture(t, root, "private/.gitignore", "!keep.go\n")
	writeIndexFixture(t, root, "sub/keep.go", "package sample\n")
	writeIndexFixture(t, root, "private/keep.go", "package sample\n")
	before, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Decide("sub/keep.go").Include || before.Decide("private/keep.go").Include {
		t.Fatal("nested negation ignored its directory scope or reopened an excluded parent")
	}
	if err := os.Remove(filepath.Join(root, "sub/.gitignore")); err != nil {
		t.Fatal(err)
	}
	after, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version() == before.Version() || after.Decide("sub/keep.go").Include {
		t.Fatal("removing nested rules did not invalidate policy and exclude source")
	}
}
