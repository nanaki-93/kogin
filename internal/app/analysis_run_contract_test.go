package app

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nanaki-93/kogin/v2/internal/project"
)

func TestAnalysisRunCoverageNeverTurnsMissingEvidenceIntoZeroFindings(t *testing.T) {
	zero, three := 0, 3
	for _, test := range []struct {
		name     string
		status   AnalysisRunStatus
		coverage AnalysisRunCoverage
		count    *int
		valid    bool
	}{
		{"queued", AnalysisRunQueued, AnalysisRunCoverage{Total: 2, Pending: 2}, nil, true},
		{"running with one empty report", AnalysisRunRunning, AnalysisRunCoverage{Total: 2, Succeeded: 1, Running: 1}, &zero, true},
		{"completed empty", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 2, Succeeded: 2}, &zero, true},
		{"completed with findings", AnalysisRunCompleted, AnalysisRunCoverage{Total: 2, Succeeded: 2}, &three, true},
		{"partial evidence", AnalysisRunPartial, AnalysisRunCoverage{Total: 3, Succeeded: 1, Partial: 1, Failed: 1}, &three, true},
		{"failed", AnalysisRunFailed, AnalysisRunCoverage{Total: 1, Failed: 1}, nil, true},
		{"unavailable", AnalysisRunUnavailable, AnalysisRunCoverage{Total: 1, Unavailable: 1}, nil, true},
		{"no eligible work", AnalysisRunUnavailable, AnalysisRunCoverage{}, nil, true},
		{"paused", AnalysisRunPaused, AnalysisRunCoverage{Total: 2, Pending: 1, Succeeded: 1}, &zero, true},
		{"interrupted", AnalysisRunInterrupted, AnalysisRunCoverage{Total: 1, Pending: 1}, nil, true},
		{"stale retained evidence", AnalysisRunStale, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &three, true},
		{"failed is not empty", AnalysisRunFailed, AnalysisRunCoverage{Total: 1, Failed: 1}, &zero, false},
		{"no work is not completed empty", AnalysisRunCompletedEmpty, AnalysisRunCoverage{}, &zero, false},
		{"coverage mismatch", AnalysisRunRunning, AnalysisRunCoverage{Total: 1, Pending: 2}, nil, false},
		{"negative coverage", AnalysisRunRunning, AnalysisRunCoverage{Total: 1, Pending: 2, Failed: -1}, nil, false},
		{"incomplete completed", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 2, Succeeded: 1, Skipped: 1}, &zero, false},
		{"lost successful count", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 1, Succeeded: 1}, nil, false},
		{"mislabeled empty", AnalysisRunCompleted, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &zero, false},
		{"mislabeled findings", AnalysisRunCompletedEmpty, AnalysisRunCoverage{Total: 1, Succeeded: 1}, &three, false},
		{"partial hides pending", AnalysisRunPartial, AnalysisRunCoverage{Total: 2, Succeeded: 1, Pending: 1}, &three, false},
		{"failed hides evidence", AnalysisRunFailed, AnalysisRunCoverage{Total: 2, Succeeded: 1, Failed: 1}, &three, false},
		{"failed hides pending", AnalysisRunFailed, AnalysisRunCoverage{Total: 2, Pending: 1, Failed: 1}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			progress := AnalysisSectionProgress{Category: project.FindingCategoryBugs, Status: test.status, Coverage: test.coverage, FindingCount: test.count}
			if err := progress.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid %t", err, test.valid)
			}
		})
	}
}

func TestAnalysisRunPreviewBindsTheWholeProjectWithoutTruncatingItToABatch(t *testing.T) {
	request := AnalysisPreviewRequest{ProjectID: "project", ProjectRevision: "revision", Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{BatchFiles: 100, BudgetSeconds: 900, MaxAttemptsPerStage: 2}}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Scope = "file"
	if err := request.Validate(); err == nil {
		t.Fatal("accepted file-scoped analysis")
	}
	request.Scope = AnalysisRunScopeProject
	for _, limits := range []AnalysisRunLimits{{}, {501, 900, 2}, {100, 3601, 2}, {100, 900, 5}} {
		request.Limits = limits
		if err := request.Validate(); err == nil {
			t.Fatalf("accepted invalid bounds: %+v", limits)
		}
	}
	identity := AnalysisQueueIdentity{ProjectID: "project", ProjectRevision: "revision", PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	runIdentity := AnalysisRunIdentity{AnalysisQueueIdentity: identity, ID: "run", Generation: "generation"}
	if err := runIdentity.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Limits = AnalysisRunLimits{100, 900, 2}
	request.ResumeRun = &runIdentity
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.ProjectRevision = "replacement"
	if err := request.Validate(); err == nil {
		t.Fatal("accepted resume preview for a replacement revision")
	}
	preview := AnalysisRunPreview{SchemaVersion: AnalysisRunSchemaVersion, PreviewID: "preview", Identity: identity, Scope: AnalysisRunScopeProject, Limits: AnalysisRunLimits{100, 900, 2}, Excluded: []AnalysisExcludedFile{}, Providers: []AnalysisProviderRequirement{{ID: "bug-provider", Stages: []AnalysisStage{AnalysisStageSemantic}, Model: EffectiveModel{Scope: "bug"}}}, ExpectedModelRequests: 600, MaxModelRequests: 1200}
	for index := 0; index < 600; index++ {
		preview.Files = append(preview.Files, AnalysisPlannedFile{AnalysisFileIdentity: AnalysisFileIdentity{Path: fmt.Sprintf("file-%03d.go", index), ContentHash: fmt.Sprintf("hash-%d", index), Language: "Go"}, SizeBytes: 40, Stages: []AnalysisStagePlan{{Stage: AnalysisStageSemantic, Eligible: true, ProviderID: "bug-provider", MaxModelRequests: 2}}})
	}
	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var restored AnalysisRunPreview
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Files) != 600 || restored.Limits.BatchFiles != 100 || restored.Files[599].ContentHash != "hash-599" {
		t.Fatalf("lost captured coverage: %+v", restored)
	}
	for _, forbidden := range []string{`"source"`, `"content"`, `"prompt"`, `"confirmations"`, `"api_key"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("preview contains %s", forbidden)
		}
	}
}

func TestAnalysisRunAdmissionAndControlRequireCompleteIdentityAndFreshIntent(t *testing.T) {
	queue := AnalysisQueueIdentity{ProjectID: "project", ProjectRevision: "revision", PolicyFingerprint: "policy", ProviderFingerprint: "providers", QueueID: "queue"}
	identity := AnalysisRunIdentity{AnalysisQueueIdentity: queue, ID: "run", Generation: "generation"}
	start := AnalysisRunStartRequest{Identity: queue, PreviewID: "preview", Limits: AnalysisRunLimits{100, 900, 2}}
	if err := start.Validate(); err != nil {
		t.Fatal(err)
	}
	start.PreviewID = ""
	if err := start.Validate(); err == nil {
		t.Fatal("accepted start without preview")
	}
	confirmation := &AnalysisRunConfirmations{ProviderIDs: []string{}, SecurityReview: true}
	for _, test := range []struct {
		name          string
		action        AnalysisRunAction
		preview       string
		confirmations *AnalysisRunConfirmations
		valid         bool
	}{
		{"pause", AnalysisRunPause, "", nil, true},
		{"cancel", AnalysisRunCancel, "", nil, true},
		{"resume", AnalysisRunResume, "fresh-preview", confirmation, true},
		{"resume without preview", AnalysisRunResume, "", confirmation, false},
		{"resume without intent", AnalysisRunResume, "fresh-preview", nil, false},
		{"pause with intent", AnalysisRunPause, "", confirmation, false},
		{"cancel with preview", AnalysisRunCancel, "fresh-preview", nil, false},
		{"unknown action", "retry", "", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := AnalysisRunControlRequest{Identity: identity, Action: test.action, PreviewID: test.preview, Confirmations: test.confirmations}
			if err := request.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid %t", err, test.valid)
			}
		})
	}
	for _, guard := range []*string{&identity.ProjectID, &identity.ProjectRevision, &identity.PolicyFingerprint, &identity.ProviderFingerprint, &identity.QueueID, &identity.ID, &identity.Generation} {
		previous := *guard
		*guard = ""
		if err := (AnalysisRunControlRequest{Identity: identity, Action: AnalysisRunCancel}).Validate(); err == nil {
			t.Fatal("accepted control with an incomplete identity")
		}
		*guard = previous
	}
}

func TestAnalysisRunKeepsConsentTransientAndProducerEvidenceTyped(t *testing.T) {
	confirmation := AnalysisRunConfirmations{ProviderIDs: []string{"analyze-provider", "bug-provider"}, SecurityReview: true}
	start, err := json.Marshal(AnalysisRunStartRequest{Confirmations: confirmation})
	if err != nil || !strings.Contains(string(start), `"security_review":true`) {
		t.Fatalf("missing explicit Security intent: %s, %v", start, err)
	}
	persisted, err := json.Marshal(AnalysisRun{SchemaVersion: AnalysisRunSchemaVersion, Status: AnalysisRunInterrupted})
	if err != nil || strings.Contains(string(persisted), `"confirmations"`) || strings.Contains(string(persisted), `"provider_ids"`) {
		t.Fatalf("durable consent: %s, %v", persisted, err)
	}
	if got := AnalysisStageSemantic.Categories(); !reflect.DeepEqual(got, []project.FindingCategory{project.FindingCategoryBugs, project.FindingCategoryPerformance, project.FindingCategorySecurity}) {
		t.Fatalf("semantic consumers = %v", got)
	}
	if got := AnalysisStageSecurityRules.Categories(); !reflect.DeepEqual(got, []project.FindingCategory{project.FindingCategorySecurity}) {
		t.Fatalf("Security consumers = %v", got)
	}
}

func TestAnalysisRunOpenAPIContractsAreRegisteredAndResolvable(t *testing.T) {
	data, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, planned := document["x-unified-analysis-contract"]; planned {
		t.Fatal("registered operations remain in a planned extension")
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/projects/current/analysis/preview", "/api/projects/current/analysis/run", "/api/projects/current/analysis/run/control"} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("missing live route: %s", path)
		}
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	category := schemas["FindingCategory"].(map[string]any)["enum"]
	if !reflect.DeepEqual(category, []any{"bugs", "performance", "security"}) {
		t.Fatalf("category schema = %v", category)
	}
	properties := schemas["AnalysisRunPreview"].(map[string]any)["properties"].(map[string]any)
	if _, truncated := properties["files"].(map[string]any)["maxItems"]; truncated {
		t.Fatal("preview schema truncates project inventory")
	}
	var checkRefs func(any)
	checkRefs = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
				var node any = document
				for _, part := range parts {
					object, ok := node.(map[string]any)
					if !ok {
						t.Fatalf("unresolved reference %s", ref)
					}
					node = object[part]
				}
				if node == nil {
					t.Fatalf("unresolved reference %s", ref)
				}
			}
			for _, child := range value {
				checkRefs(child)
			}
		case []any:
			for _, child := range value {
				checkRefs(child)
			}
		}
	}
	checkRefs(paths)
	for name, schema := range schemas {
		if strings.HasPrefix(name, "Analysis") {
			checkRefs(schema)
		}
	}
}
