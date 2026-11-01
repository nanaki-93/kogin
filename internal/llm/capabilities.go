package llm

import "github.com/nanaki-93/kasuri/v2/internal/config"

// Capabilities describes implemented adapter protocols, not an account's model
// entitlements. A particular model can still reject a supported request format.
type Capabilities struct {
	Completion bool `json:"completion"`
	JSONSchema bool `json:"json_schema"`
	Tools      bool `json:"tools"`
	Streaming  bool `json:"streaming"`
}

func ProviderCapabilities(profile config.ModelProfile) Capabilities {
	if profile.IsCLI() {
		return Capabilities{Completion: true, JSONSchema: true}
	}
	if profile.Provider == "" || profile.Provider == config.OpenAIProvider || profile.IsGoogle() {
		return Capabilities{Completion: true, JSONSchema: true, Tools: true, Streaming: true}
	}
	return Capabilities{}
}
