package project

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nanaki-93/kasuri/v2/internal/llm"
	"github.com/nanaki-93/kasuri/v2/internal/project/handbook"
	"github.com/nanaki-93/kasuri/v2/internal/storage"
)

const (
	projectAnalysisSchemaVersion = "1"
	// v7: short prose, walkthroughs checked against the files, no diagrams.
	projectAnalysisPromptVersion = "project-analysis-v7"
	projectAnalysisReportPath    = ".kasuri/project-analysis.json"
	maxProjectAnalysisBytes      = 64 * 1024
	maxProjectAnalysisItems      = 32
	maxProjectAnalysisItemBytes  = 2048
	// What the docs keep of the walkthroughs, in characters; the prompt asks
	// for less and the schema allows more (see projectAnalysisResponseSchema).
	maxWalkthroughs        = 6
	maxWalkthroughTitle    = 80
	maxWalkthroughSummary  = 300
	maxWalkthroughSteps    = 8
	maxWalkthroughStepText = 240
	maxWalkthroughPath     = 300
	maxWalkthroughSymbol   = 120

	ProjectAnalysisStatusFresh       = handbook.ProjectAnalysisStatusFresh
	ProjectAnalysisStatusFailed      = handbook.ProjectAnalysisStatusFailed
	ProjectAnalysisStatusUnavailable = handbook.ProjectAnalysisStatusUnavailable
	ProjectAnalysisStatusStale       = handbook.ProjectAnalysisStatusStale
)

type ProjectAnalysisRisk = handbook.ProjectAnalysisRisk
type ProjectAnalysisReport = handbook.ProjectAnalysisReport

// ProjectAnalysisInput identifies the model and project state a persisted
// report must match before it may be presented as fresh.
type ProjectAnalysisInput struct {
	ProjectID       string
	ProjectRevision string
	Model           string
	Profile         string
	Scope           string
	ProviderOrigin  string
	ReasoningEffort string
	PromptVersion   string
}

type ProjectWalkthrough = handbook.Walkthrough
type ProjectWalkthroughStep = handbook.WalkthroughStep

type projectAnalysisResponse struct {
	Purpose            string                `json:"purpose"`
	Architecture       string                `json:"architecture"`
	Components         []string              `json:"components"`
	Walkthroughs       []ProjectWalkthrough  `json:"walkthroughs"`
	Risks              []ProjectAnalysisRisk `json:"risks"`
	EngineeringInsight *EngineeringInsight   `json:"engineering_insight,omitempty"`
}

type projectAnalysisWireResponse struct {
	Purpose      string                    `json:"purpose"`
	Architecture string                    `json:"architecture"`
	Components   []string                  `json:"components"`
	Walkthroughs []ProjectWalkthrough      `json:"walkthroughs"`
	Risks        []projectAnalysisRiskWire `json:"risks"`
	Insight      json.RawMessage           `json:"engineering_insight"`
}

type projectAnalysisRiskWire struct {
	Severity string          `json:"severity"`
	Summary  string          `json:"summary"`
	Insight  json.RawMessage `json:"engineering_insight"`
}

func newProjectAnalysisReportWithProvenance(projectID, revision, model, profile, scope, providerOrigin, reasoningEffort string) ProjectAnalysisReport {
	return ProjectAnalysisReport{
		SchemaVersion: projectAnalysisSchemaVersion,
		ProjectID:     projectID, ProjectRevision: revision,
		Components: []string{}, Walkthroughs: []ProjectWalkthrough{}, Risks: []ProjectAnalysisRisk{},
		Status: ProjectAnalysisStatusFresh, Model: model, ConfiguredModel: model, Profile: profile, Scope: scope, ProviderOrigin: providerOrigin, ReasoningEffort: reasoningEffort, PromptVersion: projectAnalysisPromptVersion,
		GeneratedAt: time.Now().UTC(),
	}
}

// projectOverviewPromptInstructions say what each part of the overview is
// for: docs a developer new to the project reads first.
const projectOverviewPromptInstructions = "Write for a developer who is new to this project and wants to find their way around it. Use plain sentences, no Markdown, no diagrams, no marketing. " +
	"purpose: two or three sentences on what the project is, who uses it and what it does. " +
	"architecture: two to four sentences on how the main parts fit together, naming them by their project-relative directory paths. " +
	"components: the main packages or modules, each as 'path (Readable name): one sentence on its responsibility', using actual project-relative directory paths from the context; at most 16. " +
	"walkthroughs: the three to six most important things the project does (a command, a request, a background job), in the order a newcomer should read them. Each has a title a user would recognize (a short phrase such as 'Add a password' or 'Serve an API request', at most 60 characters), a one-sentence summary, and two to eight steps in execution order. " +
	"Each step is one short sentence on what happens, the project-relative path of the file where it happens (only files listed in the context), and the name of the function, method or type there (Type.Method for a method), or an empty string when you are not sure of it. "

func projectAnalysisMessages(contextText string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: "You are a software architect. Analyze only the supplied project facts and context. Return exactly one JSON object matching the supplied schema and no Markdown or prose: purpose (string), architecture (string), components (string array), walkthroughs ({title,summary,steps:[{text,path,symbol}]} array), risks ({severity,summary,engineering_insight} array), engineering_insight (null or {mechanism,why_it_matters_here,tradeoff_or_failure_mode,transferable_lesson}). " + projectOverviewPromptInstructions + EngineeringInsightPromptInstructions + "Use null for omitted insights at both report and risk level; otherwise include all four non-empty insight fields, at most 250 Unicode characters each. Risk severity must be low, medium, or high; at most 32 risks of at most 2048 UTF-8 bytes each. Keep the complete JSON response within 64 KiB. Do not invent files, functions or dependencies. Risks are suggestions, not verified findings."},
		{Role: "user", Content: contextText},
	}
}

func parseProjectAnalysisResponse(output string) (projectAnalysisResponse, error) {
	if len(output) == 0 || len(output) > maxProjectAnalysisBytes {
		return projectAnalysisResponse{}, fmt.Errorf("project analysis response is empty or too large")
	}
	response, err := decodeProjectAnalysisResponse(output)
	if err != nil {
		return response, err
	}
	if err := validateProjectAnalysisSummary(&response); err != nil {
		return response, err
	}
	if err := validateProjectAnalysisRisks(response.Risks); err != nil {
		return response, err
	}
	return response, nil
}

func decodeProjectAnalysisResponse(output string) (projectAnalysisResponse, error) {
	var wire projectAnalysisWireResponse
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return projectAnalysisResponse{}, fmt.Errorf("parse project analysis JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return projectAnalysisResponse{}, fmt.Errorf("project analysis JSON must contain one object")
	}
	response := projectAnalysisResponse{Purpose: wire.Purpose, Architecture: wire.Architecture, Components: wire.Components, Walkthroughs: wire.Walkthroughs}
	response.EngineeringInsight, _ = ParseOptionalEngineeringInsight(wire.Insight)
	response.Risks = make([]ProjectAnalysisRisk, 0, len(wire.Risks))
	for _, risk := range wire.Risks {
		insight, _ := ParseOptionalEngineeringInsight(risk.Insight)
		response.Risks = append(response.Risks, ProjectAnalysisRisk{Severity: risk.Severity, Summary: risk.Summary, EngineeringInsight: insight})
	}
	return response, nil
}

func validateProjectAnalysisSummary(response *projectAnalysisResponse) error {
	response.Purpose = strings.TrimSpace(response.Purpose)
	response.Architecture = strings.TrimSpace(response.Architecture)
	if response.Purpose == "" || response.Architecture == "" || len(response.Purpose) > maxProjectAnalysisItemBytes || len(response.Architecture) > maxProjectAnalysisItemBytes {
		return fmt.Errorf("project analysis JSON requires purpose and architecture")
	}
	if err := validateProjectAnalysisStrings("components", response.Components); err != nil {
		return err
	}
	response.Walkthroughs = boundedWalkthroughs(response.Walkthroughs)
	return nil
}

func validateProjectAnalysisRisks(risks []ProjectAnalysisRisk) error {
	if len(risks) > maxProjectAnalysisItems {
		return fmt.Errorf("project analysis risks exceed limit")
	}
	for index := range risks {
		risk := &risks[index]
		risk.Severity = strings.ToLower(strings.TrimSpace(risk.Severity))
		risk.Summary = strings.TrimSpace(risk.Summary)
		if !validProjectAnalysisSeverity(risk.Severity) || risk.Summary == "" || len(risk.Summary) > maxProjectAnalysisItemBytes {
			return fmt.Errorf("project analysis contains an invalid risk")
		}
	}
	return nil
}

func validateProjectAnalysisStrings(name string, values []string) error {
	if len(values) > maxProjectAnalysisItems {
		return fmt.Errorf("project analysis %s exceed limit", name)
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if values[index] == "" || len(values[index]) > maxProjectAnalysisItemBytes {
			return fmt.Errorf("project analysis contains an invalid %s value", name)
		}
	}
	return nil
}

func validProjectAnalysisSeverity(value string) bool {
	return value == "low" || value == "medium" || value == "high"
}

// LoadProjectAnalysisReport returns a stored report. A valid report with
// changed project, model, profile, or prompt inputs is marked stale.
func LoadProjectAnalysisReport(root string, input ProjectAnalysisInput) (*ProjectAnalysisReport, error) {
	path, err := storage.MetadataPath(root, projectAnalysisReportPath)
	if err != nil {
		return nil, fmt.Errorf("read project analysis report: %w", err)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project analysis report: %w", err)
	}
	var report ProjectAnalysisReport
	if err := json.Unmarshal(data, &report); err != nil || !validStoredProjectAnalysisReport(report) {
		return nil, fmt.Errorf("parse project analysis report")
	}
	if !projectAnalysisReportMatches(report, input) && report.Status == ProjectAnalysisStatusFresh {
		report.Status = ProjectAnalysisStatusStale
	}
	return cloneProjectAnalysisReport(&report), nil
}

// StoreProjectAnalysisReport atomically replaces the report for one analysis
// outcome. It persists unavailable and failed states with their truthful reason.
func StoreProjectAnalysisReport(root string, report ProjectAnalysisReport) error {
	if !validStoredProjectAnalysisReport(report) {
		return fmt.Errorf("project analysis report is invalid")
	}
	path, err := storage.MetadataPath(root, projectAnalysisReportPath)
	if err != nil {
		return fmt.Errorf("store project analysis report: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project analysis report: %w", err)
	}
	if err := storage.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("store project analysis report: %w", err)
	}
	return nil
}

func validStoredProjectAnalysisReport(report ProjectAnalysisReport) bool {
	if report.SchemaVersion != projectAnalysisSchemaVersion || report.ProjectID == "" || report.ProjectRevision == "" || report.Profile == "" || report.PromptVersion == "" || report.GeneratedAt.IsZero() {
		return false
	}
	switch report.Status {
	case ProjectAnalysisStatusFresh:
		return report.Purpose != "" && report.Architecture != "" && report.Failure == ""
	case ProjectAnalysisStatusFailed, ProjectAnalysisStatusUnavailable:
		return report.Failure != ""
	case ProjectAnalysisStatusStale:
		return true
	default:
		return false
	}
}

func projectAnalysisReportMatches(report ProjectAnalysisReport, input ProjectAnalysisInput) bool {
	scope := input.Scope
	if scope == "" {
		scope = input.Profile
	}
	configuredModel := report.ConfiguredModel
	if configuredModel == "" {
		configuredModel = report.Model
	}
	return report.ProjectID == input.ProjectID && report.ProjectRevision == input.ProjectRevision && configuredModel == input.Model && report.Profile == input.Profile && report.Scope == scope && report.ProviderOrigin == input.ProviderOrigin && report.ReasoningEffort == input.ReasoningEffort && report.PromptVersion == input.PromptVersion
}

func cloneProjectAnalysisReport(source *ProjectAnalysisReport) *ProjectAnalysisReport {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Components = append([]string(nil), source.Components...)
	copy.Walkthroughs = make([]ProjectWalkthrough, len(source.Walkthroughs))
	for index, walkthrough := range source.Walkthroughs {
		walkthrough.Steps = append([]ProjectWalkthroughStep(nil), walkthrough.Steps...)
		copy.Walkthroughs[index] = walkthrough
	}
	copy.Risks = append([]ProjectAnalysisRisk(nil), source.Risks...)
	for index := range copy.Risks {
		copy.Risks[index].EngineeringInsight = CloneEngineeringInsight(source.Risks[index].EngineeringInsight)
	}
	copy.EngineeringInsight = CloneEngineeringInsight(source.EngineeringInsight)
	return &copy
}

func projectAnalysisSummary(report ProjectAnalysisReport) string {
	if report.Status != ProjectAnalysisStatusFresh {
		return report.Failure
	}
	return report.Purpose + "\n\n" + report.Architecture
}
