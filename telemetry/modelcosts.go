package telemetry

// defaultCosts returns the base pricing registry with official model costs.
// Prices are in USD per 1 million tokens.
//
// Keys are family-level identifiers (e.g. "claude-opus-4", "gpt-5") wherever
// pricing has remained stable across point releases. CostModel.lookup falls
// back from an exact model id to the longest registered family prefix, so a
// dated id like "claude-opus-4-7-20260301" or "gpt-5-2025-08-07" inherits its
// family price without needing an explicit entry. Override any specific
// version via CostModel.UpdateCost when the provider diverges.
//
// Prices reflect publicly listed rates as of May 2026; treat them as a
// reasonable default rather than billing-grade — call UpdateCost for
// contract pricing or new releases the framework hasn't been updated for.
func defaultCosts() map[string]map[string]CostConfig {
	return map[string]map[string]CostConfig{
		"openai": {
			// gpt-5 family — released August 2025.
			"gpt-5": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.125, // 90% discount on cache hits
			},
			"gpt-5-mini": {
				InputCostPer1MTokens:  0.25,
				OutputCostPer1MTokens: 2.00,
				CachedCostPer1MTokens: 0.025,
			},
			"gpt-5-nano": {
				InputCostPer1MTokens:  0.05,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.005,
			},
			// gpt-4.1 family.
			"gpt-4.1": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 8.00,
				CachedCostPer1MTokens: 0.50,
			},
			"gpt-4.1-mini": {
				InputCostPer1MTokens:  0.40,
				OutputCostPer1MTokens: 1.60,
				CachedCostPer1MTokens: 0.10,
			},
			"gpt-4.1-nano": {
				InputCostPer1MTokens:  0.10,
				OutputCostPer1MTokens: 0.40,
				CachedCostPer1MTokens: 0.025,
			},
			// gpt-4o family.
			"gpt-4o": {
				InputCostPer1MTokens:  2.50,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 1.25, // 50% discount
			},
			"gpt-4o-mini": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.075,
			},
			// o-series reasoning models. o-prefixed ids share `applyMaxTokens`
			// routing in provider/openai; the family-prefix lookup mirrors that.
			"o1": {
				InputCostPer1MTokens:  15.00,
				OutputCostPer1MTokens: 60.00,
				CachedCostPer1MTokens: 7.50,
			},
			"o1-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
			"o3": {
				InputCostPer1MTokens:  2.00,
				OutputCostPer1MTokens: 8.00,
				CachedCostPer1MTokens: 0.50,
			},
			"o3-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
			"o4-mini": {
				InputCostPer1MTokens:  1.10,
				OutputCostPer1MTokens: 4.40,
				CachedCostPer1MTokens: 0.55,
			},
		},
		"anthropic": {
			// Claude 4 family — opus/sonnet/haiku share family-level pricing
			// across minor versions (4.0 through 4.7+). Dated ids like
			// "claude-opus-4-7-20260301" inherit from these via prefix lookup.
			"claude-opus-4": {
				InputCostPer1MTokens:  15.00,
				OutputCostPer1MTokens: 75.00,
				CachedCostPer1MTokens: 1.50, // 90% discount
			},
			"claude-sonnet-4": {
				InputCostPer1MTokens:  3.00,
				OutputCostPer1MTokens: 15.00,
				CachedCostPer1MTokens: 0.30,
			},
			"claude-haiku-4": {
				InputCostPer1MTokens:  1.00,
				OutputCostPer1MTokens: 5.00,
				CachedCostPer1MTokens: 0.10,
			},
			// Legacy 3.x.
			"claude-3.5-sonnet": {
				InputCostPer1MTokens:  3.00,
				OutputCostPer1MTokens: 15.00,
				CachedCostPer1MTokens: 0.30,
			},
			"claude-3.5-haiku": {
				InputCostPer1MTokens:  0.80,
				OutputCostPer1MTokens: 4.00,
				CachedCostPer1MTokens: 0.08,
			},
			"claude-3-opus": {
				InputCostPer1MTokens:  15.00,
				OutputCostPer1MTokens: 75.00,
				CachedCostPer1MTokens: 1.50,
			},
		},
		"google": {
			// Gemini's id shape is "gemini-{ver}-{tier}", so the version sits
			// between the brand and the tier. Family-prefix lookup can't bridge
			// across that — list each (major.minor, tier) pair explicitly. New
			// minor releases that keep the same price reuse the same numbers.
			"gemini-2.5-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125, // 75% discount
			},
			"gemini-2.5-flash": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.0375,
			},
			"gemini-3-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125,
			},
			"gemini-3-flash": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.0375,
			},
			"gemini-3.1-pro": {
				InputCostPer1MTokens:  1.25,
				OutputCostPer1MTokens: 10.00,
				CachedCostPer1MTokens: 0.3125,
			},
			"gemini-3.1-flash": {
				InputCostPer1MTokens:  0.15,
				OutputCostPer1MTokens: 0.60,
				CachedCostPer1MTokens: 0.0375,
			},
			// gemini-3.5-flash — released May 2026; agent-tuned Flash priced
			// well above prior Flash tiers (10x input, 15x output).
			"gemini-3.5-flash": {
				InputCostPer1MTokens:  1.50,
				OutputCostPer1MTokens: 9.00,
				CachedCostPer1MTokens: 0.15,
			},
		},
	}
}
