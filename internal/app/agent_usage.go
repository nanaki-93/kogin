package app

import (
	"slices"

	"github.com/nanaki-93/kogin/v2/internal/app/agentruntime"
	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/config"
)

func agentCost(usage agentstate.BudgetUsage, table config.AgentPriceTable) *AgentCostEstimate {
	result := &AgentCostEstimate{Status: "unknown", Currency: table.Currency, PriceVersion: table.Version, Reasons: []string{}}
	if table.Version == "" {
		result.Reasons = append(result.Reasons, "price_table_unavailable")
		return result
	}
	if usage.Accounting == nil {
		result.Reasons = append(result.Reasons, "legacy_usage")
		return result
	}
	prices := make(map[string]config.AgentModelPrice, len(table.Rates))
	for _, rate := range table.Rates {
		prices[agentruntime.ProviderUsageKey(rate.Provider, rate.Model)] = rate
	}
	for _, group := range usage.Accounting.Providers {
		price, known := prices[group.ModelKey]
		if !known {
			result.Reasons = append(result.Reasons, "model_rate_unavailable")
			continue
		}
		addProviderCost(result, group, price)
	}
	slices.Sort(result.Reasons)
	result.Reasons = slices.Compact(result.Reasons)
	if len(result.Reasons) == 0 {
		result.Status = "estimated"
		total := result.KnownSubtotal
		result.Total = &total
	}
	return result
}

func addProviderCost(result *AgentCostEstimate, group agentstate.ProviderUsage, price config.AgentModelPrice) {
	if price.InputPerMillion == nil || price.OutputPerMillion == nil || price.CachedPerMillion == nil {
		result.Reasons = append(result.Reasons, "model_rate_unavailable")
		return
	}
	result.KnownSubtotal += float64(group.ReportedOutputTokens) * *price.OutputPerMillion / 1e6
	result.KnownSubtotal += float64(group.CachedInputTokens) * *price.CachedPerMillion / 1e6
	cacheKnown := group.CacheKnownCalls == group.ReportedCalls+group.PartialUsageCalls
	if cacheKnown || *price.InputPerMillion == *price.CachedPerMillion {
		result.KnownSubtotal += float64(group.ReportedInputTokens-group.CachedInputTokens) * *price.InputPerMillion / 1e6
	} else {
		result.Reasons = append(result.Reasons, "cache_split_unknown")
	}
	if group.ActiveCalls > 0 || group.UnknownUsageCalls > 0 || group.ReportedCalls != group.ModelCalls {
		result.Reasons = append(result.Reasons, "usage_incomplete")
	}
}
