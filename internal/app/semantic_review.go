package app

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const (
	semanticAnalysisPromptVersion = "file-analysis-v17"
	maxSemanticAnalysisBytes      = 64 * 1024
	fileAnalysisInsightFieldCount = 4
	fileAnalysisInsightMaxChars   = 250
)

const fileAnalysisResponseSchemaName = "file_analysis_response"

// fileAnalysisResponseSchemaDocument mirrors the wire response accepted by
// parseSemanticReviewResponse. Target-specific checks (such as the target file path)
// remain in the parser because they depend on the selected file.
const fileAnalysisResponseSchemaDocument = `{
  "type":"object",
  "additionalProperties":false,
  "required":["purpose","responsibilities","dependencies","side_effects","risks","suggestions"],
  "properties":{
    "purpose":{"type":"string","minLength":1},
    "responsibilities":{"type":"array","maxItems":3,"items":{"type":"string","minLength":1}},
    "dependencies":{"type":"array","maxItems":3,"items":{"type":"string","minLength":1}},
    "side_effects":{"type":"array","maxItems":3,"items":{"type":"string","minLength":1}},
    "risks":{"type":"array","maxItems":3,"items":{"$ref":"#/$defs/risk"}},
    "suggestions":{"type":"array","maxItems":3,"items":{"$ref":"#/$defs/suggestion"}},
    "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
  },
  "$defs":{
    "insight":{
      "type":"object",
      "additionalProperties":false,
      "required":["mechanism","why_it_matters_here","tradeoff_or_failure_mode","transferable_lesson"],
      "properties":{
        "mechanism":{"$ref":"#/$defs/insightText"},
        "why_it_matters_here":{"$ref":"#/$defs/insightText"},
        "tradeoff_or_failure_mode":{"$ref":"#/$defs/insightText"},
        "transferable_lesson":{"$ref":"#/$defs/insightText"}
      }
    },
    "insightText":{"type":"string","minLength":1,"maxLength":250},
    "optionalInsight":{"anyOf":[{"$ref":"#/$defs/insight"},{"type":"null"}]},
    "taskSpec":{
      "type":"object",
      "additionalProperties":false,
      "required":["schema_version","target_path","acceptance_criteria","non_goals"],
      "properties":{
        "schema_version":{"enum":["2"]},
        "target_path":{"type":"string","minLength":1,"maxLength":1024},
        "acceptance_criteria":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"string","minLength":1,"maxLength":512}},
        "non_goals":{"type":"array","maxItems":8,"items":{"type":"string","minLength":1,"maxLength":512}},
        "go_test_candidate":{"anyOf":[{"$ref":"#/$defs/goTestCandidate"},{"type":"null"}]}
      }
    },
    "goTestCandidate":{
      "type":"object",
      "additionalProperties":false,
      "required":["name","content"],
      "properties":{"name":{"type":"string","minLength":1,"maxLength":512},"content":{"type":"string","minLength":1,"maxLength":16384}}
    },
    "risk":{
      "type":"object",
      "additionalProperties":false,
      "required":["category","severity","title","summary"],
      "properties":{
        "category":{"enum":["bugs","performance","security"]},
        "severity":{"enum":["low","medium","high"]},
        "title":{"type":"string","minLength":1,"maxLength":60},
        "summary":{"type":"string","minLength":1},
        "task_spec":{"anyOf":[{"$ref":"#/$defs/taskSpec"},{"type":"null"}]},
        "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
      }
    },
    "suggestion":{
      "type":"object",
      "additionalProperties":false,
      "required":["title","summary"],
      "properties":{
        "title":{"type":"string","minLength":1},
        "summary":{"type":"string","minLength":1},
        "action":{"type":"string","minLength":1},
        "engineering_insight":{"$ref":"#/$defs/optionalInsight"}
      }
    }
  }
}`

const fileAnalysisInsightGuidance = "Ground selected-file behavior and engineering insights in TARGET_SOURCE. Use TARGET_FACTS, PROJECT_FACTS, and CONTEXT_MANIFEST only for the identity and context they supply. Apply these factual boundaries to the whole final response, including purpose, risks, suggestions, explanations, task specs, and engineering insights. " +
	"State visible behavior as fact only when TARGET_SOURCE demonstrates it. Separate local guarantees from downstream behavior that this file cannot establish, and state conditional concerns as conditions rather than facts. You may explain consequences demonstrated by visible local control or data flow. Do not infer unshown downstream safeguards, callers, implementations, or consequences; do not invent files or source. " +
	"Use the shared four-field contract to explain an observable relationship or invariant, why it matters locally, a real limitation or failure mode, and a concrete test with expected observations. An engineering insight can describe a correct existing behavior and does not require a proposed change or discovered defect. " +
	"Default to tests that characterize current behavior: state what the code currently does and the expected observation. If you suggest a proposed-change test, label it proposed-change and first state the current behavior it would change; do not portray a failing proposed expectation as a present contract. " +
	"Prefer one concise file-level insight where it is genuinely useful. Intentionally omit it for trivial code without a meaningful observable relationship or when the evidence cannot support a specific explanation; do not give generic tests or advice. Do not duplicate that lesson in risks or suggestions. "

const fileAnalysisInsightSchema = "At every supported engineering_insight location (the top level, each risks[] item, and each suggestions[] item), either omit engineering_insight or use null, or provide exactly one object with exactly these four non-empty string fields and no other keys: mechanism, why_it_matters_here, tradeoff_or_failure_mode, and transferable_lesson. Each field is limited to 250 Unicode characters, intentionally keeping generated insight prose concise; the four fields together fit the parser's 1,000-normalized-rune budget. Shape: {\"mechanism\":\"...\",\"why_it_matters_here\":\"...\",\"tradeoff_or_failure_mode\":\"...\",\"transferable_lesson\":\"...\"}. Never use a string, array, or partial object. "

const analysisFindingTitleGuidance = "Give each risk or finding a distinct, content-specific title of 2–6 words and at most 60 characters, such as 'Missing input validation' or 'Repeated allocations'. Name the concern itself; do not use a filename, a generic analysis label, or a full explanation as its title. "

// EngineeringInsightPromptVersion returns the production selected-file prompt
// identity used by evaluation; callers cannot supply an unrelated label.
func EngineeringInsightPromptVersion() string { return semanticAnalysisPromptVersion }

// FileAnalysisResponseSchema returns the one strict structured-output contract
// shared by production selected-file analysis and its evaluation runner.
func FileAnalysisResponseSchema() llm.JSONSchema {
	return llm.JSONSchema{Name: fileAnalysisResponseSchemaName, Schema: json.RawMessage(fileAnalysisResponseSchemaDocument)}
}

type semanticAnalysisResponse struct {
	Diagnostics        FileAnalysisDiagnostics     `json:"-"`
	Purpose            string                      `json:"purpose"`
	Responsibilities   []string                    `json:"responsibilities"`
	Dependencies       []string                    `json:"dependencies"`
	SideEffects        []string                    `json:"side_effects"`
	Risks              []project.Finding           `json:"risks"`
	Suggestions        []project.Suggestion        `json:"suggestions"`
	EngineeringInsight *project.EngineeringInsight `json:"engineering_insight,omitempty"`
}

// semanticAnalysisWireResponse keeps the optional task_spec untrusted until its
// target has been checked against the indexed file. A malformed suggestion must
// not discard the rest of an otherwise usable file summary.
type semanticAnalysisWireResponse struct {
	Purpose            string                       `json:"purpose"`
	Responsibilities   []string                     `json:"responsibilities"`
	Dependencies       []string                     `json:"dependencies"`
	SideEffects        []string                     `json:"side_effects"`
	Risks              []semanticAnalysisFinding    `json:"risks"`
	Suggestions        []semanticAnalysisSuggestion `json:"suggestions"`
	EngineeringInsight json.RawMessage              `json:"engineering_insight"`
}

type semanticAnalysisFinding struct {
	Category project.FindingCategory `json:"category"`
	Severity string                  `json:"severity"`
	Title    string                  `json:"title"`
	Summary  string                  `json:"summary"`
	TaskSpec json.RawMessage         `json:"task_spec"`
	Insight  json.RawMessage         `json:"engineering_insight"`
}

type semanticAnalysisSuggestion struct {
	Title   string          `json:"title"`
	Summary string          `json:"summary"`
	Action  string          `json:"action"`
	Insight json.RawMessage `json:"engineering_insight"`
}

type preparedFileAnalysis struct {
	root         string
	runtime      modelRuntime
	analysis     *project.Analysis
	indexedFile  *project.IndexFile
	source       string
	cache        *project.FileAnalysisCache
	input        project.FileAnalysisInput
	instructions project.EffectiveInstructions
}

func (s *Service) prepareFileAnalysis(targetFile string) (preparedFileAnalysis, error) {
	indexedFile, err := s.manager.IndexedFile(targetFile)
	if err != nil {
		return preparedFileAnalysis{}, err
	}
	if indexedFile.Binary {
		return preparedFileAnalysis{}, fmt.Errorf("target file must be text")
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		return preparedFileAnalysis{}, err
	}
	fileInfo, err := project.GetFileInfo(s.manager.Root(), indexedFile.Path)
	if err != nil {
		return preparedFileAnalysis{}, err
	}
	if fileInfo.ContentHash != indexedFile.ContentHash {
		return preparedFileAnalysis{}, project.ErrRevisionConflict
	}
	cache, input, err := s.fileAnalysisCacheInput(analysis, indexedFile, indexedFile.ContentHash)
	if err != nil {
		return preparedFileAnalysis{}, err
	}
	instructions, err := project.ResolveInstructions(s.manager.Root(), indexedFile.Path)
	if err != nil {
		return preparedFileAnalysis{}, err
	}
	return preparedFileAnalysis{root: s.manager.Root(), runtime: s.semanticCacheRuntime(analysis), analysis: analysis, indexedFile: indexedFile, source: fileInfo.Content, cache: cache, input: input, instructions: instructions}, nil
}

// Request and validation are separate from publication so a project run can
// retain prior evidence on failure and hold its own publication authority.
func (s *Service) requestFileAnalysis(ctx context.Context, prepared preparedFileAnalysis, dispatch *analysisModelDispatch) (*project.FileAnalysis, error) {
	index, err := s.manager.Index()
	if err != nil {
		return nil, err
	}
	runtime := prepared.runtime
	manifest := semanticInstructionManifest(*prepared.indexedFile, prepared.instructions)
	prompt, err := semanticPrompt(prepared.source, *prepared.analysis, index, *prepared.indexedFile, s.contextManifestForRuntime(manifest, runtime))
	if err != nil {
		return nil, err
	}
	if prepared.instructions.Text != "" {
		prompt += "\n\n" + project.InstructionPromptGuidance + prepared.instructions.Text
	}
	timed, cancel := context.WithTimeout(ctx, duration(runtime.effective.Timeout))
	defer cancel()
	schema := FileAnalysisResponseSchema()
	result, err := s.requestAnalysisModel(timed, runtime, []llm.ChatMessage{{Role: "user", Content: prompt}}, &schema, dispatch)
	if timed.Err() != nil {
		return nil, timed.Err()
	}
	if err != nil {
		return nil, &analysisModelError{err}
	}
	if err := project.ValidateInstructions(prepared.root, prepared.indexedFile.Path, prepared.instructions.Fingerprint); err != nil {
		return nil, err
	}
	parsed, err := parseSemanticReviewResponse(result.Content, *prepared.indexedFile, prepared.source)
	if err != nil {
		return nil, &analysisModelError{err}
	}
	fresh := newFileAnalysis(prepared, parsed)
	if model := result.Model; model != "" {
		fresh.Model = model
	}
	return &fresh, nil
}

func newFileAnalysis(prepared preparedFileAnalysis, parsed semanticAnalysisResponse) project.FileAnalysis {
	input, indexedFile := prepared.input, prepared.indexedFile
	fresh := project.FileAnalysis{
		SchemaVersion: "1", ProjectID: input.ProjectID, ProjectRevision: input.ProjectRevision,
		Path: input.Path, ContentHash: input.ContentHash, Language: input.Language,
		Purpose: parsed.Purpose, Responsibilities: parsed.Responsibilities,
		Imports: append([]string(nil), indexedFile.Imports...), Dependencies: parsed.Dependencies,
		SideEffects: parsed.SideEffects, Risks: parsed.Risks, Suggestions: parsed.Suggestions, EngineeringInsight: project.CloneEngineeringInsight(parsed.EngineeringInsight),
		Status: project.AnalysisStatusFresh,
		Model:  input.Model, ConfiguredModel: input.Model, Profile: input.Profile, Scope: input.Scope, ProviderOrigin: input.ProviderOrigin, ReasoningEffort: input.ReasoningEffort, PromptVersion: input.PromptVersion, ContextPolicyVersion: input.ContextPolicyVersion,
		GeneratedAt:             time.Now().UTC(),
		InstructionsFingerprint: prepared.instructions.Fingerprint,
	}
	return fresh
}

// CachedFileAnalysis returns the existing semantic state without contacting the model.
func (s *Service) CachedFileAnalysis(targetFile string) (*project.FileAnalysis, error) {
	indexedFile, err := s.manager.IndexedFile(targetFile)
	if err != nil {
		return nil, err
	}
	analysis, err := s.manager.Analysis()
	if err != nil {
		return nil, err
	}
	info, err := project.GetFileInfo(s.manager.Root(), indexedFile.Path)
	if err != nil {
		return nil, err
	}
	cache, input, err := s.fileAnalysisCacheInput(analysis, indexedFile, info.ContentHash)
	if err != nil {
		return nil, err
	}
	cached, err := cache.Load(input)
	if err != nil {
		return nil, err
	}
	if err := s.syncFileAnalysisStatus(input, cached.Status); err != nil {
		return nil, err
	}
	return cached, nil
}

func (s *Service) fileAnalysisCacheInput(analysis *project.Analysis, file *project.IndexFile, contentHash string) (*project.FileAnalysisCache, project.FileAnalysisInput, error) {
	policy, err := project.NewContextPolicy(s.manager.Root())
	if err != nil {
		return nil, project.FileAnalysisInput{}, err
	}
	cache, err := project.NewFileAnalysisCache(s.manager.Root())
	if err != nil {
		return nil, project.FileAnalysisInput{}, err
	}
	return cache, s.semanticCacheInput(analysis, file, contentHash, policy.Version()), nil
}

func (s *Service) semanticCacheInput(analysis *project.Analysis, file *project.IndexFile, contentHash, policyVersion string) project.FileAnalysisInput {
	return semanticCacheInputForRuntime(analysis, file, contentHash, policyVersion, s.semanticCacheRuntime(analysis))
}

func (s *Service) semanticCacheRuntime(analysis *project.Analysis) modelRuntime {
	if analysis != nil && s.analysisRun != nil {
		s.analysisRun.mu.Lock()
		var models *AnalysisModels
		if s.analysisRun.run != nil && s.analysisRun.run.Identity.ProjectID == analysis.ProjectID {
			models = cloneAnalysisModels(s.analysisRun.run.Plan.Models)
		} else if s.analysisRun.run == nil && s.manager != nil {
			if run, err := loadAnalysisRun(s.manager.Root()); err == nil && run != nil && run.Identity.ProjectID == analysis.ProjectID {
				models = cloneAnalysisModels(run.Plan.Models)
			}
		}
		s.analysisRun.mu.Unlock()
		if models.profile(AnalysisStageSemantic) != "" {
			return s.analysisModelRuntime(AnalysisStageSemantic, models)
		}
	}
	return s.conf().runtimes.bug
}

func semanticCacheInputForRuntime(analysis *project.Analysis, file *project.IndexFile, contentHash, policyVersion string, runtime modelRuntime) project.FileAnalysisInput {
	return project.FileAnalysisInput{ProjectID: analysis.ProjectID, ProjectRevision: analysis.ProjectRevision, Path: file.Path, ContentHash: contentHash, Language: file.Language, Model: runtime.profile.Model, Profile: runtime.effective.Profile, Scope: runtime.effective.Scope, ProviderOrigin: runtime.effective.ProviderOrigin, ReasoningEffort: runtime.effective.ReasoningEffort, PromptVersion: semanticAnalysisPromptVersion, ContextPolicyVersion: policyVersion}
}

func (s *Service) syncFileAnalysisStatus(input project.FileAnalysisInput, status string) error {
	return s.manager.UpdateFileAnalysisStatus(input.ProjectID, input.ProjectRevision, input.Path, status)
}

func semanticPrompt(source string, analysis project.Analysis, _ *project.ProjectIndex, target project.IndexFile, manifest project.ContextManifest) (string, error) {
	if len(source) > maxSemanticAnalysisBytes {
		return "", fmt.Errorf("selected file is too large for semantic analysis")
	}
	projectFacts := struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Summary string `json:"summary"`
	}{Name: analysis.Name, Type: analysis.Type, Summary: analysis.Summary}
	facts, err := json.Marshal(projectFacts)
	if err != nil {
		return "", err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	targetJSON, err := json.Marshal(struct {
		Path     string   `json:"path"`
		Language string   `json:"language"`
		Imports  []string `json:"imports"`
	}{target.Path, target.Language, target.Imports})
	if err != nil {
		return "", err
	}
	return "You summarize exactly one selected source file. Return one JSON object only; do not use Markdown or code fences. " +
		"Required fields: purpose (string), responsibilities (string array), dependencies (string array), side_effects (string array), risks ({category,severity,title,summary,task_spec?,engineering_insight?} array), suggestions ({title,summary,action?,engineering_insight?} array), engineering_insight? (see the shared engineering insight contract below). Keep each array to at most three concise items. " +
		analysisFindingTitleGuidance +
		fileAnalysisInsightSchema + project.EngineeringInsightPromptInstructions +
		"Risk severity must be low, medium, or high. " +
		fileAnalysisInsightGuidance +
		"Every risk must have exactly one category: bugs for incorrect behavior, performance for avoidable resource costs, or security for a trust-boundary weakness. Choose the category from the demonstrated failure mechanism, independently of severity. State the relevant local control/data flow and conditions in summary. Performance concerns are hypotheses unless measurements are supplied; never invent measured impact. For security, identify the visible input and sensitive operation; do not infer missing safeguards in an unseen callee. Omit concerns unsupported by TARGET_SOURCE. General cleanup, explanations and best-practice advice belong in suggestions, not risks; an empty risks array is valid. " +
		"Keep each insight field at or below 250 Unicode characters. Generic advice to use defer, handle errors, or follow best practices is not an insight. " +
		"Include task_spec when a risk has a concrete file-scoped fix; otherwise omit it. If you include one, it must have only these fields: schema_version \"2\", target_path copied exactly from TARGET_FACTS.path, acceptance_criteria (array), non_goals (array), and optional go_test_candidate {name,content}. Never target another file. " +
		"Treat all interpretations as suggestions. Do not quote source wholesale, invent files, or include source from another file.\n\n" +
		"PROJECT_FACTS:\n" + string(facts) + "\n\nCONTEXT_MANIFEST:\n" + string(manifestJSON) + "\n\nTARGET_FACTS:\n" + string(targetJSON) + "\n\nTARGET_SOURCE (the only source content supplied):\n```\n" + source + "\n```\n", nil
}

func semanticManifest(target project.IndexFile) project.ContextManifest {
	tokens := int((target.SizeBytes + 3) / 4)
	return project.ContextManifest{Included: []project.ContextFile{{Path: target.Path, SizeBytes: target.SizeBytes, Hash: target.ContentHash, Tokens: tokens}}, Excluded: []project.ContextDecision{}, EstimatedTokens: tokens, ByteLimit: maxSemanticAnalysisBytes, TokenLimit: maxSemanticAnalysisBytes / 4}
}

func semanticInstructionManifest(target project.IndexFile, instructions project.EffectiveInstructions) project.ContextManifest {
	manifest := semanticManifest(target)
	for _, file := range instructions.Files {
		tokens := (len([]rune(file.Content)) + 3) / 4
		manifest.Included = append(manifest.Included, project.ContextFile{Path: file.Path, SizeBytes: int64(len(file.Content)), Hash: file.Hash, Tokens: tokens})
		manifest.EstimatedTokens += tokens
	}
	manifest.Excluded = append(manifest.Excluded, instructions.Excluded...)
	manifest.ByteLimit += project.MaxInstructionBytes
	manifest.TokenLimit += project.MaxInstructionBytes / 4
	return manifest
}

func parseSemanticReviewResponse(output string, target project.IndexFile, source string) (semanticAnalysisResponse, error) {
	return validateSemanticAnalysisResponse(output, target, source, false)
}

func validateSemanticAnalysisResponse(output string, target project.IndexFile, source string, collectRejectedParentDiagnostics bool) (semanticAnalysisResponse, error) {
	var sizeErr error
	if len(output) == 0 || len(output) > maxSemanticAnalysisBytes {
		sizeErr = fmt.Errorf("semantic analysis response is empty or too large")
		if !collectRejectedParentDiagnostics {
			return semanticAnalysisResponse{}, sizeErr
		}
	}
	var wire semanticAnalysisWireResponse
	decodeErr := decodeSemanticAnalysis(output, &wire)
	if decodeErr != nil {
		if sizeErr != nil {
			return semanticAnalysisResponse{}, sizeErr
		}
		return semanticAnalysisResponse{}, decodeErr
	}
	parentErr := sizeErr
	if parentErr == nil {
		parentErr = validateSemanticAnalysisLimits(wire)
	}
	if parentErr != nil && !collectRejectedParentDiagnostics {
		return semanticAnalysisResponse{}, parentErr
	}
	// Evaluation historically records bounded field metadata for any decoded
	// parent, including parents rejected by size or collection limits. Normal
	// production requests retain their cheap rejection before section parsing.
	parsed := parseSemanticSections(wire, target, source)
	if parentErr != nil {
		return semanticAnalysisResponse{Diagnostics: parsed.Diagnostics}, parentErr
	}

	if err := validateSemanticRisks(parsed.Risks); err != nil {
		return semanticAnalysisResponse{Diagnostics: parsed.Diagnostics}, err
	}
	limitFileAnalysisInsights(&parsed.EngineeringInsight, parsed.Risks, parsed.Suggestions)
	return parsed, nil
}

func parseSemanticSections(wire semanticAnalysisWireResponse, target project.IndexFile, source string) semanticAnalysisResponse {
	parsed := semanticAnalysisResponse{
		Purpose: wire.Purpose, Responsibilities: wire.Responsibilities,
		Dependencies: wire.Dependencies, SideEffects: wire.SideEffects,
		Diagnostics: FileAnalysisDiagnostics{Insights: make([]FileAnalysisInsightDiagnostic, 0, 1+len(wire.Risks)+len(wire.Suggestions))},
	}
	parsed.EngineeringInsight = parsed.Diagnostics.parseInsight("top_level", nil, wire.EngineeringInsight)
	parsed.Risks = parseSemanticRisks(wire.Risks, target, source, &parsed.Diagnostics)
	parsed.Suggestions = parseSemanticSuggestions(wire.Suggestions, &parsed.Diagnostics)
	return parsed
}

func decodeSemanticAnalysis(output string, parsed *semanticAnalysisWireResponse) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(parsed); err != nil {
		return fmt.Errorf("parse semantic analysis JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("semantic analysis JSON must contain one object")
	}
	return nil
}

func validateSemanticAnalysisLimits(parsed semanticAnalysisWireResponse) error {
	if strings.TrimSpace(parsed.Purpose) == "" || len(parsed.Responsibilities) > 32 || len(parsed.Dependencies) > 64 || len(parsed.SideEffects) > 32 || len(parsed.Risks) > 32 || len(parsed.Suggestions) > 32 {
		return fmt.Errorf("semantic analysis JSON is incomplete or exceeds limits")
	}
	return nil
}

func parseSemanticRisks(risks []semanticAnalysisFinding, target project.IndexFile, source string, diagnostics *FileAnalysisDiagnostics) []project.Finding {
	parsed := make([]project.Finding, 0, len(risks))
	for index, risk := range risks {
		finding := project.Finding{
			Category:           risk.Category,
			Severity:           strings.ToLower(strings.TrimSpace(risk.Severity)),
			Title:              strings.Join(strings.Fields(risk.Title), " "),
			Summary:            strings.TrimSpace(risk.Summary),
			TaskSpec:           parseOptionalBugTaskSpec(risk.TaskSpec, target, source),
			EngineeringInsight: diagnostics.parseInsight("risk", &index, risk.Insight),
		}
		if len(risk.TaskSpec) > 0 && strings.TrimSpace(string(risk.TaskSpec)) != "null" && finding.TaskSpec == nil {
			diagnostics.TaskSpecDegradedRiskIndices = append(diagnostics.TaskSpecDegradedRiskIndices, index)
		}
		parsed = append(parsed, finding)
	}
	return parsed
}

func parseSemanticSuggestions(suggestions []semanticAnalysisSuggestion, diagnostics *FileAnalysisDiagnostics) []project.Suggestion {
	parsed := make([]project.Suggestion, 0, len(suggestions))
	for index, suggestion := range suggestions {
		insight := diagnostics.parseInsight("suggestion", &index, suggestion.Insight)
		parsed = append(parsed, project.Suggestion{Title: suggestion.Title, Summary: suggestion.Summary, Action: suggestion.Action, EngineeringInsight: insight})
	}
	return parsed
}

// parseFileAnalysisEngineeringInsightDiagnostic applies the selected-file
// requirement that all four insight fields be present and non-empty. The
// shared project parser remains the single source for JSON shape,
// normalization, and aggregate-size validation.
func parseFileAnalysisEngineeringInsightDiagnostic(raw json.RawMessage) (*project.EngineeringInsight, project.OptionalEngineeringInsightDiagnostic) {
	insight, diagnostic := project.ParseOptionalEngineeringInsightDiagnostic(raw)
	if diagnostic.Reason != project.OptionalEngineeringInsightAccepted {
		return nil, diagnostic
	}
	if !diagnostic.TradeoffOrFailureMode.Present || !diagnostic.TradeoffOrFailureMode.RuneCountKnown || diagnostic.TradeoffOrFailureMode.Runes == 0 || !diagnostic.TransferableLesson.Present || !diagnostic.TransferableLesson.RuneCountKnown || diagnostic.TransferableLesson.Runes == 0 {
		diagnostic.Reason = project.OptionalEngineeringInsightEmptyRequiredField
		return nil, diagnostic
	}
	return insight, diagnostic
}

func limitFileAnalysisInsights(report **project.EngineeringInsight, risks []project.Finding, suggestions []project.Suggestion) {
	remaining := 3
	keep := func(insight **project.EngineeringInsight) {
		if *insight == nil {
			return
		}
		if remaining == 0 {
			*insight = nil
			return
		}
		remaining--
	}
	keep(report)
	for index := range risks {
		keep(&risks[index].EngineeringInsight)
	}
	for index := range suggestions {
		keep(&suggestions[index].EngineeringInsight)
	}
}

func parseOptionalBugTaskSpec(raw json.RawMessage, target project.IndexFile, source string) *project.BugTaskSpec {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var spec project.BugTaskSpec
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil
	}
	validated, err := validateBugTaskSpec(&spec, target, source)
	if err != nil {
		return nil
	}
	return validated
}

func validateBugTaskSpec(spec *project.BugTaskSpec, target project.IndexFile, source string) (*project.BugTaskSpec, error) {
	if spec == nil {
		return nil, nil
	}
	if !validBugTaskTarget(spec, target.Path) {
		return nil, fmt.Errorf("semantic analysis contains an invalid bug task target")
	}
	if err := validateBugTaskItems(spec.AcceptanceCriteria, true); err != nil {
		return nil, err
	}
	if err := validateBugTaskItems(spec.NonGoals, false); err != nil {
		return nil, err
	}
	validated := project.SanitizeBugTaskSpec(spec)
	validated.TargetPath = target.Path
	if validated.GoTestCandidate != nil {
		validated.GoTestCandidate = validateGoTestCandidate(validated.GoTestCandidate, target, source)
	}
	return validated, nil
}

func validBugTaskTarget(spec *project.BugTaskSpec, targetPath string) bool {
	return spec.SchemaVersion == project.BugTaskSpecSchemaVersion && len(spec.TargetPath) <= 1024 &&
		spec.TargetPath == targetPath
}

func validateBugTaskItems(items []string, required bool) error {
	if len(items) > project.MaxBugTaskItems || required && len(items) == 0 {
		return fmt.Errorf("semantic analysis bug task items exceed limits")
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" || len(item) > project.MaxBugTaskItemBytes {
			return fmt.Errorf("semantic analysis bug task item is invalid")
		}
	}
	return nil
}

func validateGoTestCandidate(candidate *project.GoTestCandidateSpec, target project.IndexFile, source string) *project.GoTestCandidateSpec {
	if candidate == nil || target.Language != "Go" || len(candidate.Name) > project.MaxBugTaskItemBytes || len(candidate.Content) > project.MaxBugTaskCandidateBytes || !validGoTestName(candidate.Name) {
		return nil
	}
	fset := token.NewFileSet()
	targetFile, err := parser.ParseFile(fset, target.Path, source, parser.PackageClauseOnly)
	if err != nil {
		return nil
	}
	testFile, err := parser.ParseFile(fset, candidate.Name+"_test.go", candidate.Content, parser.AllErrors)
	if err != nil || testFile.Name == nil || testFile.Name.Name != targetFile.Name.Name {
		return nil
	}
	count := 0
	for _, declaration := range testFile.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == candidate.Name {
			count++
		}
	}
	if count != 1 {
		return nil
	}
	copy := *candidate
	return &copy
}

func validGoTestName(name string) bool {
	runes := []rune(name)
	return len(runes) > len("Test") && strings.HasPrefix(name, "Test") && unicode.IsUpper(runes[len("Test")])
}

func validateSemanticRisks(risks []project.Finding) error {
	for _, risk := range risks {
		if !risk.Category.Valid() || risk.Severity != "low" && risk.Severity != "medium" && risk.Severity != "high" || risk.Summary == "" {
			return fmt.Errorf("semantic analysis contains an invalid risk")
		}
	}
	return nil
}
