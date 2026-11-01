package llm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

type AgyModel struct {
	ID   string
	Name string
}

// DiscoverAgyModels asks the signed-in CLI for its current catalog, without a
// prompt. Agy may contact its service; no project context is supplied.
func DiscoverAgyModels(ctx context.Context, executable string) ([]AgyModel, error) {
	output, err := readCLIModelCatalog(ctx, config.AgyProvider, executable, []string{"--log-file", os.DevNull, "models"}, "")
	if err != nil {
		return nil, err
	}
	return decodeAgyModels(output)
}

func decodeAgyModels(output []byte) ([]AgyModel, error) {
	// Agy 1.3.4 emits tab-separated slug/name rows; its models subcommand does
	// not accept --output-format. Progress is on stderr, never catalog stdout.
	scanner := bufio.NewScanner(bytes.NewReader(output))
	models := []AgyModel{}
	seen := map[string]bool{}
	for scanner.Scan() {
		id, name, ok := strings.Cut(scanner.Text(), "\t")
		if !ok || !config.ValidAgyModelID(id) || seen[id] || !validAgyModelName(name) {
			return nil, fmt.Errorf("agy returned an invalid model catalog")
		}
		seen[id] = true
		models = append(models, AgyModel{ID: id, Name: name})
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("agy did not return a complete model catalog")
	}
	return models, nil
}

func validAgyModelName(name string) bool {
	return name != "" && len(name) <= 512 && strings.TrimSpace(name) == name &&
		utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}
