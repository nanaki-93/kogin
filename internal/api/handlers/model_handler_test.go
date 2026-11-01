package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/config"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

func TestAvailableModelCatalogIncludesCLIMetadataAndRejectsQueries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI providers require process-group cleanup")
	}
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "fake-pi")
	script := `#!/bin/sh
cat >/dev/null
cat <<'MODELS'
{"id":"models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"local-server","id":"qwen","name":"Qwen","baseUrl":"http://localhost:1234/v1","headers":{"Authorization":"private-token"}}]}}
MODELS
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	agyCommand := filepath.Join(t.TempDir(), "fake-agy")
	agyScript := "#!/bin/sh\n[ \"$1\" = '--log-file' ] && [ \"$3\" = 'models' ] && [ \"$#\" -eq 3 ] || exit 8\nprintf 'gemini-new-high\\tGemini New (High)\\n'\n"
	if err := os.WriteFile(agyCommand, []byte(agyScript), 0700); err != nil {
		t.Fatal(err)
	}
	// Claude Code has no catalog command: an installed executable lists its aliases.
	claudeCommand := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(claudeCommand, []byte("#!/bin/sh\nexit 8\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ModelScopes.Bug = config.ModelProfileConfig{Provider: config.ClaudeProvider, CLIPath: claudeCommand, Model: "sonnet"}
	cfg.ModelScopes.Analyze = config.ModelProfileConfig{Provider: config.AgyProvider, CLIPath: agyCommand, Model: "configured-agy"}
	cfg.ModelScopes.Code = config.ModelProfileConfig{Provider: config.PiProvider, CLIPath: command, Model: "local-server/default"}
	service, err := app.New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?model=arbitrary", "?%ZZ"} {
		response := httptest.NewRecorder()
		NewModelHandler(service).Available(response, httptest.NewRequest(http.MethodGet, "/api/models/available"+query, nil))
		if query != "" {
			if response.Code != http.StatusBadRequest {
				t.Fatalf("query accepted: %s", response.Body.String())
			}
			continue
		}
		var catalog app.AvailableModels
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || catalog.Pi.Status != "ready" || catalog.Agy.Status != "ready" || catalog.Claude.Status != "ready" {
			t.Fatalf("catalog response = %d: %s", response.Code, response.Body.String())
		}
		if len(catalog.Defaults) != 3 || len(catalog.Models) != 9 || catalog.Models[0].ID != "agy:gemini-new-high" || catalog.Models[4].ID != "claude:sonnet" || catalog.Models[len(catalog.Models)-1].ID != "pi:local-server/qwen" {
			t.Fatalf("missing catalog data: %+v", catalog)
		}
		if strings.Contains(response.Body.String(), "private-token") || strings.Contains(response.Body.String(), command) || strings.Contains(response.Body.String(), agyCommand) || strings.Contains(response.Body.String(), claudeCommand) {
			t.Fatal("catalog exposed credentials or executable path")
		}
	}
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewModelHandler(service).Available(response, httptest.NewRequest(http.MethodGet, "/api/models/available", nil))
	var catalog app.AvailableModels
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || catalog.Pi.Status != "unavailable" || catalog.Agy.Status != "ready" || len(catalog.Models) != 8 {
		t.Fatalf("configured models lost when Pi unavailable: %s", response.Body.String())
	}
	if err := errors.Join(os.Remove(agyCommand), os.Remove(claudeCommand)); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	NewModelHandler(service).Available(response, httptest.NewRequest(http.MethodGet, "/api/models/available", nil))
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil || catalog.Pi.Status != "unavailable" || catalog.Agy.Status != "unavailable" || catalog.Claude.Status != "unavailable" || len(catalog.Models) != 3 {
		t.Fatalf("configured models lost when every CLI is unavailable: %s", response.Body.String())
	}
}

func TestCurrentModelCatalogExposesOnlyScopedProfilesWithoutSecrets(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.New(&config.Config{
		ModelScopes: config.ModelScopesConfig{
			Analyze: config.ModelProfileConfig{APIBaseURL: "https://analyze.example/v1", APIKey: "secret-value", Model: "analyze", ReasoningEffort: "high"},
			Bug:     config.ModelProfileConfig{APIBaseURL: "http://127.0.0.1:11434/v1", Model: "bug", ReasoningEffort: "low"},
			Code:    config.ModelProfileConfig{APIBaseURL: "https://function.example/v1", APIKey: "secret-value", Model: "code", ReasoningEffort: "max"},
		},
	}, manager)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewModelHandler(service).Current(response, httptest.NewRequest(http.MethodGet, "/api/models/current", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var catalog app.ModelCatalog
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Scopes) != 3 || catalog.Scopes["analyze"].Model != "analyze" || catalog.Scopes["analyze"].ReasoningEffort != "high" || catalog.Scopes["bug"].ReasoningEffort != "low" || catalog.Scopes["code"].ReasoningEffort != "max" || catalog.Scopes["bug"].RemoteProvider || !catalog.Scopes["code"].RemoteProvider {
		t.Fatalf("catalog = %+v", catalog)
	}
	if strings.Contains(response.Body.String(), "secret-value") || strings.Contains(response.Body.String(), "/v1") {
		t.Fatalf("model response leaked secret configuration: %s", response.Body.String())
	}
}

func TestCLIModelCatalogRequiresConsentWithoutLaunchingOrExposingExecutablePaths(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ModelScopes.Analyze = config.ModelProfileConfig{Provider: config.AgyProvider, CLIPath: "/not-installed/private-agy", Model: "agy-model"}
	cfg.ModelScopes.Code = config.ModelProfileConfig{Provider: config.PiProvider, CLIPath: "/not-installed/private-pi", Model: "pi-model"}
	service, err := app.New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewModelHandler(service).Current(response, httptest.NewRequest(http.MethodGet, "/api/models/current", nil))
	var catalog app.ModelCatalog
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog = %s", response.Body.String())
	}
	for scope, provider := range map[string]string{"analyze": "agy", "code": "pi"} {
		model := catalog.Scopes[scope]
		if model.ProviderOrigin != "cli://"+provider || !model.RemoteProvider || model.Temperature != nil || model.MaxTokens != nil {
			t.Fatalf("CLI metadata = %+v", model)
		}
	}
	if strings.Contains(response.Body.String(), "private-") || catalog.Scopes["bug"].MaxTokens == nil {
		t.Fatalf("invalid metadata = %s", response.Body.String())
	}
}

func TestNativeGoogleCatalogIsPassiveAndSourceFree(t *testing.T) {
	manager, err := project.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ModelScopes.Analyze = config.ModelProfileConfig{Provider: config.GeminiProvider, Model: "fixture", APIKey: "private-api-key"}
	cfg.ModelScopes.Bug = config.ModelProfileConfig{Provider: config.VertexProvider, Model: "fixture", AccessToken: "private-access-token", VertexProject: "private-project", VertexLocation: "global"}
	service, err := app.New(cfg, manager)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewModelHandler(service).Current(response, httptest.NewRequest(http.MethodGet, "/api/models/current", nil))
	var catalog app.ModelCatalog
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog=%s", response.Body.String())
	}
	for _, scope := range []string{"analyze", "bug"} {
		model := catalog.Scopes[scope]
		if !model.RemoteProvider || !model.Capabilities.JSONSchema || !model.Capabilities.Streaming {
			t.Fatalf("native metadata=%+v", model)
		}
	}
	if strings.Contains(response.Body.String(), "private-") {
		t.Fatal("catalog exposed native credentials or project configuration")
	}
}
