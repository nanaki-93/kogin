package llm

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nanaki-93/mini-orca/v2/internal/config"
)

// Catalog commands inherit CLI authentication but never the imported workspace.
func readCLIModelCatalog(ctx context.Context, provider config.ModelProvider, executable string, args []string, input string) (output []byte, err error) {
	directory, err := os.MkdirTemp("", "kasuri-models-")
	if err != nil {
		return nil, fmt.Errorf("cannot create private %s catalog directory", provider)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil && err == nil {
			output, err = nil, fmt.Errorf("cannot remove private %s catalog directory", provider)
		}
	}()
	timed, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return runCLI(timed, config.ModelProfile{Provider: provider, CLIPath: executable}, cliInvocation{
		directory: directory, args: args, input: input,
	})
}
