package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionsResolveApplicableGuidesAndIdentity(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"AGENTS.md": "Use precise names.", "internal/AGENTS.md": "Test error paths.", "internal/worker/AGENTS.md": "Keep cancellation.", "other/AGENTS.md": "Unrelated."} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ResolveInstructions(root, "internal/worker/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 3 || result.Files[0].Path != "AGENTS.md" || result.Files[2].Scope != "internal/worker" || strings.Contains(result.Text, "Unrelated") {
		t.Fatalf("instructions: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Changed guidance."), 0644); err != nil {
		t.Fatal(err)
	}
	changed, err := ResolveInstructions(root, "internal/worker/new.go")
	if err != nil || changed.Fingerprint == result.Fingerprint {
		t.Fatalf("identity did not change: %v", err)
	}
}

func TestInstructionsRespectPolicyAndRejectUnsafeInput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Private."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".kogin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kogin/context-policy.json"), []byte(`{"exclude":["AGENTS.md"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ResolveInstructions(root, "new.go")
	if err != nil || len(result.Files) != 0 || len(result.Excluded) != 1 {
		t.Fatalf("excluded instructions: %+v, %v", result, err)
	}
	if _, err := ResolveInstructions(root, "../outside.go"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Remove(filepath.Join(root, ".kogin/context-policy.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("a", MaxInstructionBytes+1)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInstructions(root, "new.go"); err == nil {
		t.Fatal("accepted oversized instructions")
	}
}

func TestInstructionsMissingAndSymlink(t *testing.T) {
	root := t.TempDir()
	result, err := ResolveInstructions(root, "new/path.go")
	if err != nil || len(result.Files) != 0 || result.Fingerprint == "" {
		t.Fatalf("missing instructions: %+v, %v", result, err)
	}
	outside := filepath.Join(t.TempDir(), "guide.md")
	if err := os.WriteFile(outside, []byte("Outside."), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveInstructions(root, "new.go"); err == nil {
		t.Fatal("followed instruction symlink")
	}
}

func TestProjectContextIncludesApplicableGuidesOnceAndHonorsExclusions(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"AGENTS.md":          "Root cancellation guidance.",
		"internal/AGENTS.md": "Scoped worker guidance.",
		"other/AGENTS.md":    "Unrelated guide content.",
		"internal/main.go":   "package main\nfunc Run() {}\n",
		"README.md":          strings.Repeat("Long project context. ", 3000),
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, excluded := range [][]string{nil, {"AGENTS.md"}} {
		text, manifest, err := NewContextBuilder().BuildWithExcludedFiles(root, "internal/main.go", excluded)
		if err != nil {
			t.Fatal(err)
		}
		wantRoot := 1
		if len(excluded) != 0 {
			wantRoot = 0
		}
		if strings.Count(text, "Root cancellation guidance.") != wantRoot || strings.Count(text, "Scoped worker guidance.") != 1 || strings.Contains(text, "Unrelated guide content.") {
			t.Fatal("context lost guide scope or exclusions")
		}
		for _, file := range manifest.Included {
			if file.Path == "internal/AGENTS.md" && (file.Hash == "" || file.Truncated) {
				t.Fatal("guide manifest does not describe full guidance")
			}
		}
		if estimateTokens(text) > manifest.TokenLimit {
			t.Fatal("guides bypassed context budget")
		}
	}
}
