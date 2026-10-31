package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/llm"
	"github.com/nanaki-93/mini-orca/v2/internal/project"
)

const (
	semanticAnalysisPromptVersion = "file-analysis-v18"
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
        "test_candidate":{"anyOf":[{"$ref":"#/$defs/testCandidate"},{"type":"null"}]}
      }
    },
    "testCandidate":{
      "type":"object",
      "additionalProperties":false,
      "required":["path","name","content"],
      "properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"name":{"type":"string","minLength":1,"maxLength":200},"content":{"type":"string","minLength":1,"maxLength":16384}}
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
        "anchor":{"type":"string","maxLength":300},
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

// FileAnalysisResponseSchema returns the one strict structured-output contract
// shared by production selected-file analysis and its evaluation runner.
func FileAnalysisResponseSchema() llm.JSONSchema {
	return llm.JSONSchema{Name: fileAnalysisResponseSchemaName, Schema: json.RawMessage(fileAnalysisResponseSchemaDocument)}
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
