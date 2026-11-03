package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

const piCatalogFixture = `{"id":"models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"id":"local/qwen","name":"Qwen Local","provider":"lm-studio","baseUrl":"http://127.0.0.1:1234/v1","contextWindow":32768,"headers":{"Authorization":"credential-private-marker"}},{"id":"gpt-5","name":"GPT-5","provider":"openai-codex","baseUrl":"https://provider.invalid/v1","apiKey":"credential-private-marker"}]}}` + "\n"

func TestPiModelDiscoveryReadsCatalogInIsolationWithoutInference(t *testing.T) {
	profile, capture := cliFixture(t, config.PiProvider, "catalog")
	models, err := DiscoverPiModels(context.Background(), profile.CLIPath)
	if err != nil || len(models) != 2 || models[0].ID != "local/qwen" || models[1].Provider != "openai-codex" {
		t.Fatalf("catalog = %+v, %v", models, err)
	}
	encoded, err := json.Marshal(models)
	if err != nil || strings.Contains(string(encoded), "credential-private-marker") {
		t.Fatal("catalog retained authentication data")
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if request.Input != "{\"id\":\"models\",\"type\":\"get_available_models\"}\n" || request.System != "" || request.PrivateMode != 0700 {
		t.Fatalf("catalog request contained context or lacked isolation: %+v", request)
	}
	for _, flag := range []string{"--mode rpc", "--no-session", "--no-tools", "--no-extensions", "--no-mcp", "--no-skills", "--no-context-files", "--no-prompt-templates", "--no-themes", "--offline", "--no-approve"} {
		if !strings.Contains(strings.Join(request.Args, " "), flag) {
			t.Fatalf("catalog omitted %s", flag)
		}
	}
	if _, err := os.Stat(request.Directory); !os.IsNotExist(err) {
		t.Fatal("catalog retained its private working directory")
	}
}

func TestPiModelDiscoveryDistinguishesEmptyCatalogFromInvalidResponses(t *testing.T) {
	const empty = `{"id":"models","type":"response","command":"get_available_models","success":true,"data":{"models":[]}}`
	models, err := decodePiModels([]byte(empty))
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("empty catalog = %+v, %v", models, err)
	}
	for _, output := range []string{
		"", "credential-private-marker", strings.Replace(empty, "[]", "null", 1),
		strings.Replace(empty, "true", "false", 1), strings.Replace(empty, `"models","type"`, `"other","type"`, 1),
		empty + "\n" + empty, strings.Replace(empty, "[]", `[{"provider":"pi","id":"--prompt"}]`, 1),
		strings.Replace(empty, "[]", `[{"provider":"pi","id":"a"},{"provider":"pi","id":"a"}]`, 1),
		strings.Replace(empty, "[]", `[{"provider":"pi","id":"a\nb"}]`, 1),
	} {
		if _, err := decodePiModels([]byte(output)); err == nil || strings.Contains(err.Error(), "credential-private-marker") {
			t.Fatalf("invalid catalog returned %v", err)
		}
	}
}

func TestCLIModelDiscoveryPreservesCancellationAndSanitizesFailures(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.PiProvider, config.AgyProvider} {
		t.Run(string(provider), func(t *testing.T) {
			testCLIModelDiscoveryFailures(t, provider)
		})
	}
}

func testCLIModelDiscoveryFailures(t *testing.T, provider config.ModelProvider) {
	t.Helper()
	discover := func(ctx context.Context, executable string) error {
		if provider == config.AgyProvider {
			_, err := DiscoverAgyModels(ctx, executable)
			return err
		}
		_, err := DiscoverPiModels(ctx, executable)
		return err
	}
	for _, mode := range []string{"exit", "malformed", "oversized", "stderr-limit"} {
		t.Run(mode, func(t *testing.T) {
			profile, _ := cliFixture(t, provider, mode)
			err := discover(context.Background(), profile.CLIPath)
			if err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("catalog failure = %v", err)
			}
			if (mode == "oversized" || mode == "stderr-limit") && !errors.Is(err, ErrUnusableResponse) {
				t.Fatalf("output limit was not enforced: %v", err)
			}
		})
	}
	profile, capture := cliFixture(t, provider, "catalog")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := discover(ctx, profile.CLIPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("canceled discovery launched a process")
	}
	t.Run("running cancellation", func(t *testing.T) {
		profile, capture := cliFixture(t, provider, "wait")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- discover(ctx, profile.CLIPath) }()
		waitForCLIFile(t, capture)
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("running cancellation = %v", err)
		}
		var request cliCapture
		readTestJSON(t, capture, &request)
		if _, err := os.Stat(request.Directory); !os.IsNotExist(err) {
			t.Fatal("canceled discovery retained its directory")
		}
	})
}
