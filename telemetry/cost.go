package telemetry

import "sync"

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
	config := cm.lookup(provider, model)
	cm.mu.RUnlock()

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

func (cm *CostModel) lookup(provider, model string) CostConfig {
	// Try exact match first
	if p, ok := cm.prices[provider]; ok {
		if c, ok := p[model]; ok {
			return c
		}
	}
	// Fallback to custom provider
	if p, ok := cm.prices["custom"]; ok {
		if c, ok := p[model]; ok {
			return c
		}
	}
	// Default: zero cost (avoid division by zero)
	return CostConfig{}
}

func (cm *CostModel) loadDefaults() {
	for provider, models := range defaultCosts() {
		cm.prices[provider] = models
	}
}
