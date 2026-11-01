package app

import (
	"fmt"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

// Choices reference catalog models or legacy profiles; credentials remain daemon-owned.
type AnalysisModels struct {
	Code string `json:"code"`
	// Review preserves model assignments in older clients and saved runs.
	Review string `json:"review,omitempty"`
	// Features is the model of the retired feature-discovery step, read from
	// saved runs; a request that names one is refused.
	Features    string `json:"features"`
	Performance string `json:"performance,omitempty"`
	Security    string `json:"security,omitempty"`
}

func (models *AnalysisModels) Validate() error {
	if models == nil {
		return nil
	}
	profiles := models.selections()
	if models.Review != "" {
		profiles = append(profiles, models.Review)
	}
	for _, profile := range profiles {
		if !validModelSelection(profile) {
			return fmt.Errorf("analysis models must reference catalog model IDs or configured analyze, bug or function profiles")
		}
	}
	return nil
}

func (models *AnalysisModels) profile(stage AnalysisStage) string {
	if models == nil {
		return ""
	}
	switch stage {
	case AnalysisStageSemantic:
		return models.Code
	case AnalysisStageFeatures:
		return models.Features
	case AnalysisStagePerformance:
		if models.Performance != "" {
			return models.Performance
		}
	case AnalysisStageSecurityAI:
		if models.Security != "" {
			return models.Security
		}
	default:
		return ""
	}
	return models.Review
}

func (models *AnalysisModels) selections() []string {
	selections := []string{models.Code, models.profile(AnalysisStagePerformance), models.profile(AnalysisStageSecurityAI)}
	if models.Features != "" {
		selections = append(selections, models.Features)
	}
	return selections
}

func (s *Service) analysisModelRuntime(stage AnalysisStage, models *AnalysisModels) modelRuntime {
	return s.runtimeForAnalysisScope(analysisStageScope(stage), models.profile(stage))
}

func (s *Service) analysisFileModelIdentity(models *AnalysisModels) [3]EffectiveModel {
	return [3]EffectiveModel{
		s.analysisModelRuntime(AnalysisStageSemantic, models).effective,
		s.analysisModelRuntime(AnalysisStagePerformance, models).effective,
		s.analysisModelRuntime(AnalysisStageSecurityAI, models).effective,
	}
}

func analysisStageScope(stage AnalysisStage) config.ModelScope {
	if stage == AnalysisStageSemantic {
		return config.BugModelScope
	}
	return config.AnalyzeModelScope
}

// analysisStageProfile names the configured profile a stage uses, including defaults.
func analysisStageProfile(stage AnalysisStage, models *AnalysisModels) string {
	if profile := models.profile(stage); profile != "" {
		return profile
	}
	return string(analysisStageScope(stage))
}

func (s *Service) runtimeForAnalysisScope(scope config.ModelScope, profile string) modelRuntime {
	if profile == "" {
		profile = string(scope)
	}
	runtime, _ := s.selectedRuntime(scope, profile) // Admission validates catalog membership before dispatch.
	runtime.profile.Scope = scope
	runtime.effective.Scope = string(scope)
	return runtime
}

func cloneAnalysisModels(models *AnalysisModels) *AnalysisModels {
	if models == nil {
		return nil
	}
	copy := *models
	return &copy
}
