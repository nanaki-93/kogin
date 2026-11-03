package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFileAnalysisCategoriesRoundTripAndLegacyReportsRemainReadable(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	input := analysisCacheInput("main.go", "hash")
	legacy := newFreshAnalysis(input)
	legacy.Purpose = "Historical explanation"
	legacy.Risks = []Finding{{Severity: "high", Summary: "Slow and insecure bug text does not establish a category."}}
	if err := cache.Store(legacy); err != nil {
		t.Fatal(err)
	}
	path := cache.cachePath(input.Path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), `"category"`) {
		t.Fatalf("invented a legacy category: %s", before)
	}
	currentInput := input
	currentInput.PromptVersion = "categorized-prompt-v2"
	loaded, err := cache.Load(currentInput)
	if err != nil || loaded.Status != AnalysisStatusStale || loaded.Purpose != legacy.Purpose || loaded.Risks[0].Category != FindingCategoryUnclassified {
		t.Fatalf("legacy read = %+v, %v", loaded, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("legacy read rewrote stored evidence")
	}
	fresh := newFreshAnalysis(currentInput)
	fresh.Risks = []Finding{
		{Category: FindingCategoryBugs, Severity: "high", Summary: "Drops an error."},
		{Category: FindingCategoryPerformance, Severity: "medium", Summary: "Copies each request twice."},
		{Category: FindingCategorySecurity, Severity: "low", Summary: "Exposes internal paths."},
	}
	if err := cache.Store(fresh); err != nil {
		t.Fatal(err)
	}
	loaded, err = cache.Load(currentInput)
	if err != nil || loaded.Status != AnalysisStatusFresh {
		t.Fatalf("categorized read = %+v, %v", loaded, err)
	}
	for index, risk := range fresh.Risks {
		if loaded.Risks[index].Category != risk.Category {
			t.Fatalf("category lost: %+v", loaded.Risks)
		}
	}
	fresh.Risks[0].Category = "reliability"
	if err := cache.Store(fresh); err == nil {
		t.Fatal("stored an unknown category")
	}
	loaded, err = cache.Load(currentInput)
	if err != nil || loaded.Risks[0].Category != FindingCategoryBugs {
		t.Fatal("failed category write replaced valid evidence")
	}
	var decoded Finding
	if err := json.Unmarshal([]byte(`{"severity":"low","summary":"Legacy"}`), &decoded); err != nil || decoded.Category.Valid() {
		t.Fatalf("legacy decoding = %+v, %v", decoded, err)
	}
}

func TestFileAnalysisCacheRoundTripAndSourceFreePersistence(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	input := analysisCacheInput("main.go", "sha256:one")
	analysis := newFreshAnalysis(input)
	analysis.Purpose = "Runs the command."
	analysis.Responsibilities = []string{"dispatches work"}
	analysis.Risks = []Finding{{Severity: "high", Summary: "Check errors.", TaskSpec: &BugTaskSpec{SchemaVersion: BugTaskSpecSchemaVersion, TargetPath: "main.go", AcceptanceCriteria: []string{"Return errors."}, NonGoals: []string{}}}}
	if err := cache.Store(analysis); err != nil {
		t.Fatal(err)
	}
	loaded, err := cache.Load(input)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != AnalysisStatusFresh || loaded.Purpose != analysis.Purpose || loaded.Risks[0].TaskSpec == nil || loaded.Risks[0].TaskSpec.TargetPath != "main.go" {
		t.Fatalf("loaded analysis = %+v", loaded)
	}
	data, err := os.ReadFile(cache.cachePath("main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "package main\\nfunc Run() {}") {
		t.Fatalf("cache contains original source: %s", data)
	}
}

func TestFileAnalysisCacheInvalidatesOnlyChangedInputs(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "one.go", "package main\nfunc One() {}\n")
	writeIndexFixture(t, root, "two.go", "package main\nfunc Two() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	one := analysisCacheInput("one.go", "sha256:one")
	two := analysisCacheInput("two.go", "sha256:two")
	if err := cache.Store(newFreshAnalysis(one)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Store(newFreshAnalysis(two)); err != nil {
		t.Fatal(err)
	}

	changedOne := one
	changedOne.ContentHash = "sha256:one-changed"
	changedOne.ProjectRevision = "sha256:revision-after-one-change"
	loaded, err := cache.Load(changedOne)
	if err != nil || loaded.Status != AnalysisStatusStale {
		t.Fatalf("changed analysis = %+v, %v; want stale", loaded, err)
	}
	updatedTwo := two
	updatedTwo.ProjectRevision = changedOne.ProjectRevision
	loaded, err = cache.Load(updatedTwo)
	if err != nil || loaded.Status != AnalysisStatusFresh {
		t.Fatalf("unchanged analysis = %+v, %v; want fresh", loaded, err)
	}

	for _, mutate := range []func(*FileAnalysisInput){
		func(value *FileAnalysisInput) { value.Model = "new-model" },
		func(value *FileAnalysisInput) { value.Profile = "new-profile" },
		func(value *FileAnalysisInput) { value.Scope = "new-scope" },
		func(value *FileAnalysisInput) { value.ProviderOrigin = "https://new-provider.example" },
		func(value *FileAnalysisInput) { value.ReasoningEffort = "high" },
		func(value *FileAnalysisInput) { value.ContextPolicyVersion = "policy-v2" },
		func(value *FileAnalysisInput) { value.PromptVersion = "prompt-v2" },
	} {
		candidate := two
		mutate(&candidate)
		loaded, err := cache.Load(candidate)
		if err != nil || loaded.Status != AnalysisStatusStale {
			t.Fatalf("changed validity input = %+v, %v; want stale", loaded, err)
		}
	}
}

func TestFileAnalysisCacheAllowsProviderSelectedModel(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	input := analysisCacheInput("main.go", "sha256:one")
	input.Model = ""
	analysis := newFreshAnalysis(input)
	analysis.Model = "provider-selected-model"
	if err := cache.Store(analysis); err != nil {
		t.Fatal(err)
	}
	loaded, err := cache.Load(input)
	if err != nil || loaded.Status != AnalysisStatusFresh {
		t.Fatalf("provider-selected analysis = %+v, %v; want fresh", loaded, err)
	}
}

func TestFileAnalysisCacheInvalidatesWhenPolicyRulesChange(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	input := analysisCacheInput("main.go", "sha256:one")
	input.ContextPolicyVersion = policy.Version()
	if err := cache.Store(newFreshAnalysis(input)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".kogin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kogin", "context-policy.json"), []byte(`{"exclude":["generated/**"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	changedPolicy, err := NewContextPolicy(root)
	if err != nil {
		t.Fatal(err)
	}
	if changedPolicy.Version() == input.ContextPolicyVersion {
		t.Fatal("policy version did not change after context-policy update")
	}
	changedInput := input
	changedInput.ContextPolicyVersion = changedPolicy.Version()
	loaded, err := cache.Load(changedInput)
	if err != nil || loaded.Status != AnalysisStatusStale {
		t.Fatalf("policy-changed analysis = %+v, %v; want stale", loaded, err)
	}
}

func TestFileAnalysisCacheRecoversCorruptionAndConcurrentAccess(t *testing.T) {
	root := t.TempDir()
	writeIndexFixture(t, root, "main.go", "package main\nfunc Run() {}\n")
	cache, err := NewFileAnalysisCache(root)
	if err != nil {
		t.Fatal(err)
	}
	input := analysisCacheInput("main.go", "sha256:one")
	if err := os.MkdirAll(filepath.Dir(cache.cachePath(input.Path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.cachePath(input.Path), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := cache.Load(input)
	if err != nil || loaded.Status != AnalysisStatusMissing {
		t.Fatalf("recovered analysis = %+v, %v", loaded, err)
	}
	corrupt, err := filepath.Glob(cache.cachePath(input.Path) + ".corrupt-*")
	if err != nil || len(corrupt) != 1 {
		t.Fatalf("corrupt backup = %v, %v", corrupt, err)
	}

	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := cache.Store(newFreshAnalysis(input)); err != nil {
				t.Error(err)
			}
			if _, err := cache.Load(input); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	loaded, err = cache.Load(input)
	if err != nil || loaded.Status != AnalysisStatusFresh {
		t.Fatalf("concurrent cache result = %+v, %v", loaded, err)
	}
}

func analysisCacheInput(path, hash string) FileAnalysisInput {
	return FileAnalysisInput{ProjectID: "sha256:project", ProjectRevision: "sha256:revision", Path: path, ContentHash: hash, Language: "Go", Model: "local-model", Profile: "bug", Scope: "bug", ProviderOrigin: "http://localhost:11434", ReasoningEffort: "medium", PromptVersion: "prompt-v1", ContextPolicyVersion: "policy-v1"}
}

func newFreshAnalysis(input FileAnalysisInput) FileAnalysis {
	return FileAnalysis{SchemaVersion: fileAnalysisSchemaVersion, ProjectID: input.ProjectID, ProjectRevision: input.ProjectRevision, Path: input.Path, ContentHash: input.ContentHash, Language: input.Language, Status: AnalysisStatusFresh, Model: input.Model, ConfiguredModel: input.Model, Profile: input.Profile, Scope: input.Scope, ProviderOrigin: input.ProviderOrigin, ReasoningEffort: input.ReasoningEffort, PromptVersion: input.PromptVersion, ContextPolicyVersion: input.ContextPolicyVersion}
}

func (c *FileAnalysisCache) cachePath(relative string) string {
	return filepath.Join(c.root, fileAnalysisRelativeDir, analysisPathKey(normalizedAnalysisPath(relative))+".json")
}
