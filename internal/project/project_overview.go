package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project/handbook"
	"github.com/nanaki-93/mini-orca/v2/internal/project/projectmap"
	"github.com/nanaki-93/mini-orca/v2/internal/project/symbols"
)

const (
	maxFileViewBytes = 1024 * 1024
	maxProjectFiles  = 20000
)

var ignoredProjectDirectories = map[string]bool{
	".git": true, ".gradle": true, ".idea": true, ".mini-orca": true, ".worktrees": true,
	"node_modules": true, "vendor": true, "build": true, "dist": true,
	"target": true, "out": true, ".next": true, ".cache": true,
}

// ErrAnalysisPublicationBlocked lets an admitted client preserve a fatal host
// failure instead of converting it to the defined model-failure inventory view.
var ErrAnalysisPublicationBlocked = errors.New("project interpretation publication blocked")

type projectAnalysisClient interface {
	ChatWithJSONSchema(context.Context, []llm.ChatMessage, llm.JSONSchema) (*llm.ChatResponse, error)
}

type Analysis = handbook.Analysis

// FileInfo describes one selected project file.
type FileInfo struct {
	Path        string    `json:"path"`
	ContentHash string    `json:"content_hash"`
	Name        string    `json:"name"`
	Extension   string    `json:"extension"`
	Language    string    `json:"language"`
	SizeBytes   int64     `json:"size_bytes"`
	LineCount   int       `json:"line_count"`
	ModifiedAt  time.Time `json:"modified_at"`
	Binary      bool      `json:"binary"`
	Content     string    `json:"content,omitempty"`
}

// Analyzer scans projects and asks the configured model for an architectural summary.
type Analyzer struct {
	// contextNote goes before the project context, such as the components'
	// summaries an analysis run wrote.
	contextNote string
	// keepSaved: a failed or unavailable interpretation leaves the saved
	// report on disk, as when the overview of an analyzed project is written
	// again.
	keepSaved       bool
	llm             projectAnalysisClient
	model           string
	profile         string
	scope           string
	providerOrigin  string
	reasoningEffort string
}

// NewAnalyzerWithProvenance records the effective scope and sanitized provider
// origin alongside the configured model for cache freshness.
func NewAnalyzerWithProvenance(client projectAnalysisClient, model, scope, providerOrigin, reasoningEffort string) *Analyzer {
	profile := scope
	if profile == "" {
		profile = "analysis"
	}
	if scope == "" {
		scope = profile
	}
	return &Analyzer{llm: client, model: model, profile: profile, scope: scope, providerOrigin: providerOrigin, reasoningEffort: reasoningEffort}
}

// AnalyzeSnapshot enriches a captured inventory. Callers can admit model work
// against its exact identity without making project facts depend on a runtime.
// WithContextNote adds text the model reads before the project context.
func (a *Analyzer) WithContextNote(note string) *Analyzer {
	a.contextNote = note
	return a
}

// KeepingSavedOnFailure makes a failed interpretation leave the saved report
// as it is: writing the overview again must not trade a good one for a
// failure that a restart would then show.
func (a *Analyzer) KeepingSavedOnFailure() *Analyzer {
	a.keepSaved = true
	return a
}

func (a *Analyzer) AnalyzeSnapshot(ctx context.Context, analysis *Analysis) (*Analysis, error) {
	if err := validateAnalysisSnapshot(analysis); err != nil {
		return nil, err
	}
	policy, err := NewContextPolicy(analysis.Path)
	if err != nil {
		return nil, err
	}

	contextText, err := NewContextBuilder().Build(analysis.Path, "")
	if err != nil {
		return nil, fmt.Errorf("build analysis context: %w", err)
	}
	if a.contextNote != "" {
		contextText = a.contextNote + "\n\n" + contextText
	}
	report, err := a.interpret(ctx, analysis, contextText)
	if err != nil {
		return nil, err
	}
	analysis.Report = report
	analysis.AIStatus = report.Status
	analysis.Summary = projectAnalysisSummary(report)

	if err := validateAnalysisSnapshot(analysis); err != nil {
		return nil, err
	}
	currentPolicy, err := NewContextPolicy(analysis.Path)
	if err != nil {
		return nil, err
	}
	if currentPolicy.Version() != policy.Version() {
		return nil, ErrRevisionConflict
	}
	if a.keepSaved && report.Status != ProjectAnalysisStatusFresh {
		return analysis, nil
	}
	if err := StoreProjectAnalysisReport(analysis.Path, report); err != nil {
		return nil, err
	}
	return analysis, nil
}

// interpret asks the model for the overview of a snapshot. A failed or
// unusable reply is a report with its failure; only a blocked publication is
// an error.
func (a *Analyzer) interpret(ctx context.Context, analysis *Analysis, contextText string) (ProjectAnalysisReport, error) {
	report := newProjectAnalysisReportWithProvenance(analysis.ProjectID, analysis.ProjectRevision, a.model, a.profile, a.scope, a.providerOrigin, a.reasoningEffort)
	if a.llm == nil {
		report.Status = ProjectAnalysisStatusUnavailable
		report.Failure = "AI analysis is unavailable because no LLM client is configured."
		return report, nil
	}
	response, chatErr := a.llm.ChatWithJSONSchema(ctx, projectAnalysisMessages(contextText), projectAnalysisResponseSchema())
	if errors.Is(chatErr, ErrAnalysisPublicationBlocked) {
		return report, chatErr
	}
	if failure := projectAnalysisResponseFailure(ctx, response, chatErr); failure != "" {
		report.Status = ProjectAnalysisStatusFailed
		report.Failure = failure + " The factual project inventory below is still valid."
		return report, nil
	}
	parsed, parseErr := parseProjectAnalysisResponse(response.Choices[0].Message.Content)
	if parseErr != nil {
		report.Status = ProjectAnalysisStatusFailed
		report.Failure = "AI analysis returned an unusable structured report. The factual project inventory below is still valid."
		return report, nil
	}
	report.Purpose = parsed.Purpose
	report.Architecture = parsed.Architecture
	report.Components = parsed.Components
	report.Walkthroughs = checkWalkthroughs(analysis.Path, analysis.Files, parsed.Walkthroughs)
	report.Risks = parsed.Risks
	report.EngineeringInsight = CloneEngineeringInsight(parsed.EngineeringInsight)
	if response.Model != "" {
		report.Model = response.Model
	}
	return report, nil
}

func projectAnalysisResponseFailure(ctx context.Context, response *llm.ChatResponse, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "AI analysis timed out before completing the report. Retry or increase the import timeout."
	case ctx.Err() != nil:
		return "AI analysis was canceled before completing the report."
	case errors.Is(err, llm.ErrStructuredRequestRejected):
		return "AI analysis provider rejected the structured report request. Check the Analyze model and provider settings."
	case errors.Is(err, llm.ErrGenerationRefused):
		return "AI analysis provider declined to return a project report."
	case errors.Is(err, llm.ErrGenerationIncomplete):
		return "AI analysis did not finish normally. Retry the project description."
	case err != nil || response == nil || len(response.Choices) == 0:
		return "AI analysis could not be completed."
	}
	choice := response.Choices[0]
	if choice.Message.Refusal != "" || choice.FinishReason == "content_filter" {
		return "AI analysis provider declined to return a project report."
	}
	switch choice.FinishReason {
	case "length":
		return "AI analysis reached the provider's output limit before completing the report. Increase the Analyze model's output limit and retry."
	case "", "stop":
		// Some compatible providers omit finish_reason on a successful reply.
		return ""
	default:
		return "AI analysis did not finish normally. Retry the project description."
	}
}

// StoreLocalInventory makes a newly created workspace reopenable without an
// import-time provider request. The absent interpretation stays unavailable.
func (a *Analyzer) StoreLocalInventory(ctx context.Context, root string) error {
	analysis, err := ScanProject(ctx, root)
	if err != nil {
		return err
	}
	report := newProjectAnalysisReportWithProvenance(analysis.ProjectID, analysis.ProjectRevision, a.model, a.profile, a.scope, a.providerOrigin, a.reasoningEffort)
	report.Status = ProjectAnalysisStatusUnavailable
	report.Failure = "This worktree has not been analyzed. Run project analysis to create its report."
	return StoreProjectAnalysisReport(analysis.Path, report)
}

// Restore reloads deterministic facts and the persisted project interpretation
// without contacting a model or rewriting project artifacts.
func (a *Analyzer) Restore(root string) (*Analysis, error) {
	analysis, err := ScanProject(context.Background(), root)
	if err != nil {
		return nil, err
	}
	report, err := LoadProjectAnalysisReport(analysis.Path, ProjectAnalysisInput{
		ProjectID:       analysis.ProjectID,
		ProjectRevision: analysis.ProjectRevision,
		Model:           a.model,
		Profile:         a.profile,
		Scope:           a.scope,
		ProviderOrigin:  a.providerOrigin,
		ReasoningEffort: a.reasoningEffort,
		PromptVersion:   projectAnalysisPromptVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("load stored project analysis: %w", err)
	}
	if report == nil {
		fallback := newProjectAnalysisReportWithProvenance(analysis.ProjectID, analysis.ProjectRevision, a.model, a.profile, a.scope, a.providerOrigin, a.reasoningEffort)
		fallback.Status = ProjectAnalysisStatusUnavailable
		fallback.Failure = "Project analysis has not run yet. Run project analysis to create its report."
		report = &fallback
	}
	analysis.Report = *report
	analysis.AIStatus = report.Status
	analysis.Summary = projectAnalysisSummary(*report)
	analysis.AnalyzedAt = report.GeneratedAt
	return analysis, nil
}

// ScanProject captures deterministic facts and source identity without a model.
func ScanProject(ctx context.Context, root string) (*Analysis, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	policy, err := NewContextPolicy(canonical)
	if err != nil {
		return nil, err
	}
	analysis, err := scanWithPolicy(ctx, canonical, policy)
	if err != nil {
		return nil, err
	}
	analysis.ProjectID = projectID(canonical)
	analysis.ProjectRevision, err = projectRevision(canonical)
	if err != nil {
		return nil, err
	}
	return analysis, nil
}

func validateAnalysisSnapshot(analysis *Analysis) error {
	if analysis == nil || analysis.ProjectID != projectID(analysis.Path) {
		return ErrRevisionConflict
	}
	revision, err := projectRevision(analysis.Path)
	if err != nil {
		return err
	}
	if revision != analysis.ProjectRevision {
		return ErrRevisionConflict
	}
	return nil
}

func scanWithPolicy(ctx context.Context, root string, policy *ContextPolicy) (*Analysis, error) {
	result := &Analysis{
		Name: filepath.Base(root), Path: root, Type: "unknown",
		Languages: make(map[string]int), AnalyzedAt: time.Now().UTC(),
	}
	detection := detectProject(root)
	result.Type = detection.Type
	result.BuildFile = detection.BuildFile
	paths, err := WalkProjectFiles(ctx, ProjectWalkOptions{
		Root: root, Policy: policy, IgnoredDirectories: ignoredProjectDirectories, IncludeSymlinkFiles: true, MaxFiles: maxProjectFiles,
	})
	if err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	result.Files = paths
	result.FileCount = len(paths)
	for _, relativePath := range paths {
		language := detectLanguage(relativePath)
		if language == "Text" {
			continue
		}
		result.SourceFileCount++
		result.Languages[language]++
		path, err := ResolveFile(root, relativePath)
		if err != nil {
			continue
		}
		data, err := readLimited(path, maxFileViewBytes)
		if err == nil && !isBinary(data) {
			result.TotalLines += countLines(data)
		}
	}
	return result, nil
}

func GetFileInfo(root, relative string) (*FileInfo, error) {
	fullPath, err := ResolveFile(root, relative)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file")
	}
	if stat.Size() > maxFileViewBytes {
		return nil, fmt.Errorf("file is larger than %d bytes", maxFileViewBytes)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}
	binary := isBinary(data)
	info := &FileInfo{
		Path: filepath.ToSlash(filepath.Clean(relative)), Name: filepath.Base(relative),
		Extension: strings.TrimPrefix(strings.ToLower(filepath.Ext(relative)), "."),
		Language:  detectLanguage(relative), SizeBytes: stat.Size(), ModifiedAt: stat.ModTime(), Binary: binary,
	}
	if !binary {
		info.LineCount = countLines(data)
		info.Content = string(data)
	}
	info.ContentHash = contentHash(data)
	return info, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	return bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(sample)
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	lines := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}

func detectLanguage(path string) string {
	return symbols.DetectLanguage(path)
}

type ProjectDocsComponent = handbook.ProjectDocsComponent
type ProjectDocsReport = handbook.ProjectDocsReport

type ProjectDocsGraph = handbook.ProjectDocsGraph
type DocsInputs = handbook.DocsInputs
type ModuleSummary = handbook.ModuleSummary

// DocsComponents are the components the docs show for a project map.
func DocsComponents(m projectmap.Map) ([]projectmap.Component, []projectmap.Edge) {
	return handbook.DocsComponents(m)
}

// BuildProjectDocs builds the project's docs from its map, its checks and
// the model's summaries.
func BuildProjectDocs(in DocsInputs) ProjectDocsReport {
	return handbook.BuildProjectDocs(in)
}
