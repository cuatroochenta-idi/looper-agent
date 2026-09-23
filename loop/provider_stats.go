package loop

import (
	"sync"

	"github.com/cuatroochenta-idi/looper-agent/provider"
	"github.com/cuatroochenta-idi/looper-agent/telemetry"
)

// ProviderStats is the per-(Provider, Model) breakdown for one run.
// Multiprovider chains (FailoverProvider over several inners, or any chain
// that mixes models within a single run) emit several entries. Single-
// provider runs emit one. The slice on RunResult.Providers preserves the
// first-seen order so the primary entry stays at index 0.
type ProviderStats struct {
	// Provider is the cost-table key ("openai" | "anthropic" | "google").
	Provider string

	// Model is the effective model identifier the call resolved to.
	Model string

	// Calls is the number of LLM calls this (Provider, Model) answered.
	Calls int

	// FallbackCalls is the subset of Calls that came via a failover
	// switch (LLMResponse.Fallback / StreamChunk.Fallback). Equals
	// Calls when the entry is itself a fallback target that answered
	// every time; zero when this is the primary.
	FallbackCalls int

	// Usage is the per-entry token total.
	Usage Usage

	// Cost is the per-entry USD breakdown computed via the cost model.
	// Zero values when no cost model is configured.
	Cost CostBreakdown
}

// runStats is the live accumulator the loop feeds. Goroutine-safe so
// streaming code paths (which read chunks from a worker goroutine) and
// the iterator's consumer can share the same instance without external
// synchronisation.
type runStats struct {
	mu      sync.Mutex
	entries map[providerStatKey]*providerStatEntry
	order   []providerStatKey
}

type providerStatKey struct {
	provider string
	model    string
}

type providerStatEntry struct {
	calls         int
	fallbackCalls int
	usage         provider.Usage

	// unpriced holds each call's usage until snapshot prices it. Calls are
	// priced one by one because a tiered model's rates depend on the
	// call's own prompt size; pricing the summed usage would bill every
	// call of a long run at the long-context rate.
	unpriced []provider.Usage
	// cost is the sum of the per-call breakdowns priced so far.
	cost CostBreakdown
}

func newRunStats() *runStats {
	return &runStats{entries: make(map[providerStatKey]*providerStatEntry)}
}

// add accounts one LLM response against the (ProviderID, ModelID) bucket.
// Empty ProviderID falls back to fallbackProv (the loop's static
// provider label) so callers using legacy providers still see a single
// bucket rather than an empty key. Same for fallbackModel.
func (r *runStats) add(resp *provider.LLMResponse, fallbackProv, fallbackModel string) {
	if resp == nil {
		return
	}
	r.addCall(resp.ProviderID, resp.ModelID, resp.Usage, resp.Fallback, fallbackProv, fallbackModel)
}

// addChunk accounts one streaming chunk. Only final and error chunks carry
// Usage in the wire contract (an errored call still billed its partial
// tokens); deltas carry none and are ignored.
func (r *runStats) addChunk(c provider.StreamChunk, fallbackProv, fallbackModel string) {
	if c.Usage == nil {
		return
	}
	r.addCall(c.ProviderID, c.ModelID, *c.Usage, c.Fallback, fallbackProv, fallbackModel)
}

func (r *runStats) addCall(provID, modelID string, u provider.Usage, fallback bool, fallbackProv, fallbackModel string) {
	if provID == "" {
		provID = fallbackProv
	}
	if modelID == "" {
		modelID = fallbackModel
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := providerStatKey{provider: provID, model: modelID}
	e, ok := r.entries[k]
	if !ok {
		e = &providerStatEntry{}
		r.entries[k] = e
		r.order = append(r.order, k)
	}
	e.calls++
	if fallback {
		e.fallbackCalls++
	}
	e.usage.InputTokens += u.InputTokens
	e.usage.OutputTokens += u.OutputTokens
	e.usage.CachedTokens += u.CachedTokens
	e.usage.CacheWriteTokens += u.CacheWriteTokens
	e.usage.Cost += u.Cost
	e.unpriced = append(e.unpriced, u)
}

// snapshot materialises the accumulator into the public ProviderStats
// slice. costFn (typically providerCostFor) prices ONE call; each entry's
// Cost is the sum over its calls. Calls priced by an earlier snapshot are
// not priced again, so costFn must be the same function on every call —
// the loop's budget check snapshots after every LLM call. Passing it in
// keeps this file free of AgentLoop dependencies and easy to test in
// isolation.
func (r *runStats) snapshot(costFn func(provider, model string, u provider.Usage) CostBreakdown) []ProviderStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ProviderStats, 0, len(r.order))
	for _, k := range r.order {
		e := r.entries[k]
		stats := ProviderStats{
			Provider:      k.provider,
			Model:         k.model,
			Calls:         e.calls,
			FallbackCalls: e.fallbackCalls,
			Usage: Usage{
				InputTokens:      e.usage.InputTokens,
				OutputTokens:     e.usage.OutputTokens,
				CachedTokens:     e.usage.CachedTokens,
				CacheWriteTokens: e.usage.CacheWriteTokens,
			},
		}
		if costFn != nil {
			for _, u := range e.unpriced {
				e.cost = addCost(e.cost, costFn(k.provider, k.model, u))
			}
			e.unpriced = e.unpriced[:0]
			stats.Cost = e.cost
		}
		out = append(out, stats)
	}
	return out
}

// fallbackCount reports the number of LLM calls in the run that hit a
// FailoverProvider fallback target (response.Fallback=true). Exposed on
// RunResult for cheap "did we fall back?" queries.
func (r *runStats) fallbackCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int
	for _, e := range r.entries {
		n += e.fallbackCalls
	}
	return n
}

// addCost sums two breakdowns field by field; the sum is Estimated when
// either side is.
func addCost(a, b CostBreakdown) CostBreakdown {
	return CostBreakdown{
		TotalUSD:         a.TotalUSD + b.TotalUSD,
		InputUSD:         a.InputUSD + b.InputUSD,
		OutputUSD:        a.OutputUSD + b.OutputUSD,
		CachedUSD:        a.CachedUSD + b.CachedUSD,
		CacheWriteUSD:    a.CacheWriteUSD + b.CacheWriteUSD,
		SavingsUSD:       a.SavingsUSD + b.SavingsUSD,
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CachedTokens:     a.CachedTokens + b.CachedTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
		Estimated:        a.Estimated || b.Estimated,
	}
}

// providerCostFor is the per-call cost helper used by snapshot. Lives
// here (alongside the accumulator) rather than on AgentLoop so the
// stats package stays self-contained and the loop's calculateCost stays
// the single point that talks to telemetry.CostModel.
func providerCostFor(cm *telemetry.CostModel, providerName, modelName string, u provider.Usage) CostBreakdown {
	tokens := CostBreakdown{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CachedTokens:     u.CachedTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
	if cm == nil {
		// No pricing tables configured, but an API-reported cost is still valid.
		tokens.TotalUSD = u.Cost
		return tokens
	}
	br := cm.Calculate(providerName, modelName, telemetry.Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CachedTokens:     u.CachedTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		Cost:             u.Cost,
	})
	return CostBreakdown{
		TotalUSD:         br.TotalUSD,
		InputUSD:         br.InputUSD,
		OutputUSD:        br.OutputUSD,
		CachedUSD:        br.CachedUSD,
		CacheWriteUSD:    br.CacheWriteUSD,
		SavingsUSD:       br.SavingsUSD,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CachedTokens:     u.CachedTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		Estimated:        br.Estimated,
	}
}
