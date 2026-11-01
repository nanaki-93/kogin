package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
)

type mockChatClient struct{}

func (mockChatClient) ChatWithJSONSchema(context.Context, []llm.ChatMessage, llm.JSONSchema) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.ChatMessage{
		Role: "assistant", Content: `{"purpose":"A small test project.","architecture":"One Go package.","components":["main package"],"walkthroughs":[],"risks":[]}`,
	}}}}, nil
}

type recordingChatClient struct{ messages []llm.ChatMessage }

func (c *recordingChatClient) ChatWithJSONSchema(ctx context.Context, messages []llm.ChatMessage, schema llm.JSONSchema) (*llm.ChatResponse, error) {
	c.messages = append([]llm.ChatMessage(nil), messages...)
	return mockChatClient{}.ChatWithJSONSchema(ctx, messages, schema)
}

func TestResolveFileRejectsSiblingPrefixAndSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	sibling := filepath.Join(parent, "app2")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(sibling, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveFile(root, filepath.Join("..", "app2", "secret.txt")); err == nil {
		t.Fatal("expected sibling-prefix traversal to be rejected")
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveFile(root, "link.txt"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestAnalyzerWritesCanonicalReportAndContextIncludesInventory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/app\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "internal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "service.go"), []byte("package internal\n\nfunc Work() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	analysis, err := analyzeProjectForTest(context.Background(), NewAnalyzerWithProvenance(mockChatClient{}, "", "analysis", "", ""), root)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Type != "go" || analysis.FileCount != 3 || analysis.SourceFileCount != 2 {
		t.Fatalf("unexpected analysis: %+v", analysis)
	}
	if analysis.Report.Status != ProjectAnalysisStatusFresh || analysis.Report.Purpose != "A small test project." {
		t.Fatalf("structured report = %+v", analysis.Report)
	}
	if _, err := os.Stat(filepath.Join(root, projectAnalysisReportPath)); err != nil {
		t.Fatalf("canonical report was not persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kasuri", "analysis.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy markdown report was generated: %v", err)
	}

	contextText, err := NewContextBuilder().Build(root, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"main.go", "internal/service.go", "func main()", "func Work()"} {
		if !strings.Contains(contextText, expected) {
			t.Errorf("context missing %q", expected)
		}
	}
}

func TestGetFileInfoRejectsLargeAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := GetFileInfo(root, "binary.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Binary || info.Content != "" {
		t.Fatalf("expected binary file without content, got %+v", info)
	}
	large := make([]byte, maxFileViewBytes+1)
	for i := range large {
		large[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), large, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := GetFileInfo(root, "large.txt"); err == nil {
		t.Fatal("expected oversized file to be rejected")
	}
}

func TestContextPolicyExcludesSecretsIgnoredAndGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".gitignore":        "ignored.go\n",
		".kasuriignore":     "private/**\n",
		"main.go":           "package main\nfunc main() {}\n",
		".env":              "DATABASE_PASSWORD=must-not-leak\n",
		"private/server.go": "package private\nconst Key = \"must-not-leak\"\n",
		"ignored.go":        "package ignored\nconst Token = \"must-not-leak\"\n",
		"config.yaml":       "api_key: must-not-leak\n",
		"tls.key":           "must-not-leak\n",
		"app.min.js":        "const secret = 'must-not-leak'\n",
		"package-lock.json": "must-not-leak\n",
	}
	for path, content := range files {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", "config.yaml", "tls.key", "app.min.js", "package-lock.json", "ignored.go", "private/server.go"} {
		if decision := policy.Decide(path); decision.Include || decision.Reason == "" {
			t.Errorf("policy decision for %s = %+v, want excluded with reason", path, decision)
		}
	}
	if decision := policy.Decide("main.go"); !decision.Include {
		t.Fatalf("main.go unexpectedly excluded: %+v", decision)
	}

	client := &recordingChatClient{}
	if _, err := analyzeProjectForTest(context.Background(), NewAnalyzerWithProvenance(client, "", "analysis", "", ""), root); err != nil {
		t.Fatal(err)
	}
	var prompt strings.Builder
	for _, message := range client.messages {
		prompt.WriteString(message.Content)
	}
	if strings.Contains(prompt.String(), "must-not-leak") {
		t.Fatalf("excluded content reached model prompt: %s", prompt.String())
	}
	if !strings.Contains(prompt.String(), "func main()") {
		t.Fatalf("eligible source did not reach model prompt: %s", prompt.String())
	}
}

func TestContextPolicyExcludesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\nconst Secret = \"must-not-leak\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.go")); err != nil {
		t.Fatal(err)
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if decision := policy.Decide("linked.go"); decision.Include || !strings.Contains(decision.Reason, "symlink") {
		t.Fatalf("symlink decision = %+v, want exclusion", decision)
	}
}

func TestContextPolicyProjectOverrides(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".kasuri"), 0755); err != nil {
		t.Fatal(err)
	}
	config := `{"include":["ignored.go"],"exclude":["drafts/**"]}`
	if err := os.WriteFile(filepath.Join(root, ".kasuri", "context-policy.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0644); err != nil {
		t.Fatal(err)
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if decision := policy.Decide("ignored.go"); !decision.Include || decision.Reason != "project context-policy include" {
		t.Fatalf("include override = %+v", decision)
	}
	if decision := policy.Decide("drafts/main.go"); decision.Include || decision.Reason != "project context-policy exclude" {
		t.Fatalf("exclude override = %+v", decision)
	}
}

func TestContextBuilderRejectsExcludedAndSymlinkTargetsBeforeReadingSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package main\n// must-not-reach-context\nfunc Ignored() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n// must-not-reach-context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.go")); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"ignored.go", "linked.go"} {
		contextText, manifest, err := NewContextBuilder().BuildWithManifest(root, target)
		if err == nil {
			t.Fatalf("target %q was accepted", target)
		}
		if strings.Contains(contextText, "must-not-reach-context") {
			t.Fatalf("target %q leaked excluded source: %s", target, contextText)
		}
		if len(manifest.Excluded) == 0 {
			t.Fatalf("target %q manifest omitted excluded-path provenance: %+v", target, manifest)
		}
	}
}

func TestContextBuilderMarksOversizedTargetSnippetTruncated(t *testing.T) {
	root := t.TempDir()
	content := "package main\n\nfunc Run() {\n" + strings.Repeat("// bounded source\n", maxTargetBytes/8) + "}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	contextText, manifest, err := NewContextBuilder().BuildWithManifest(root, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Truncated || len(manifest.Included) != 1 || !manifest.Included[0].Truncated {
		t.Fatalf("oversized target manifest = %+v", manifest)
	}
	if len(contextText) > maxContextBytes {
		t.Fatalf("context length = %d, limit = %d", len(contextText), maxContextBytes)
	}
}

func TestContextBuilderKeepsSnippetHeadersInsideTheContextByteLimit(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 30; index++ {
		path := filepath.Join(root, fmt.Sprintf("source-%02d.go", index))
		content := "package main\n" + strings.Repeat("// bounded context\n", maxSnippetBytes/8)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	contextText, manifest, err := NewContextBuilder().BuildWithManifest(root, "source-00.go")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Truncated {
		t.Fatalf("large context was not marked truncated: %+v", manifest)
	}
	if len(contextText) > maxContextBytes {
		t.Fatalf("context length = %d, limit = %d", len(contextText), maxContextBytes)
	}
}

func TestContextBuilderBoundsAnInventoryOnlyOverflow(t *testing.T) {
	root := t.TempDir()
	prefix := strings.Repeat("inventory-", 8)
	for index := 0; index < 700; index++ {
		path := filepath.Join(root, fmt.Sprintf("%s%04d.txt", prefix, index))
		if err := os.WriteFile(path, []byte("metadata only\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	contextText, manifest, err := NewContextBuilder().BuildWithManifest(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Truncated {
		t.Fatalf("inventory overflow was not marked truncated: %+v", manifest)
	}
	if len(contextText) > maxContextBytes || manifest.EstimatedTokens > maxContextTokens || estimateTokens(contextText) > maxContextTokens {
		t.Fatalf("inventory bounds bytes=%d/%d tokens=%d/%d", len(contextText), maxContextBytes, manifest.EstimatedTokens, maxContextTokens)
	}
	if !strings.HasPrefix(contextText, "## Bounded project file inventory\n") || strings.Contains(contextText, "Complete project file inventory") {
		t.Fatalf("inventory heading = %q", contextText[:min(len(contextText), 80)])
	}
	if strings.Contains(contextText, "## File:") {
		t.Fatalf("inventory-only fixture unexpectedly emitted snippets: %q", contextText)
	}
	lastPath := fmt.Sprintf("%s%04d.txt", prefix, 699)
	if contextManifestHasPath(manifest, lastPath) {
		t.Fatalf("manifest included an inventory path not present in the prompt: %q", lastPath)
	}
	if !contextManifestHasExclusion(manifest, lastPath, "bounded context inventory") {
		t.Fatalf("manifest did not explain the omitted inventory path: %+v", manifest.Excluded)
	}
}

func TestContextBuilderKeepsTargetSourceWhenInventoryTruncates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.go"), []byte("package main\n\nfunc Target() { println(\"selected\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix := strings.Repeat("inventory-", 8)
	for index := 0; index < 200; index++ {
		path := filepath.Join(root, fmt.Sprintf("%s%04d.txt", prefix, index))
		if err := os.WriteFile(path, []byte("metadata only\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	contextText, manifest, err := NewContextBuilder().BuildWithManifest(root, "target.go")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Truncated || len(manifest.Included) == 0 || manifest.Included[0].Path != "target.go" {
		t.Fatalf("target inventory manifest = %+v", manifest)
	}
	if !strings.Contains(contextText, "## File: target.go") || !strings.Contains(contextText, "func Target()") {
		t.Fatalf("target source was not retained after inventory truncation: %q", contextText)
	}
	lastPath := fmt.Sprintf("%s%04d.txt", prefix, 199)
	if contextManifestHasPath(manifest, lastPath) || !contextManifestHasExclusion(manifest, lastPath, "bounded context inventory") {
		t.Fatalf("target overflow provenance = included:%+v excluded:%+v", manifest.Included, manifest.Excluded)
	}
	if len(contextText) > maxContextBytes || manifest.EstimatedTokens > maxContextTokens {
		t.Fatalf("target context bounds bytes=%d/%d tokens=%d/%d", len(contextText), maxContextBytes, manifest.EstimatedTokens, maxContextTokens)
	}
}

func contextManifestHasPath(manifest ContextManifest, path string) bool {
	for _, file := range manifest.Included {
		if file.Path == path {
			return true
		}
	}
	return false
}

func contextManifestHasExclusion(manifest ContextManifest, path, reason string) bool {
	for _, decision := range manifest.Excluded {
		if decision.Path == path && strings.Contains(decision.Reason, reason) {
			return true
		}
	}
	return false
}
