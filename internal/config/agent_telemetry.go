package config

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// AgentTelemetry holds the optional price table that turns reported usage
// into cost estimates.
type AgentTelemetry struct {
	Prices AgentPriceTable `yaml:"prices"`
}

type AgentPriceTable struct {
	Version  string            `yaml:"version"`
	Currency string            `yaml:"currency"`
	Rates    []AgentModelPrice `yaml:"rates"`
}

// Pointer rates distinguish an explicit zero price from missing configuration.
type AgentModelPrice struct {
	Provider         string   `yaml:"provider"`
	Model            string   `yaml:"model"`
	InputPerMillion  *float64 `yaml:"input_per_million"`
	OutputPerMillion *float64 `yaml:"output_per_million"`
	CachedPerMillion *float64 `yaml:"cached_per_million"`
}

var priceVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var priceCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func ResolveAgentTelemetry(value AgentTelemetry) (AgentTelemetry, error) {
	table := &value.Prices
	if table.Version == "" && table.Currency == "" && len(table.Rates) == 0 {
		return value, nil
	}
	if !priceVersion.MatchString(table.Version) || !priceCurrency.MatchString(table.Currency) || len(table.Rates) == 0 || len(table.Rates) > 64 {
		return value, fmt.Errorf("agent_runtime.telemetry.prices requires a version, three-letter currency and 1–64 rates")
	}
	table.Rates = slices.Clone(table.Rates)
	seen := map[[2]string]bool{}
	for i, rate := range table.Rates {
		key := [2]string{rate.Provider, rate.Model}
		if seen[key] || !priceIdentity(rate.Provider) || !priceIdentity(rate.Model) {
			return value, fmt.Errorf("agent price identities must be nonempty, bounded and unique")
		}
		seen[key] = true
		copy, err := resolveModelPrice(rate)
		if err != nil {
			return value, err
		}
		table.Rates[i] = copy
	}
	return value, nil
}

func priceIdentity(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func resolveModelPrice(rate AgentModelPrice) (AgentModelPrice, error) {
	for _, field := range []**float64{&rate.InputPerMillion, &rate.OutputPerMillion, &rate.CachedPerMillion} {
		if *field == nil || math.IsNaN(**field) || math.IsInf(**field, 0) || **field < 0 || **field > 1e6 {
			return rate, fmt.Errorf("agent prices require explicit finite input, output and cached rates from 0 to 1000000 per million tokens")
		}
		value := **field
		*field = &value
	}
	return rate, nil
}
