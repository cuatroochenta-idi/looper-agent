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
	InputCostPer1MTokens float64

	// OutputCostPer1MTokens is the USD cost per 1 million output tokens.
	OutputCostPer1MTokens float64

	// CachedCostPer1MTokens is the USD cost per 1 million cached input tokens.
	// Typically 50% discount on OpenAI, 90% on Anthropic.
	CachedCostPer1MTokens float64
}

// CostBreakdown provides a detailed cost report.
type CostBreakdown struct {
	TotalUSD     float64
	InputUSD     float64
	OutputUSD    float64
	CachedUSD    float64
	SavingsUSD   float64
	InputTokens  int
	OutputTokens int
	CachedTokens int
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

// Calculate computes the cost for a given usage.
func (cm *CostModel) Calculate(provider, model string, usage Usage) CostBreakdown {
	cm.mu.RLock()
	config, matched := cm.lookup(provider, model)
	cm.mu.RUnlock()

	if !matched {
		cm.warnMiss(provider, model)
	}

	// Non-cached input tokens at full input price
	nonCachedInput := usage.InputTokens - usage.CachedTokens
	nonCachedCost := float64(nonCachedInput) / 1_000_000.0 * config.InputCostPer1MTokens

	// Cached input tokens at cached (discounted) price
	cachedCost := float64(usage.CachedTokens) / 1_000_000.0 * config.CachedCostPer1MTokens

	// Output tokens at output price
	outputCost := float64(usage.OutputTokens) / 1_000_000.0 * config.OutputCostPer1MTokens

	// Savings from caching: what we would have paid for those tokens at full input rate
	savings := (float64(usage.CachedTokens)/1_000_000.0*config.InputCostPer1MTokens) - cachedCost

	return CostBreakdown{
		TotalUSD:     nonCachedCost + cachedCost + outputCost,
		InputUSD:     nonCachedCost + cachedCost,
		OutputUSD:    outputCost,
		CachedUSD:    cachedCost,
		SavingsUSD:   savings,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CachedTokens: usage.CachedTokens,
	}
}

// lookup returns the price config for (provider, model) and whether a match
// was found. The resolution order is:
//
//  1. Exact match in the provider's table.
//  2. Longest family-prefix match within the provider's table — i.e. the
//     longest registered key K such that the model starts with K and the
//     next character is a family separator (`-` or `.`). This lets
//     "claude-opus-4" match "claude-opus-4-7", "gpt-5" match "gpt-5.5",
//     and any dated suffix ("…-20250514", "…-2025-08-07") fall back to its
//     family.
//  3. Exact match in the "custom" table.
//
// Returns (CostConfig{}, false) when nothing matches; the caller surfaces
// the miss via warnMiss.
func (cm *CostModel) lookup(provider, model string) (CostConfig, bool) {
	if c, ok := exact(cm.prices, provider, model); ok {
		return c, true
	}
	if c, ok := longestFamily(cm.prices, provider, model); ok {
		return c, true
	}
	if c, ok := exact(cm.prices, "custom", model); ok {
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
