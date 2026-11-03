package agentruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"google.golang.org/genai"

	"github.com/nanaki-93/kogin/v2/internal/app/agentstate"
	"github.com/nanaki-93/kogin/v2/internal/llm"
)

// ProviderUsageKey excludes destinations and credentials from retained usage.
func ProviderUsageKey(provider, model string) string {
	data, _ := json.Marshal([2]string{provider, model})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type tokenReceipt struct {
	Input, Output, Cached int64
	CacheKnown, Final     bool
}

func (r tokenReceipt) valid() bool {
	return r.Input >= 0 && r.Output >= 0 && r.Input <= 1<<50 && r.Output <= 1<<50 && r.Cached >= 0 && r.Cached <= r.Input && (r.CacheKnown || r.Cached == 0)
}

func (r *modelReservation) observeUsage(receipt tokenReceipt) error {
	if !receipt.valid() {
		return fmt.Errorf("%w: invalid provider token receipt", llm.ErrUnusableResponse)
	}
	if r == nil {
		return nil
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	if r.finished {
		return nil
	}
	if previous := r.reported; previous != nil && (receipt.Input < previous.Input || receipt.Output < previous.Output || receipt.Cached < previous.Cached || previous.Final && !receipt.Final) {
		return fmt.Errorf("%w: provider token receipt regressed", llm.ErrUnusableResponse)
	}
	r.reported = &receipt
	return nil
}

func (b *Budget) usageGroupLocked(key string) (int, error) {
	if b.usage.Accounting == nil {
		b.usage.Accounting = &agentstate.UsageAccounting{Version: 1, Providers: []agentstate.ProviderUsage{}}
	}
	groups := b.usage.Accounting
	index := slices.IndexFunc(groups.Providers, func(group agentstate.ProviderUsage) bool { return group.ModelKey == key })
	if index >= 0 {
		return index, nil
	}
	if len(groups.Providers) >= 64 {
		return 0, &BudgetError{Limit: "provider accounting identities"}
	}
	groups.Providers = append(groups.Providers, agentstate.ProviderUsage{ModelKey: key})
	return len(groups.Providers) - 1, nil
}

func (r *modelReservation) finalUsageLocked(input, output int64, known bool) error {
	if !known {
		return nil
	}
	receipt := tokenReceipt{Input: input, Output: output, Final: true}
	if r.reported != nil {
		receipt.Cached, receipt.CacheKnown = r.reported.Cached, r.reported.CacheKnown
	}
	if !receipt.valid() {
		return fmt.Errorf("%w: invalid provider usage", ErrBudgetExceeded)
	}
	r.reported = &receipt
	return nil
}

// settleAccountingLocked keeps partial reported consumption while charging the
// unmeasured remainder conservatively. It runs exactly once with budget.mu held.
func (r *modelReservation) settleAccountingLocked(completed bool) (int64, int64) {
	group := &r.budget.usage.Accounting.Providers[r.group]
	input, output := r.input, r.output
	group.ActiveCalls--
	if !completed {
		group.FailedCalls++
	}
	if r.reported == nil || !r.reported.Final {
		r.budget.usage.UnknownUsageCalls++
		group.UnknownUsageCalls++
	} else {
		input, output = r.reported.Input, r.reported.Output
		group.ReportedCalls++
	}
	if receipt := r.reported; receipt != nil {
		input, output = max(input, receipt.Input), max(output, receipt.Output)
		group.ReportedInputTokens += receipt.Input
		group.ReportedOutputTokens += receipt.Output
		group.CachedInputTokens += receipt.Cached
		if !receipt.Final {
			group.PartialUsageCalls++
		}
		if receipt.CacheKnown {
			group.CacheKnownCalls++
		}
	}
	group.InputTokens += input
	group.OutputTokens += output
	return input, output
}

func chatTokenReceipt(usage *llm.ChatUsage) (tokenReceipt, error) {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return tokenReceipt{}, fmt.Errorf("%w: invalid provider usage", llm.ErrUnusableResponse)
	}
	receipt := tokenReceipt{Input: int64(usage.PromptTokens), Output: max(int64(usage.CompletionTokens), int64(usage.TotalTokens)-int64(usage.PromptTokens)), Final: true}
	if usage.PromptTokensDetails != nil {
		receipt.Cached, receipt.CacheKnown = int64(usage.PromptTokensDetails.CachedTokens), true
	}
	if !receipt.valid() {
		return tokenReceipt{}, fmt.Errorf("%w: invalid provider usage", llm.ErrUnusableResponse)
	}
	return receipt, nil
}

func googleTokenReceipt(usage *genai.GenerateContentResponseUsageMetadata, final bool) (tokenReceipt, error) {
	if usage.PromptTokenCount < 0 || usage.CandidatesTokenCount < 0 || usage.ThoughtsTokenCount < 0 || usage.TotalTokenCount < 0 {
		return tokenReceipt{}, fmt.Errorf("%w: invalid native usage", llm.ErrUnusableResponse)
	}
	receipt := tokenReceipt{
		Input: int64(usage.PromptTokenCount), Output: max(int64(usage.CandidatesTokenCount)+int64(usage.ThoughtsTokenCount), int64(usage.TotalTokenCount)-int64(usage.PromptTokenCount)),
		Cached: int64(usage.CachedContentTokenCount), CacheKnown: true, Final: final,
	}
	if !receipt.valid() {
		return tokenReceipt{}, fmt.Errorf("%w: invalid native cached usage", llm.ErrUnusableResponse)
	}
	return receipt, nil
}
