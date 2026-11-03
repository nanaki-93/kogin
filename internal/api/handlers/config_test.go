package handlers

import "github.com/nanaki-93/kogin/v2/internal/config"

func scopedHandlerConfig(apiBaseURL string) *config.Config {
	cfg := config.Default()
	cfg.ModelScopes.Analyze.APIBaseURL = apiBaseURL
	cfg.ModelScopes.Bug.APIBaseURL = apiBaseURL
	cfg.ModelScopes.Code.APIBaseURL = apiBaseURL
	return cfg
}
