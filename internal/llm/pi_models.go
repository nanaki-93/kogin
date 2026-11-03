package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

// PiModel contains only catalog metadata. Authentication and headers stay in Pi.
type PiModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	BaseURL       string `json:"baseUrl"`
	ContextWindow int    `json:"contextWindow"`
}

// DiscoverPiModels reads Pi's authenticated and custom model inventory without
// a prompt, project context, extensions, tools, or startup network refreshes.
func DiscoverPiModels(ctx context.Context, executable string) ([]PiModel, error) {
	output, err := readCLIModelCatalog(ctx, config.PiProvider, executable,
		[]string{"--mode", "rpc", "--no-session", "--no-tools", "--no-extensions", "--no-mcp", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--offline", "--no-approve"},
		"{\"id\":\"models\",\"type\":\"get_available_models\"}\n")
	if err != nil {
		return nil, err
	}
	return decodePiModels(output)
}

func decodePiModels(output []byte) ([]PiModel, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 4096), maxProviderResponseBytes)
	var models []PiModel
	found := false
	for scanner.Scan() {
		var response struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Command string `json:"command"`
			Success bool   `json:"success"`
			Data    struct {
				Models []PiModel `json:"models"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != "models" || response.Type != "response" || response.Command != "get_available_models" || !response.Success || response.Data.Models == nil || found {
			return nil, fmt.Errorf("pi returned an invalid model catalog")
		}
		found, models = true, response.Data.Models
	}
	if scanner.Err() != nil || !found {
		return nil, fmt.Errorf("pi did not return a complete model catalog")
	}
	if err := validatePiModels(models); err != nil {
		return nil, err
	}
	return models, nil
}

func validatePiModels(models []PiModel) error {
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		key := model.Provider + "/" + model.ID
		if !ValidPiModelID(model.Provider, model.ID) || seen[key] || len(model.Name) > 512 || strings.ContainsAny(model.Name, "\r\n\x00") {
			return fmt.Errorf("pi returned invalid or duplicate model identifiers")
		}
		seen[key] = true
	}
	return nil
}

func ValidPiModelID(provider, model string) bool {
	return provider != "" && model != "" && len(provider) <= 128 && len(model) <= 512 &&
		!strings.ContainsAny(provider, "/\\ \t\r\n\x00") && !strings.HasPrefix(provider, "-") &&
		!strings.ContainsAny(model, "\r\n\x00") && strings.TrimSpace(model) == model && !strings.HasPrefix(model, "-")
}
