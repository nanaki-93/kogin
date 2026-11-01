package llm

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nanaki-93/kasuri/v2/internal/config"
)

const agyCatalogFixture = "gemini-3.8-flash-high\tGemini 3.8 Flash (High)\ngemini-4-argon-medium\tGemini 4 Argon (Medium)\n"

func TestAgyModelDiscoveryReadsLiveCatalogWithoutInference(t *testing.T) {
	profile, capture := cliFixture(t, config.AgyProvider, "catalog")
	models, err := DiscoverAgyModels(context.Background(), profile.CLIPath)
	if err != nil || !reflect.DeepEqual(models, []AgyModel{
		{ID: "gemini-3.8-flash-high", Name: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-4-argon-medium", Name: "Gemini 4 Argon (Medium)"},
	}) {
		t.Fatalf("catalog = %+v, %v", models, err)
	}
	var request cliCapture
	readTestJSON(t, capture, &request)
	if !reflect.DeepEqual(request.Args, []string{"--log-file", os.DevNull, "models"}) || request.Input != "" || request.System != "" || request.PrivateMode != 0700 {
		t.Fatalf("catalog request contained context or lacked isolation: %+v", request)
	}
	if _, err := os.Stat(request.Directory); !os.IsNotExist(err) {
		t.Fatal("catalog retained its private working directory")
	}
}

func TestAgyModelDiscoveryAcceptsEmptyAndRejectsMalformedCatalogs(t *testing.T) {
	for _, output := range []string{"", agyCatalogFixture, strings.ReplaceAll(agyCatalogFixture, "\n", "\r\n")} {
		models, err := decodeAgyModels([]byte(output))
		if err != nil || models == nil {
			t.Fatalf("valid catalog = %+v, %v", models, err)
		}
	}
	for name, output := range map[string]string{
		"diagnostic":       "credential-private-marker",
		"whitespace":       "\n",
		"duplicate":        agyCatalogFixture + agyCatalogFixture,
		"flag as model":    "--prompt\tGemini\n",
		"display name ID":  "Gemini Flash\tGemini Flash\n",
		"missing name":     "gemini\t\n",
		"extra column":     "gemini\tGemini\tprivate-marker\n",
		"control in name":  "gemini\tGemini\x1b[31m\n",
		"long name":        "gemini\t" + strings.Repeat("x", 513),
		"invalid encoding": "gemini\t\xff",
		"partial catalog":  agyCatalogFixture + "error: private-marker",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAgyModels([]byte(output)); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("invalid catalog returned %v", err)
			}
		})
	}
}
