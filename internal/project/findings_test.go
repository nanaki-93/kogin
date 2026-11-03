package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindingClassificationPreservesLegacyIDsTriageAndProvenance(t *testing.T) {
	for _, status := range []string{FindingStatusDismissed, FindingStatusFixed} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewFindingStore(root)
			if err != nil {
				t.Fatal(err)
			}
			input := FindingInput{ProjectID: "project", ProjectRevision: "revision", FileHashes: map[string]string{"main.go": "hash"}}
			analysis := FileAnalysis{Path: "main.go", ContentHash: "hash", Status: AnalysisStatusFresh, Risks: []Finding{{Severity: "high", Summary: "Missing authorization check."}}}
			legacy := normalizeFinding(SuggestedFindingsForFile(analysis)[0], input)
			legacy.Status = status
			data, err := json.Marshal(findingDocument{SchemaVersion: findingStoreSchema, Findings: []UnifiedFinding{legacy}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), `"category"`) {
				t.Fatal("legacy fixture unexpectedly classified")
			}
			if err := os.MkdirAll(filepath.Join(root, ".kasuri"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, findingStorePath), data, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(input)
			if err != nil || len(loaded) != 1 || loaded[0].Category.Valid() || loaded[0].Status != status {
				t.Fatalf("legacy finding = %+v, %v", loaded, err)
			}
			analysis.Risks[0].Category = FindingCategorySecurity
			analysis.Risks[0].Title = "Missing authorization"
			reported := SuggestedFindingsForFile(analysis)
			current, err := store.ReconcileSource(input, FindingSourceAI, reported)
			if err != nil || len(current) != 1 {
				t.Fatalf("classified reconciliation = %+v, %v", current, err)
			}
			got := current[0]
			if got.Title != "Missing authorization" || got.Message != legacy.Message {
				t.Fatalf("title update lost finding content: %+v", got)
			}
			if got.ID != legacy.ID || got.Status != status || got.Category != FindingCategorySecurity || got.Source != FindingSourceAI || got.Confidence != FindingConfidenceSuggested || got.Severity != "high" {
				t.Fatalf("classification changed identity/triage/provenance: %+v", got)
			}
			reported[0].Category = "correctness"
			if _, err := store.ReconcileSource(input, FindingSourceAI, reported); err == nil {
				t.Fatal("accepted unknown category")
			}
			loaded, err = store.Load(input)
			if err != nil || loaded[0].Category != FindingCategorySecurity || loaded[0].Status != status {
				t.Fatal("invalid update replaced stored triage")
			}
		})
	}
}

func TestFindingIDAndValidationKeepSourcesDistinct(t *testing.T) {
	base := UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: "medium", Title: "Suggestion", Message: "Check input", Location: FindingLocation{Path: "main.go", StartLine: 4, EndLine: 4}}
	for _, test := range []struct {
		name  string
		value UnifiedFinding
		valid bool
	}{
		{name: "AI suggested", value: base, valid: true},
		{name: "tool reported", value: UnifiedFinding{Source: FindingSourceVet, Confidence: FindingConfidenceToolReported, Severity: "high", Title: "vet", Message: "bad format"}, valid: true},
		{name: "AI cannot claim a tool", value: UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceToolReported, Severity: "low", Title: "bad", Message: "bad"}, valid: false},
		{name: "tool cannot claim suggestion", value: UnifiedFinding{Source: FindingSourceTest, Confidence: FindingConfidenceSuggested, Severity: "low", Title: "bad", Message: "bad"}, valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := normalizeFinding(test.value, FindingInput{ProjectID: "project", ProjectRevision: "revision"})
			err := validateFinding(value)
			if (err == nil) != test.valid {
				t.Fatalf("validateFinding(%+v) error = %v, want valid %t", value, err, test.valid)
			}
		})
	}
	equivalent := UnifiedFinding{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: "medium", Title: "Suggestion", Message: "Check input", Location: FindingLocation{Path: "main.go", StartLine: 4, EndLine: 4}}
	id := FindingID(base)
	if id == "" || id != FindingID(equivalent) {
		t.Fatalf("finding ID must be stable for equivalent findings: %q, %q", id, FindingID(equivalent))
	}
	changed := equivalent
	changed.Message = "Check a different input"
	if id == FindingID(changed) {
		t.Fatal("finding ID did not include finding content")
	}
}

func TestFindingStoreReconcilesTriageSanitizationAndFreshness(t *testing.T) {
	root := t.TempDir()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := FindingInput{ProjectID: "project", ProjectRevision: "revision-one", FileHashes: map[string]string{"main.go": "hash-one"}}
	reported := []UnifiedFinding{{Source: FindingSourceAI, Confidence: FindingConfidenceSuggested, Severity: "medium", Title: "Risk", Message: "password: should-not-persist", Evidence: "api_key: should-not-persist", FileHash: "hash-one", Location: FindingLocation{Path: "main.go"}, OriginatingAnalysis: "file", TaskSpec: &BugTaskSpec{SchemaVersion: BugTaskSpecSchemaVersion, TargetPath: "main.go", AcceptanceCriteria: []string{"password: should-not-persist"}, NonGoals: []string{}}}}
	stored, err := store.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Status != FindingStatusOpen || stored[0].Freshness != FindingFreshnessFresh || strings.Contains(stored[0].Message+stored[0].Evidence+stored[0].TaskSpec.AcceptanceCriteria[0], "should-not-persist") {
		t.Fatalf("stored findings = %+v", stored)
	}
	if err := store.SetStatus(stored[0].ID, FindingStatusDismissed); err != nil {
		t.Fatal(err)
	}
	rerun, err := store.ReconcileSource(input, FindingSourceAI, reported)
	if err != nil || rerun[0].Status != FindingStatusDismissed {
		t.Fatalf("triage after unchanged rerun = %+v, %v", rerun, err)
	}
	stale, err := store.Load(FindingInput{ProjectID: "project", ProjectRevision: "revision-two", FileHashes: map[string]string{"main.go": "hash-two"}})
	if err != nil || stale[0].Freshness != FindingFreshnessStale {
		t.Fatalf("changed input findings = %+v, %v", stale, err)
	}
	// Left out of a report, a dismissed finding stays dismissed (so a repeat
	// of it is recognized); any other retires.
	kept, err := store.ReconcileSource(input, FindingSourceAI, nil)
	if err != nil || kept[0].Status != FindingStatusDismissed || kept[0].Freshness != FindingFreshnessStale {
		t.Fatalf("dismissed finding left out = %+v, %v", kept, err)
	}
	if err := store.SetStatus(stored[0].ID, FindingStatusOpen); err != nil {
		t.Fatal(err)
	}
	retired, err := store.ReconcileSource(input, FindingSourceAI, nil)
	if err != nil || retired[0].Status != FindingStatusRetired || retired[0].Freshness != FindingFreshnessStale {
		t.Fatalf("retired findings = %+v, %v", retired, err)
	}
}

func TestFindingStoreRecoversCorruptStorageAndAdaptsFreshAIRisks(t *testing.T) {
	root := t.TempDir()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, findingStorePath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(FindingInput{ProjectID: "project", ProjectRevision: "revision"})
	if err != nil || len(loaded) != 0 {
		t.Fatalf("recovered findings = %+v, %v", loaded, err)
	}
	corrupt, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(corrupt) != 1 {
		t.Fatalf("corrupt recovery = %v, %v", corrupt, err)
	}
	projectFindings := SuggestedFindingsForProject(ProjectAnalysisReport{Status: ProjectAnalysisStatusFresh, Risks: []ProjectAnalysisRisk{{Severity: "low", Summary: "Add tests"}}})
	task := &BugTaskSpec{SchemaVersion: BugTaskSpecSchemaVersion, TargetPath: "main.go", AcceptanceCriteria: []string{"Return errors."}}
	fileFindings := SuggestedFindingsForFile(FileAnalysis{
		Status:      AnalysisStatusFresh,
		Path:        "main.go",
		ContentHash: "hash",
		Risks:       []Finding{{Severity: "high", Summary: "Check errors in Run", TaskSpec: task}},
		Suggestions: []Suggestion{{Title: "Propagate Run error", Summary: "Return error to caller", Action: "Wrap and return the error from Run instead of ignoring it."}},
	})
	if projectFindings[0].Confidence != FindingConfidenceSuggested || projectFindings[0].Source != FindingSourceAI || fileFindings[0].Location.Path != "main.go" || fileFindings[0].FileHash != "hash" || fileFindings[0].TaskSpec == nil {
		t.Fatalf("adapted findings = %+v %+v", projectFindings, fileFindings)
	}
	if fileFindings[0].EngineeringInsight == nil || !strings.Contains(fileFindings[0].EngineeringInsight.TransferableLesson, "Wrap and return the error") {
		t.Fatalf("expected SuggestedFindingsForFile to attach matched suggestion action to EngineeringInsight, got %+v", fileFindings[0].EngineeringInsight)
	}
}

func TestFindingStoreDismissedTargetsPersistAcrossProducerTypesAndReopen(t *testing.T) {
	root := t.TempDir()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	semTarget := FindingTarget{ProjectID: "p1", Kind: "semantic", ID: "finding:sem-1", Path: "main.go"}
	perfTarget := FindingTarget{ProjectID: "p1", Kind: "performance", ID: "perf:1", Path: "perf.go"}
	secTarget := FindingTarget{ProjectID: "p1", Kind: "security", ID: "sec:1", Path: "sec.go", Source: SecuritySourceDeterministic}

	for _, target := range []FindingTarget{semTarget, perfTarget, secTarget} {
		if err := store.SetTargetStatus(target, FindingStatusDismissed, ""); err != nil {
			t.Fatalf("SetTargetStatus(%+v, dismissed): %v", target, err)
		}
	}
	dismissed, err := dismissedFindingTargets(store, "p1")
	if err != nil || len(dismissed) != 3 {
		t.Fatalf("expected 3 dismissed targets, got %+v, err=%v", dismissed, err)
	}

	if err := store.SetTargetStatus(perfTarget, FindingStatusOpen, ""); err != nil {
		t.Fatalf("SetTargetStatus(open): %v", err)
	}
	dismissed, err = dismissedFindingTargets(store, "p1")
	if err != nil || len(dismissed) != 2 {
		t.Fatalf("expected 2 dismissed targets after reopening perfTarget, got %+v, err=%v", dismissed, err)
	}
}

// dismissedFindingTargets reads the persisted dismissed targets of one project.
func dismissedFindingTargets(store *FindingStore, projectID string) ([]FindingTarget, error) {
	document, err := store.readDocumentSnapshot()
	if err != nil {
		return nil, err
	}
	result := []FindingTarget{}
	for _, target := range document.Dismissed {
		if target.ProjectID == projectID {
			result = append(result, target)
		}
	}
	return result, nil
}

func TestRulesFindingsAreRefreshedInPlaceAndSurviveAnalysisResets(t *testing.T) {
	root := t.TempDir()
	store, err := NewFindingStore(root)
	if err != nil {
		t.Fatal(err)
	}
	input := FindingInput{ProjectID: "project", ProjectRevision: "r1", FileHashes: map[string]string{"big.py": "h1"}}
	debt := func(evidence string) UnifiedFinding {
		return UnifiedFinding{Category: FindingCategoryMaintainability, Source: FindingSourceRules, Confidence: FindingConfidenceToolReported,
			Severity: "low", Rule: "code-budget", Title: "Refactor big.py", Message: "big.py is over its budgets.", Evidence: evidence,
			FileHash: "h1", Location: FindingLocation{Path: "big.py"}}
	}
	if err := store.RecordFindings(input, FindingSourceRules, []UnifiedFinding{debt("file has 900 lines (max 800)")}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(input)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("findings = %+v, %v", loaded, err)
	}
	if err := store.SetStatus(loaded[0].ID, FindingStatusDismissed); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFindings(input, FindingSourceRules, []UnifiedFinding{debt("file has 950 lines (max 800)")}); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearExcept(nil); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(input)
	if err != nil || len(again) != 1 || again[0].ID != loaded[0].ID || again[0].Status != FindingStatusDismissed ||
		again[0].Evidence != "file has 950 lines (max 800)" || !again[0].DetectedAt.Equal(loaded[0].DetectedAt) {
		t.Fatalf("refreshed finding = %+v, %v", again, err)
	}
	invalid := debt("x")
	invalid.Source = FindingSourceAI
	invalid.Confidence = FindingConfidenceSuggested
	if err := store.RecordFindings(input, FindingSourceAI, []UnifiedFinding{invalid}); err == nil {
		t.Fatal("an AI finding was accepted with the maintainability category")
	}
}
