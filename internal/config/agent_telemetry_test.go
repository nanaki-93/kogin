package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPriceTable() AgentTelemetry {
	in, out, cached := 1.0, 2.0, 0.5
	return AgentTelemetry{Prices: AgentPriceTable{Version: "rates-2026-10", Currency: "USD", Rates: []AgentModelPrice{{Provider: "openai", Model: "fixture", InputPerMillion: &in, OutputPerMillion: &out, CachedPerMillion: &cached}}}}
}

func TestAgentPricesRequireExplicitVersionedFiniteRates(t *testing.T) {
	if _, err := ResolveAgentTelemetry(AgentTelemetry{}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"version", "currency", "empty", "duplicate", "missing", "negative", "nan", "infinity", "oversized", "control"} {
		value := testPriceTable()
		switch invalid {
		case "version":
			value.Prices.Version = ""
		case "currency":
			value.Prices.Currency = "usd"
		case "empty":
			value.Prices.Rates = nil
		case "duplicate":
			value.Prices.Rates = append(value.Prices.Rates, value.Prices.Rates[0])
		case "missing":
			value.Prices.Rates[0].CachedPerMillion = nil
		case "negative":
			*value.Prices.Rates[0].InputPerMillion = -1
		case "nan":
			*value.Prices.Rates[0].InputPerMillion = math.NaN()
		case "infinity":
			*value.Prices.Rates[0].InputPerMillion = math.Inf(1)
		case "oversized":
			*value.Prices.Rates[0].InputPerMillion = 1e7
		case "control":
			value.Prices.Rates[0].Model = "secret\nmodel"
		}
		if _, err := ResolveAgentTelemetry(value); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
	value := testPriceTable()
	*value.Prices.Rates[0].InputPerMillion = 0
	resolved, err := ResolveAgentTelemetry(value)
	if err != nil {
		t.Fatal(err)
	}
	*value.Prices.Rates[0].InputPerMillion = 9
	if *resolved.Prices.Rates[0].InputPerMillion != 0 {
		t.Fatal("configured rates aliased admitted settings")
	}
}

func TestAgentPriceYAMLRejectsUnknownNestedFields(t *testing.T) {
	base := "agent_runtime:\n  telemetry:\n    prices:\n      version: fixture\n      currency: USD\n      rates:\n        - provider: openai\n          model: fixture\n          input_per_million: 0\n          output_per_million: 1\n          cached_per_million: 0\n"
	base += "model_scopes:\n  analyze: {api_base_url: 'http://localhost:1234/v1', model: a}\n  bug: {api_base_url: 'http://localhost:1234/v1', model: b}\n  code: {api_base_url: 'http://localhost:1234/v1', model: f}\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromYAML(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(base, "cached_per_million", "cached_typo", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromYAML(path); err == nil {
		t.Fatal("unknown rate accepted")
	}
}
