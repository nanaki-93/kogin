package llm

import (
	"testing"

	"github.com/nanaki-93/kogin/v2/internal/config"
)

func TestProviderCapabilitiesDescribeImplementedProtocols(t *testing.T) {
	for _, provider := range []config.ModelProvider{config.OpenAIProvider, config.GeminiProvider, config.VertexProvider, config.PiProvider, config.AgyProvider} {
		profile := config.ModelProfile{Provider: provider}
		capabilities := ProviderCapabilities(profile)
		if !capabilities.Completion || !capabilities.JSONSchema || capabilities.Tools == profile.IsCLI() || capabilities.Streaming == profile.IsCLI() {
			t.Fatalf("%s capabilities=%+v", provider, capabilities)
		}
	}
	if ProviderCapabilities(config.ModelProfile{Provider: "unknown"}) != (Capabilities{}) {
		t.Fatal("unknown adapter claimed capabilities")
	}
}
