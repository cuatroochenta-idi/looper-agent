package telemetry

import (
	"log"
	"maps"
	"strings"
	"sync"
)

// CostConfig holds pricing for a specific model.
type CostConfig struct {
	// InputCostPer1MTokens is the USD cost per 1 million input tokens.
	InputCostPer1MTokens float64 `json:"input"`

	// OutputCostPer1MTokens is the USD cost per 1 million output tokens.
	OutputCostPer1MTokens float64 `json:"output"`

	// CachedCostPer1MTokens is the USD cost per 1 million cached input tokens.
	// Typically 50% discount on OpenAI, 90% on Anthropic.
	CachedCostPer1MTokens float64 `json:"cached"`

	// CacheWriteCostPer1MTokens is the USD cost per 1 million cache-write
	// input tokens (Anthropic bills cache_creation at 1.25× input). When
	// zero and cache-write tokens are present, Calculate falls back to
	// 1.25× InputCostPer1MTokens — the only provider that reports the
	// bucket bills it exactly there.
	CacheWriteCostPer1MTokens float64 `json:"cache_write"`
}

// CostBreakdown provides a detailed cost report.
type CostBreakdown struct {
	TotalUSD         float64
	InputUSD         float64
	OutputUSD        float64
	CachedUSD        float64
	CacheWriteUSD    float64
	SavingsUSD       float64
	InputTokens      int
	OutputTokens     int
	CachedTokens     int
	CacheWriteTokens int

	// Estimated is true when TotalUSD came from the pricing tables rather
	// than an API-reported cost. False both for API-reported totals and for
	// the all-zero "no pricing known" case (which warns instead).
	Estimated bool
}

// CostModel is a thread-safe registry of model pricing.
// It includes base costs for official models and supports custom overrides.
type CostModel struct {
	mu     sync.RWMutex
	prices map[string]map[string]CostConfig // provider -> model -> cost

	missMu       sync.Mutex
	warnedMisses map[string]bool
}

// NewCostModel creates a cost model pre-populated with official base prices.
func NewCostModel() *CostModel {
	cm := &CostModel{
		prices: make(map[string]map[string]CostConfig),
	}
	cm.loadDefaults()
	return cm
}

// UpdateCost registers or updates the cost for a specific provider and model.
func (cm *CostModel) UpdateCost(provider, model string, config CostConfig) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.prices[provider] == nil {
		cm.prices[provider] = make(map[string]CostConfig)
	}
	cm.prices[provider][model] = config
}

// WithCustomCost registers a custom model cost (e.g., for Ollama, OpenRouter).
func (cm *CostModel) WithCustomCost(model string, config CostConfig) {
	cm.UpdateCost("custom", model, config)
}

// WithCustomCosts bulk-registers pricing overrides from a dict. Keys are
// either "provider/model" (scoped to that provider's table, overriding the
// built-in matrix entry) or a bare model id (registered in the "custom"
// table, which wins over every built-in during lookup). Family-level keys
// work the same as matrix keys ("gpt-5" prices "gpt-5-2025-08-07").
//
// This is the config-file entry point: looper.json's model_costs feeds it.
func (cm *CostModel) WithCustomCosts(costs map[string]CostConfig) {
	for key, cfg := range costs {
		if prov, model, ok := strings.Cut(key, "/"); ok && prov != "" && model != "" {
			cm.UpdateCost(prov, model, cfg)
			continue
		}
		cm.WithCustomCost(key, cfg)
	}
}

// Calculate computes the cost for a given usage, resolving through the
// precision cascade:
//
//  1. API-reported cost (usage.Cost > 0) is authoritative for TotalUSD —
//     the upstream already billed it, success or failure. The component
//     split is re-scaled from whatever pricing table matched so the
//     breakdown stays populated.
//  2. Otherwise the total is ESTIMATED from pricing tables — custom
//     overrides (WithCustomCost / WithCustomCosts) first, then the
//     built-in matrix — and Estimated is set.
//  3. Neither available → all-zero breakdown plus a one-time warning.
func (cm *CostModel) Calculate(provider, model string, usage Usage) CostBreakdown {
	cm.mu.RLock()
	config, matched := cm.lookup(provider, model)
	cm.mu.RUnlock()

	// Only warn about a missing pricing entry when there's no API-reported
	// cost to fall back on — an upstream cost makes the tables irrelevant.
	if !matched && usage.Cost == 0 {
		cm.warnMiss(provider, model)
	}

	// InputTokens is the inclusive prompt total (see provider.Usage); the
	// cached and cache-write buckets price differently, so carve them out.
	// Clamp defensively: a provider that violated the ⊆ contract must not
	// produce a negative full-rate bucket.
	nonCachedInput := max(usage.InputTokens-usage.CachedTokens-usage.CacheWriteTokens, 0)
	nonCachedCost := float64(nonCachedInput) / 1_000_000.0 * config.InputCostPer1MTokens

	// Cached input tokens at cached (discounted) price
	cachedCost := float64(usage.CachedTokens) / 1_000_000.0 * config.CachedCostPer1MTokens

	// Cache-write tokens at the write premium (default 1.25× input when the
	// config doesn't say otherwise — the Anthropic billing rule).
	cacheWriteRate := config.CacheWriteCostPer1MTokens
	if cacheWriteRate == 0 && usage.CacheWriteTokens > 0 {
		cacheWriteRate = config.InputCostPer1MTokens * 1.25
	}
	cacheWriteCost := float64(usage.CacheWriteTokens) / 1_000_000.0 * cacheWriteRate

	// Output tokens at output price
	outputCost := float64(usage.OutputTokens) / 1_000_000.0 * config.OutputCostPer1MTokens

	// Savings from caching: what we would have paid for those tokens at full input rate
	savings := (float64(usage.CachedTokens)/1_000_000.0*config.InputCostPer1MTokens) - cachedCost

	tableInput := nonCachedCost + cachedCost + cacheWriteCost
	tableTotal := tableInput + outputCost

	breakdown := CostBreakdown{
		TotalUSD:         tableTotal,
		InputUSD:         tableInput,
		OutputUSD:        outputCost,
		CachedUSD:        cachedCost,
		CacheWriteUSD:    cacheWriteCost,
		SavingsUSD:       savings,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CachedTokens:     usage.CachedTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		Estimated:        matched && tableTotal > 0,
	}

	// API-reported cost overrides the estimated total. Re-scale the table
	// split to the reported total so the per-component breakdown stays
	// consistent; when no table can price this model (tableTotal == 0) the
	// split degrades to zero rather than being invented.
	if usage.Cost > 0 {
		breakdown.TotalUSD = usage.Cost
		breakdown.Estimated = false
		if tableTotal > 0 {
			scale := usage.Cost / tableTotal
			breakdown.InputUSD = tableInput * scale
			breakdown.OutputUSD = outputCost * scale
			breakdown.CachedUSD = cachedCost * scale
			breakdown.CacheWriteUSD = cacheWriteCost * scale
			breakdown.SavingsUSD = savings * scale
		} else {
			breakdown.InputUSD = 0
			breakdown.OutputUSD = 0
			breakdown.CachedUSD = 0
			breakdown.CacheWriteUSD = 0
			breakdown.SavingsUSD = 0
		}
	}

	return breakdown
}

// lookup returns the price config for (provider, model) and whether a match
// was found. User-supplied custom pricing always outranks the built-in
// matrix; within each table an exact id beats a family prefix. Resolution
// order:
//
//  1. Exact match in the "custom" table (WithCustomCost / bare-key dict).
//  2. Longest family-prefix match in the "custom" table — the longest
//     registered key K such that the model starts with K and the next
//     character is a family separator (`-` or `.`).
//  3. Exact match in the provider's table. Note UpdateCost(provider, …) and
//     "provider/model" dict keys write here, so they override the matrix
//     entry directly rather than racing it.
//  4. Longest family-prefix match in the provider's table — this lets
//     "claude-opus-4" match "claude-opus-4-7", "gpt-5" match "gpt-5.5",
//     and any dated suffix ("…-20250514", "…-2025-08-07") fall back to its
//     family.
//
// Returns (CostConfig{}, false) when nothing matches; the caller surfaces
// the miss via warnMiss.
func (cm *CostModel) lookup(provider, model string) (CostConfig, bool) {
	if c, ok := exact(cm.prices, "custom", model); ok {
		return c, true
	}
	if c, ok := longestFamily(cm.prices, "custom", model); ok {
		return c, true
	}
	if c, ok := exact(cm.prices, provider, model); ok {
		return c, true
	}
	if c, ok := longestFamily(cm.prices, provider, model); ok {
		return c, true
	}
	return CostConfig{}, false
}

func exact(table map[string]map[string]CostConfig, provider, model string) (CostConfig, bool) {
	p, ok := table[provider]
	if !ok {
		return CostConfig{}, false
	}
	c, ok := p[model]
	return c, ok
}

func longestFamily(table map[string]map[string]CostConfig, provider, model string) (CostConfig, bool) {
	p, ok := table[provider]
	if !ok {
		return CostConfig{}, false
	}
	var bestKey string
	var bestCfg CostConfig
	for k, c := range p {
		if !familyPrefix(model, k) {
			continue
		}
		if len(k) > len(bestKey) {
			bestKey, bestCfg = k, c
		}
	}
	return bestCfg, bestKey != ""
}

// familyPrefix reports whether key is a family-level prefix of model. The
// prefix must terminate at end-of-string or at a `-`/`.` boundary so that
// "gpt-4" does not accidentally match "gpt-40" while "gpt-5" still matches
// "gpt-5-2025-08-07" and "gpt-5.5".
func familyPrefix(model, key string) bool {
	if key == "" {
		return false
	}
	if !strings.HasPrefix(model, key) {
		return false
	}
	if len(model) == len(key) {
		return true
	}
	switch model[len(key)] {
	case '-', '.':
		return true
	}
	return false
}

// warnMiss logs a one-time warning per (provider, model) pair so cost
// blind spots stop being silent — repeated calls for the same key stay
// quiet to avoid spamming long-running agents.
func (cm *CostModel) warnMiss(provider, model string) {
	if model == "" {
		return
	}
	cm.missMu.Lock()
	defer cm.missMu.Unlock()
	if cm.warnedMisses == nil {
		cm.warnedMisses = make(map[string]bool)
	}
	key := provider + "/" + model
	if cm.warnedMisses[key] {
		return
	}
	cm.warnedMisses[key] = true
	log.Printf("telemetry: no cost entry for %s/%s — reporting $0 "+
		"(register one with CostModel.UpdateCost / Agent.WithCustomModelCost)",
		provider, model)
}

func (cm *CostModel) loadDefaults() {
	maps.Copy(cm.prices, defaultCosts())
}
